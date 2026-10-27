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
	"os/exec"
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
	"github.com/xiemocun/ghfetch/internal/state"
)

// version is set by the release build via -ldflags "-X main.version=...".
var version = "0.8.0"

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
	case "completion":
		return cmdCompletion(args[1:])
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
  ghfetch install [--all] <owner/repo>[@tag]... [flags]
  ghfetch list    <owner/repo>[@tag]
  ghfetch completion <bash|zsh|fish>
  ghfetch version
  ghfetch help

INSTALL FLAGS
  --all                install every repository named on the command line
  --force              reinstall even when the recorded install matches
  --to <file>          use this exact release asset
  --asset <regexp>     choose the asset by regular expression
  --bin <name>         executable inside the archive (default: repo name)
  --dest <dir>         install directory (default: /usr/local/bin, else ~/.local/bin)
  --source <list>      comma separated mirror prefixes, "direct" to skip mirrors
  --require-checksum   fail when the release publishes no checksum file
  --attestation <policy>  SLSA provenance policy: off, optional or required
  --verify-attestation alias for --attestation=optional
  --require-attestation alias for --attestation=required
  --dry-run            resolve and print the plan, install nothing
  --json               machine readable report on stdout

Installing a tag that is already recorded as installed at the same path is a
no-op; the record lives in ~/.local/state/ghfetch/installed.json (override with
GHFETCH_STATE_FILE).

Set GH_TOKEN or GITHUB_TOKEN to raise the API rate limit.
`)
}

// releaseFor is the one API call the install path makes, behind a variable so
// tests can drive the whole install against a local server.
var releaseFor = func(ctx context.Context, repo, tag string) (*ghrelease.Release, error) {
	return ghrelease.New().Release(ctx, repo, tag)
}

// The status strings are part of the --json contract and are asserted by tests.
const (
	statusInstalled = "installed"
	statusSkipped   = "skipped"
	statusPlanned   = "planned"
	statusFailed    = "failed"
)

// installOptions carries the resolved flags so the single and the batch form
// cannot drift apart.
type installOptions struct {
	asset   string
	bin     string
	dest    string
	exact   string
	source  string
	needSum bool
	attMode attestationMode
	dryRun  bool
	json    bool
	force   bool
}

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var opts installOptions
	fs.StringVar(&opts.asset, "asset", "", "regexp selecting the release asset")
	fs.StringVar(&opts.bin, "bin", "", "executable inside the archive")
	fs.StringVar(&opts.dest, "dest", "", "install directory")
	fs.StringVar(&opts.exact, "to", "", "exact asset filename")
	fs.StringVar(&opts.source, "source", "", "comma separated mirror prefixes")
	fs.BoolVar(&opts.needSum, "require-checksum", false, "fail when no checksum file is published")
	attPolicy := fs.String("attestation", "", "SLSA provenance policy: off, optional or required")
	verifyAtt := fs.Bool("verify-attestation", false, "alias for --attestation=optional")
	requireAtt := fs.Bool("require-attestation", false, "alias for --attestation=required")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "resolve only, install nothing")
	fs.BoolVar(&opts.json, "json", false, "emit a machine readable report on stdout")
	all := fs.Bool("all", false, "install every repository named on the command line")
	fs.BoolVar(&opts.force, "force", false, "reinstall even when the recorded install matches")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("usage: ghfetch install <owner/repo>[@tag]")
	}
	// Several repositories is what --all is for; accepting a list silently
	// would make "install a b" look like it installed b only.
	if len(positional) > 1 && !*all {
		return fmt.Errorf("install takes one repository, got %d (pass --all to install several)", len(positional))
	}
	// Resolve the provenance policy before touching the network: a typo in
	// --attestation should not cost an API round trip.
	attMode, err := resolveAttestationMode(*attPolicy, *verifyAtt, *requireAtt)
	if err != nil {
		return err
	}
	opts.attMode = attMode

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	if *all {
		return installAll(ctx, positional, opts)
	}
	report, err := installOne(ctx, positional[0], opts)
	if err != nil {
		return err
	}
	// installOne owns the human-readable progress, so a batch and a single
	// install print the same lines; JSON is printed once, by the caller.
	if opts.json {
		return printJSON(report)
	}
	return nil
}

// installAll installs every spec, in order, and keeps going after a failure: one
// unbuildable repository must not leave the rest of a tool list uninstalled.
// The error returned at the end names what failed, so the exit code stays
// meaningful for scripts.
func installAll(ctx context.Context, specs []string, opts installOptions) error {
	reports := make([]installReport, 0, len(specs))
	var failed []string
	var installed, skipped int
	for i, spec := range specs {
		fmt.Fprintf(os.Stderr, "[%d/%d] %s\n", i+1, len(specs), spec)
		report, err := installOne(ctx, spec, opts)
		if err != nil {
			report = installReport{
				Repo: spec, Status: statusFailed, Error: err.Error(),
				// Keep the three-state attestation contract even for a failure:
				// nothing was verified, and the detail says why it is not
				// "skipped because you did not ask".
				Attestation: attestationSkipped, AttestationMode: opts.attMode.String(),
				AttestationDetail: "not attempted: the install failed",
			}
			failed = append(failed, spec)
			fmt.Fprintf(os.Stderr, "error: %s: %v\n", spec, err)
		}
		switch report.Status {
		case statusInstalled:
			installed++
		case statusSkipped:
			skipped++
		}
		reports = append(reports, report)
	}
	if opts.json {
		if err := printJSON(batchReport{OK: len(failed) == 0, Results: reports}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(os.Stderr, "%d installed, %d skipped, %d failed (of %d)\n",
			installed, skipped, len(failed), len(specs))
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d installs failed: %s", len(failed), len(specs), strings.Join(failed, ", "))
	}
	return nil
}

// installOne resolves and installs a single spec. It prints progress to stderr
// and returns the report --json prints, so both entry points share one path.
func installOne(ctx context.Context, spec string, o installOptions) (installReport, error) {
	repo, tag, err := splitSpec(spec)
	if err != nil {
		return installReport{}, err
	}
	target := platform.Detect()

	rel, err := releaseFor(ctx, repo, tag)
	if err != nil {
		return installReport{}, err
	}

	assetName, err := chooseAsset(rel, target, o.exact, o.asset)
	if err != nil {
		return installReport{}, err
	}
	asset, ok := rel.Asset(assetName)
	if !ok {
		return installReport{}, fmt.Errorf("release %s has no asset named %q", rel.Tag, assetName)
	}

	want := o.bin
	if want == "" {
		want = path.Base(repo) + target.BinSuffix()
	} else if target.OS == "windows" && !strings.HasSuffix(want, ".exe") {
		want += ".exe"
	}

	prefixes, err := source.ParsePrefixes(o.source)
	if err != nil {
		return installReport{}, err
	}
	chain := source.New(prefixes)
	installDir, err := resolveDest(o.dest)
	if err != nil {
		return installReport{}, err
	}
	final := filepath.Join(installDir, want)

	report := installReport{
		Repo: repo, Tag: rel.Tag, Asset: asset.Name, Size: asset.Size,
		Binary: want, Dest: installDir, Status: statusPlanned,
		// --json consumers must never have to read an empty attestation field
		// and guess whether verification ran: "skipped" is stated up front and
		// only an actual verification pass overwrites it.
		Attestation: attestationSkipped, AttestationMode: o.attMode.String(),
	}

	// Idempotency comes before the download, not after: the whole point is to
	// not fetch a binary that is already on disk at this tag.
	if !o.dryRun && !o.force {
		rec, skip, why := alreadyInstalled(installDir, repo, rel)
		if skip {
			report.Status = statusSkipped
			report.Installed = rec.Path
			report.Binary = filepath.Base(rec.Path)
			fmt.Fprintf(os.Stderr, "%s %s is already installed at %s (--force reinstalls)\n", repo, rel.Tag, rec.Path)
			return report, nil
		}
		if why != "" {
			fmt.Fprintf(os.Stderr, "note: reinstalling %s: %s\n", want, why)
		}
	}

	if !o.json {
		fmt.Fprintf(os.Stderr, "%s %s -> %s (%s)\n", repo, rel.Tag, asset.Name, humanSize(asset.Size))
	}
	if o.dryRun {
		report.DryRun = true
		report.Candidates = chain.Candidates(asset.URL)
		if o.json {
			return report, nil
		}
		for _, candidate := range report.Candidates {
			fmt.Fprintf(os.Stderr, "  try:     %s\n", candidate)
		}
		fmt.Fprintf(os.Stderr, "  binary:  %s\n  install: %s\n", want, final)
		return report, nil
	}

	data, servedBy, err := chain.Get(ctx, asset.URL)
	if err != nil {
		return installReport{}, err
	}
	sum := sha256.Sum256(data)
	report.Source = hostOf(servedBy)
	report.SHA256 = hex.EncodeToString(sum[:])
	if !o.json {
		fmt.Fprintf(os.Stderr, "downloaded %s from %s\n", humanSize(int64(len(data))), report.Source)
	}

	method, err := verify(ctx, chain, rel, asset, sum, o.needSum)
	if err != nil {
		return installReport{}, err
	}
	report.Verified = method

	// Called even when the policy is off: verifyAttestation returns immediately
	// with "skipped" so the report always carries one of the three statuses.
	status, detail, err := verifyAttestation(ctx, repo, data, report.SHA256, o.attMode)
	if err != nil {
		return installReport{}, err
	}
	report.Attestation = status
	report.AttestationDetail = detail

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
			final = filepath.Join(installDir, want)
		}
	}
	if err != nil {
		return installReport{}, fmt.Errorf("%s: %w (use --bin to name the executable)", asset.Name, err)
	}
	if picked != "" && picked != want {
		fmt.Fprintf(os.Stderr, "note: archive member %q installed as %q\n", picked, want)
	}

	installed, err := installBinary(installDir, want, body, mode)
	if err != nil {
		return installReport{}, err
	}
	report.Installed = installed
	report.Status = statusInstalled
	// Recorded after the rename: a record pointing at a file that is not there
	// would make the next install skip a binary it never wrote.
	rememberInstall(installed, repo, rel, asset, body)
	if o.json {
		return report, nil
	}
	fmt.Fprintf(os.Stderr, "installed %s (%s)\n", installed, humanSize(int64(len(body))))
	if !onPath(installDir) {
		fmt.Fprintf(os.Stderr, "note: %s is not in PATH\n", installDir)
	}
	return report, nil
}

// alreadyInstalled reports whether this repository is already installed at this
// release in destDir, so the download can be skipped.
//
// The state file is the only source of truth. Asking the installed binary for
// its version would tie the decision to each project's flag spelling ("-v",
// "--version", "version", a bare tag), which is exactly the kind of guessing
// that silently installs the wrong thing. The digest on disk is re-checked as
// well, so a binary someone replaced by hand is installed again instead of
// being trusted because a record exists.
func alreadyInstalled(destDir, repo string, rel *ghrelease.Release) (rec state.Record, skip bool, why string) {
	store, ok := stateStore()
	if !ok {
		return state.Record{}, false, ""
	}
	rec, have := store.Get(state.Key(repo, destDir))
	if !have {
		return state.Record{}, false, ""
	}
	switch {
	case rec.Tag != rel.Tag:
		return rec, false, fmt.Sprintf("%s is installed at %s, the release is %s", repo, rec.Tag, rel.Tag)
	case rec.SHA256 == "":
		return rec, false, "the recorded digest is missing"
	case rec.Path == "":
		return rec, false, "the record does not say where the binary is"
	case filepath.Dir(filepath.Clean(rec.Path)) != filepath.Clean(destDir):
		// A record pointing outside the destination says nothing about the file
		// this install is about to write.
		return rec, false, fmt.Sprintf("the record points at %s, outside %s", rec.Path, destDir)
	}
	data, err := os.ReadFile(rec.Path)
	if err != nil {
		return rec, false, fmt.Sprintf("cannot read %s: %v", rec.Path, err)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != rec.SHA256 {
		return rec, false, fmt.Sprintf("%s no longer matches the recorded digest", rec.Path)
	}
	return rec, true, ""
}

// stateStore resolves the install record. A machine where the record cannot be
// located still installs; it just cannot skip.
func stateStore() (state.Store, bool) {
	path, err := state.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: install record disabled: %v\n", err)
		return state.Store{}, false
	}
	return state.Store{Path: path}, true
}

// rememberInstall records what was installed. Failing to write the record is a
// warning: the binary is already in place and correct, and the only consequence
// is that the next run downloads it again.
func rememberInstall(installed, repo string, rel *ghrelease.Release, asset ghrelease.Asset, body []byte) {
	store, ok := stateStore()
	if !ok {
		return
	}
	sum := sha256.Sum256(body)
	rec := state.Record{
		Repo: repo, Tag: rel.Tag,
		Path: installed, Binary: filepath.Base(installed), Asset: asset.Name,
		SHA256:      hex.EncodeToString(sum[:]),
		InstalledAt: time.Now().UTC(),
	}
	if err := store.Put(state.Key(repo, filepath.Dir(installed)), rec); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record the install in %s: %v\n", store.Path, err)
	}
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

// verifyAttestation checks the SLSA build provenance of the downloaded bytes.
//
// The signature work is delegated to `gh attestation verify` on purpose: that
// command already carries Sigstore's trust roots, the Rekor lookup and the
// bundle format, and it is the verifier GitHub documents. Reimplementing it
// here would mean shipping a TUF root store inside an installer, which is a
// much larger trust surface than the installer itself.
//
// Its exit status is NOT trusted on its own. `gh attestation verify` exits 0
// when the attestations API answers 404 for the digest, printing the HTTP error
// to stderr, so a wrapper that only reads the exit code reports "verified" for
// an artifact nobody ever attested. The JSON report is parsed instead, and the
// attested subject digest must equal the digest of the bytes we downloaded.
//
// The policy decides what a negative result costs, never whether it is visible.
// The returned status is always one of verified, skipped or failed, so a caller
// that only reads the value can tell "not requested" from "requested and not
// proved". Returning "" for both, as 0.6.0 did, made a downgraded check
// indistinguishable from no check at all.
func verifyAttestation(ctx context.Context, repo string, data []byte, digest string, mode attestationMode) (status, detail string, err error) {
	if mode == attestationOff {
		return attestationSkipped, "", nil
	}

	ghBin, lookErr := exec.LookPath("gh")
	if lookErr != nil {
		return attestationOutcome(repo, mode, "gh CLI not found in PATH; install it to verify build provenance")
	}

	// gh matches attestations by the subject's digest, so the file name is
	// irrelevant; the bytes are exactly the release asset.
	tmp, err := os.CreateTemp("", "ghfetch-attest-*")
	if err != nil {
		return attestationFailed, err.Error(), fmt.Errorf("create temp file for attestation check: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, werr := tmp.Write(data); werr != nil {
		tmp.Close()
		return attestationFailed, werr.Error(), fmt.Errorf("write temp file for attestation check: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return attestationFailed, cerr.Error(), fmt.Errorf("close temp file for attestation check: %w", cerr)
	}

	cmd := exec.CommandContext(ctx, ghBin, "attestation", "verify", tmpName, "--repo", repo, "--format", "json")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, runErr := cmd.Output()

	problem := ""
	switch {
	case runErr != nil:
		problem = attestationProblem(stderr.String(), runErr)
	case !attestationCovers(out, digest, repo):
		problem = "no attestation covers this digest for " + repo
	}
	if problem != "" {
		return attestationOutcome(repo, mode, problem)
	}

	fmt.Fprintf(os.Stderr, "build provenance verified (gh attestation, %s)\n", repo)
	return attestationVerified, "", nil
}

// attestationProblem reduces the verifier's complaint to something a user can
// read in a --json field or a one-line warning. gh prints the whole API URL for
// a 404 on the attestations endpoint, which is noise: the meaning is that the
// project publishes no provenance for this digest.
func attestationProblem(stderrOut string, runErr error) string {
	if strings.Contains(stderrOut, "HTTP 404") {
		return "no attestations published for this digest (HTTP 404)"
	}
	problem := firstLine(stderrOut)
	if problem == "no output" {
		problem = firstLine(runErr.Error())
	}
	return truncate(problem, 200)
}

// truncate keeps one line of errno-ish text from dominating the report.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// attestationOutcome turns a verification problem into the triple the caller and
// --json need. Under the optional policy the install continues but the report
// says failed; under the required policy it is an error. Neither path returns an
// empty status, which is the whole point: a skipped check is not a passed check.
func attestationOutcome(repo string, mode attestationMode, problem string) (string, string, error) {
	if mode == attestationRequired {
		return attestationFailed, problem, fmt.Errorf("build provenance did not verify for %s: %s (--attestation=required)", repo, problem)
	}
	fmt.Fprintf(os.Stderr, "warning: build provenance did not verify for %s (%s)\n", repo, problem)
	return attestationFailed, problem, nil
}

// attestationMode is how hard ghfetch has to try to prove build provenance.
type attestationMode int

const (
	attestationOff attestationMode = iota
	attestationOptional
	attestationRequired
)

// The status strings are part of the --json contract and are asserted by tests.
const (
	attestationVerified = "verified"
	attestationSkipped  = "skipped"
	attestationFailed   = "failed"
)

func (m attestationMode) String() string {
	switch m {
	case attestationOptional:
		return "optional"
	case attestationRequired:
		return "required"
	default:
		return "off"
	}
}

func parseAttestationMode(s string) (attestationMode, error) {
	switch s {
	case "off":
		return attestationOff, nil
	case "optional":
		return attestationOptional, nil
	case "required":
		return attestationRequired, nil
	default:
		return attestationOff, fmt.Errorf("unknown attestation policy %q, want off, optional or required", s)
	}
}

// resolveAttestationMode folds --attestation and the two legacy boolean flags
// into one policy. The legacy flags are aliases, and the stricter request wins:
// a script that passes both --attestation=off and --require-attestation must not
// lose the check it asked for just because of argument order.
func resolveAttestationMode(policy string, verify, require bool) (attestationMode, error) {
	mode := attestationOff
	if verify {
		mode = attestationOptional
	}
	if require {
		mode = attestationRequired
	}
	if policy == "" {
		return mode, nil
	}
	parsed, err := parseAttestationMode(policy)
	if err != nil {
		return attestationOff, err
	}
	if parsed > mode {
		return parsed, nil
	}
	return mode, nil
}

// attestationCovers reports whether the verifier's JSON report contains at least
// one verified attestation whose subject list includes digest AND whose signing
// certificate names repo as the source repository.
//
// The repository check is deliberate redundancy. `gh attestation verify --repo`
// already compares the certificate identity with the requested repository, but
// that behaviour lives in gh, not in this tool; asserting it here means a gh
// change, a wrapper bug, or a differently scoped verification cannot turn into a
// passed check for an artifact from someone else's build.
func attestationCovers(report []byte, digest, repo string) bool {
	if strings.TrimSpace(string(report)) == "" {
		return false
	}
	var entries []struct {
		VerificationResult struct {
			Signature struct {
				Certificate struct {
					SourceRepositoryURI string `json:"sourceRepositoryURI"`
				} `json:"certificate"`
			} `json:"signature"`
			Statement struct {
				Subject []struct {
					Digest map[string]string `json:"digest"`
				} `json:"subject"`
			} `json:"statement"`
		} `json:"verificationResult"`
	}
	if err := json.Unmarshal(report, &entries); err != nil || len(entries) == 0 {
		return false
	}
	want := normalizeRepoURI(repo)
	for _, e := range entries {
		if normalizeRepoURI(e.VerificationResult.Signature.Certificate.SourceRepositoryURI) != want {
			continue
		}
		for _, s := range e.VerificationResult.Statement.Subject {
			if strings.EqualFold(s.Digest["sha256"], digest) {
				return true
			}
		}
	}
	return false
}

// normalizeRepoURI reduces "https://github.com/Owner/Repo.git/" to
// "owner/repo" so certificate URIs and a --repo argument compare cleanly.
func normalizeRepoURI(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return s
}

// firstLine keeps an error report short when a verifier prints a paragraph.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "no output"
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
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
	Repo      string `json:"repo"`
	Tag       string `json:"tag"`
	Asset     string `json:"asset"`
	Size      int64  `json:"size"`
	Binary    string `json:"binary"`
	Dest      string `json:"dest"`
	Installed string `json:"installed,omitempty"`
	// Status is always present and is one of installed, skipped, planned or
	// failed, so a caller never has to infer what happened from the other
	// fields being empty.
	Status   string `json:"status"`
	Error    string `json:"error,omitempty"`
	Source   string `json:"source,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Verified string `json:"verified,omitempty"`
	// Attestation is always present and is one of verified, skipped or failed.
	// An empty value would mean "the caller has to guess".
	Attestation       string   `json:"attestation"`
	AttestationMode   string   `json:"attestation_mode"`
	AttestationDetail string   `json:"attestation_detail,omitempty"`
	Candidates        []string `json:"candidates,omitempty"`
	DryRun            bool     `json:"dry_run,omitempty"`
}

type hostReport struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// batchReport is what --all --json prints: one entry per requested repository,
// in the order they were named, so a pipeline can find the failures without
// parsing stderr.
type batchReport struct {
	OK      bool            `json:"ok"`
	Results []installReport `json:"results"`
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
