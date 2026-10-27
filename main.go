// Command ghfetch installs binaries published on GitHub Releases, falling back
// through a chain of mirrors when github.com is slow or unreachable, and
// verifying the published sha256 digest before anything is written to disk.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
var version = "0.1.0"

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

	fmt.Fprintf(os.Stderr, "%s %s -> %s (%s)\n", repo, rel.Tag, asset.Name, humanSize(asset.Size))
	if *dryRun {
		for _, candidate := range chain.Candidates(asset.URL) {
			fmt.Fprintf(os.Stderr, "  try:     %s\n", candidate)
		}
		fmt.Fprintf(os.Stderr, "  binary:  %s\n  install: %s\n", want, filepath.Join(installDir, want))
		return nil
	}

	data, servedBy, err := chain.Get(ctx, asset.URL)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "downloaded %s from %s\n", humanSize(int64(len(data))), hostOf(servedBy))

	if err := verify(ctx, chain, rel, asset, data, *needSum); err != nil {
		return err
	}

	picked, body, mode, err := archive.Extract(data, asset.Name, want)
	if err != nil && errors.Is(err, archive.ErrNotFound) {
		// Repos disagree about the name of the binary inside the archive. Fall
		// back to the largest executable rather than failing the install.
		fmt.Fprintf(os.Stderr, "note: %q is not in %s, using the largest executable instead\n", want, asset.Name)
		picked, body, mode, err = archive.Extract(data, asset.Name, "")
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

// verify compares the download against the published digest. It fails closed on
// a mismatch and, with requireSum, on a missing manifest.
func verify(ctx context.Context, chain *source.Chain, rel *ghrelease.Release, asset ghrelease.Asset, data []byte, requireSum bool) error {
	sumAsset, hasSum := rel.ChecksumAsset()
	if !hasSum {
		if requireSum {
			return fmt.Errorf("release %s publishes no checksum file", rel.Tag)
		}
		fmt.Fprintln(os.Stderr, "warning: no checksum file in release, installing unverified bytes")
		return nil
	}
	manifest, _, err := chain.Get(ctx, sumAsset.URL)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", sumAsset.Name, err)
	}
	wantDigest, ok := checksum.Lookup(checksum.Parse(manifest), asset.Name)
	if !ok {
		if requireSum {
			return fmt.Errorf("%s lists no digest for %s", sumAsset.Name, asset.Name)
		}
		fmt.Fprintf(os.Stderr, "warning: %s has no entry for %s, skipping verification\n", sumAsset.Name, asset.Name)
		return nil
	}
	actual := sha256.Sum256(data)
	actualHex := hex.EncodeToString(actual[:])
	if actualHex != wantDigest {
		return fmt.Errorf("checksum mismatch for %s: published %s, downloaded %s", asset.Name, wantDigest, actualHex)
	}
	fmt.Fprintln(os.Stderr, "checksum verified (sha256)")
	return nil
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

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
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