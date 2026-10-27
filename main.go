// Command ghfetch installs binaries published on GitHub Releases, falling back
// through a chain of mirrors when github.com is slow or unreachable, and
// verifying the published sha256 digest before anything is written to disk.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/xiemocun/ghfetch/internal/archive"
	"github.com/xiemocun/ghfetch/internal/checksum"
	"github.com/xiemocun/ghfetch/internal/ghrelease"
	"github.com/xiemocun/ghfetch/internal/platform"
	"github.com/xiemocun/ghfetch/internal/source"
)

// version is set by the release build via -ldflags "-X main.version=...".
var version = "0.3.0"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ghfetch: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	switch args[0] {
	case "install":
		return cmdInstall(args[1:])
	case "list":
		return cmdList(args[1:])
	case "version", "-v", "--version":
		fmt.Printf("ghfetch %s (%s, %s)\n", version, runtime.Version(), platform.Detect())
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q, run 'ghfetch help'", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ghfetch - install GitHub release binaries without fighting the network

USAGE
  ghfetch install <owner/repo>[@tag] [flags]
  ghfetch list    <owner/repo>[@tag]
  ghfetch version
  ghfetch help

INSTALL FLAGS
  --to <file>          use this exact release asset
  --asset <regexp>     choose the asset by regular expression
  --bin <name>         executable inside the archive (default: repo name)
  --dest <dir>         install directory (default: /usr/local/bin, else ~/.local/bin)
  --source <list>      comma separated mirror prefixes, "direct" to skip mirrors
  --require-checksum   fail when the release publishes no checksum file
  --dry-run            resolve and print the plan, install nothing

Set GH_TOKEN or GITHUB_TOKEN to raise the API rate limit.
`)
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	pick := fs.String("asset", "", "regexp selecting the release asset")
	binName := fs.String("bin", "", "executable inside the archive")
	dest := fs.String("dest", "", "install directory")
	exact := fs.String("to", "", "exact asset filename")
	sourceList := fs.String("source", "", "comma separated mirror prefixes")
	needSum := fs.Bool("require-checksum", false, "fail when no checksum file is published")
	dryRun := fs.Bool("dry-run", false, "resolve only, install nothing")
	asJSON := fs.Bool("json", false, "emit a machine readable report on stdout")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: ghfetch install <owner/repo>[@tag]")
	}

	repo, tag, err := splitSpec(positional[0])
	if err != nil {
		return err
	}
	target := platform.Detect()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	rel, err := ghrelease.New().Release(ctx, repo, tag)
	if err != nil {
		return err
	}

	assetName, err := chooseAsset(rel, target, *exact, *pick)
	if err != nil {
		return err
	}
	asset, ok := rel.Asset(assetName)
	if !ok {
		return fmt.Errorf("release %s has no asset named %q", rel.Tag, assetName)
	}

	want := *binName
	if want == "" {
		want = path.Base(repo) + target.BinSuffix()
	} else if target.OS == "windows" && !strings.HasSuffix(want, ".exe") {
		want += ".exe"
	}

	prefixes, err := source.ParsePrefixes(*sourceList)
	if err != nil {
		return err
	}
	chain := source.New(prefixes)
	installDir, err := resolveDest(*dest)
	if err != nil {
		return err
	}

	if !*asJSON {
		fmt.Fprintf(os.Stderr, "%s %s -> %s (%s)\n", repo, rel.Tag, asset.Name, humanSize(asset.Size))
	}
	report := installReport{
		Repo: repo, Tag: rel.Tag, Asset: asset.Name, Size: asset.Size,
		Binary: want, Dest: installDir,
	}
	if *dryRun {
		report.DryRun = true
		report.Candidates = chain.Candidates(asset.URL)
		if *asJSON {
			return printJSON(report)
		}
		for _, candidate := range report.Candidates {
			fmt.Fprintf(os.Stderr, "  try:     %s\n", candidate)
		}
		fmt.Fprintf(os.Stderr, "  binary:  %s\n  install: %s\n", want, filepath.Join(installDir, want))
		return nil
	}

	data, servedBy, err := chain.Get(ctx, asset.URL)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	report.Source = hostOf(servedBy)
	report.SHA256 = hex.EncodeToString(sum[:])
	if !*asJSON {
		fmt.Fprintf(os.Stderr, "downloaded %s from %s\n", humanSize(int64(len(data))), report.Source)
	}

	method, err := verify(ctx, chain, rel, asset, sum, *needSum)
	if err != nil {
		return err
	}
	report.Verified = method

	picked, body, mode, err := archive.Extract(data, asset.Name, want)
	if err != nil && errors.Is(err, archive.ErrNotFound) {
		// Repos disagree about the name of the binary inside the archive. Fall
		// back to the largest executable rather than failing the install.
		fmt.Fprintf(os.Stderr, "note: %q is not in %s, using the largest executable instead\n", want, asset.Name)
		picked, body, mode, err = archive.Extract(data, asset.Name, "")
		if err == nil && picked != "" && platform.Score(picked, target) == 0 {
			// The member is a clean binary name ("rg"), not a platform-stamped
			// artifact ("tool_linux_amd64"), so keep it: installing ripgrep as
			// "rg" is what the user meant.
			want = picked
			report.Binary = want
		}
	}
	if err != nil {
		return fmt.Errorf("%s: %w (use --bin to name the executable)", asset.Name, err)
	}
	if picked != "" && picked != want {
		fmt.Fprintf(os.Stderr, "note: archive member %q installed as %q\n", picked, want)
	}

	installed, err := installBinary(installDir, want, body, mode)
	if err != nil {
		return err
	}
	report.Installed = installed
	if *asJSON {
		return printJSON(report)
	}
	fmt.Fprintf(os.Stderr, "installed %s (%s)\n", installed, humanSize(int64(len(body))))
	if !onPath(installDir) {
		fmt.Fprintf(os.Stderr, "note: %s is not in PATH\n", installDir)
	}
	return nil
}

// chooseAsset resolves the asset to install: an explicit name, a regexp, then
// automatic platform matching.
func chooseAsset(rel *ghrelease.Release, target platform.Target, exact, pattern string) (string, error) {
	if exact != "" {
		if _, ok := rel.Asset(exact); !ok {
			return "", fmt.Errorf("release %s has no asset named %q", rel.Tag, exact)
		}
		return exact, nil
	}
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("--asset: %w", err)
		}
		for _, a := range rel.Assets {
			if re.MatchString(a.Name) {
				return a.Name, nil
			}
		}
		return "", fmt.Errorf("--asset %q matched no asset in %s", pattern, rel.Tag)
	}
	name, err := platform.Best(rel.Names(), target)
	if err != nil {
		return "", fmt.Errorf("%w for release %s", err, rel.Tag)
	}
	return name, nil
}

// verify compares the downloaded bytes against the digest the release
// publishes: a full manifest first, then a per-asset sidecar. It returns the
// name of the mechanism used, or "" when the release published no digest at
// all. A mismatch always fails; a missing digest fails only with requireSum.
func verify(ctx context.Context, chain *source.Chain, rel *ghrelease.Release, asset ghrelease.Asset, sum [32]byte, requireSum bool) (string, error) {
	actualHex := hex.EncodeToString(sum[:])

	var (
		wantDigest string
		source     string
		found      bool
	)
	if sumAsset, ok := rel.ChecksumAsset(); ok {
		manifest, _, err := chain.Get(ctx, sumAsset.URL)
		if err != nil {
			return "", fmt.Errorf("fetch %s: %w", sumAsset.Name, err)
		}
		wantDigest, found = checksum.Lookup(checksum.Parse(manifest), asset.Name)
		source = sumAsset.Name
	}
	if !found {
		if side, ok := rel.DigestFor(asset.Name); ok {
			blob, _, err := chain.Get(ctx, side.URL)
			if err != nil {
				return "", fmt.Errorf("fetch %s: %w", side.Name, err)
			}
			wantDigest, found = checksum.ParseSingle(blob)
			source = side.Name
		}
	}

	switch {
	case !found:
		if requireSum {
			return "", fmt.Errorf("release %s publishes no digest for %s", rel.Tag, asset.Name)
		}
		fmt.Fprintln(os.Stderr, "warning: no usable digest in the release, installing unverified bytes")
		return "", nil
	case wantDigest != actualHex:
		return "", fmt.Errorf("checksum mismatch for %s: %s says %s, downloaded %s",
			asset.Name, source, wantDigest, actualHex)
	}
	fmt.Fprintf(os.Stderr, "checksum verified (sha256, from %s)\n", source)
	return "sha256", nil
}

// installBinary writes body next to its final path and renames it into place, so
// a reader never observes a half-written executable.
func installBinary(dir, name string, body []byte, mode os.FileMode) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".ghfetch-*")
	if err != nil {
		return "", fmt.Errorf("cannot write to %s: %w (try --dest ~/.local/bin)", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(mode | 0o755); err != nil {
		tmp.Close()
		return "", fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmpName, err)
	}

	final := filepath.Join(dir, name)
	if err := os.Rename(tmpName, final); err != nil {
		return "", fmt.Errorf("install to %s: %w", final, err)
	}
	return final, nil
}

// installReport, listReport and friends are the machine readable output of
// --json. Field names are snake_case because shell and CI consumers read them.
type installReport struct {
	Repo       string   `json:"repo"`
	Tag        string   `json:"tag"`
	Asset      string   `json:"asset"`
	Size       int64    `json:"size"`
	Binary     string   `json:"binary"`
	Dest       string   `json:"dest"`
	Installed  string   `json:"installed,omitempty"`
	Source     string   `json:"source,omitempty"`
	SHA256     string   `json:"sha256,omitempty"`
	Verified   string   `json:"verified,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	DryRun     bool     `json:"dry_run,omitempty"`
}

type hostReport struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type listAsset struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
	Selected bool   `json:"selected"`
}

type listReport struct {
	Repo     string      `json:"repo"`
	Tag      string      `json:"tag"`
	Host     hostReport  `json:"host"`
	Selected string      `json:"selected"`
	Assets   []listAsset `json:"assets"`
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON on stdout")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: ghfetch list <owner/repo>[@tag]")
	}
	repo, tag, err := splitSpec(positional[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	rel, err := ghrelease.New().Release(ctx, repo, tag)
	if err != nil {
		return err
	}
	target := platform.Detect()
	auto, _ := platform.Best(rel.Names(), target)

	if *asJSON {
		report := listReport{
			Repo: repo, Tag: rel.Tag,
			Host:     hostReport{OS: target.OS, Arch: target.Arch},
			Selected: auto,
		}
		for _, a := range rel.Assets {
			report.Assets = append(report.Assets, listAsset{
				Name: a.Name, Size: a.Size, URL: a.URL, Selected: a.Name == auto,
			})
		}
		return printJSON(report)
	}

	fmt.Printf("%s %s (host: %s)\n", repo, rel.Tag, target)
	for _, a := range rel.Assets {
		marker := "  "
		if a.Name == auto {
			marker = "* "
		}
		fmt.Printf("%s%-58s %10s\n", marker, a.Name, humanSize(a.Size))
	}
	return nil
}

// parseInterspersed parses flags that may appear before or after the positional
// argument. The standard flag package stops at the first non-flag word, which
// would make "ghfetch install cli/cli --dest /tmp" reject the flags.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// splitSpec accepts owner/repo and owner/repo@tag.
func splitSpec(spec string) (repo, tag string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", errors.New("missing <owner/repo>")
	}
	repo, tag, _ = strings.Cut(spec, "@")
	repo, err = ghrelease.NormalizeRepo(repo)
	if err != nil {
		return "", "", err
	}
	return repo, strings.TrimSpace(tag), nil
}

// resolveDest picks the install directory: the flag, then /usr/local/bin when we
// may write there, then ~/.local/bin.
func resolveDest(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if os.Geteuid() == 0 {
		return "/usr/local/bin", nil
	}
	if writable("/usr/local/bin") {
		return "/usr/local/bin", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "bin"), nil
}

func writable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".ghfetch-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	return true
}

func onPath(dir string) bool {
	clean := filepath.Clean(dir)
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if entry != "" && filepath.Clean(entry) == clean {
			return true
		}
	}
	return false
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Host
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}