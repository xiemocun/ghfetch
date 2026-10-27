package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xiemocun/ghfetch/internal/ghrelease"
	"github.com/xiemocun/ghfetch/internal/state"
)

// fakeCDN serves the release assets of the fake repositories below and counts
// how often each one was downloaded, which is how "skipped" is proved to mean
// "no network" rather than "downloaded again".
type fakeCDN struct {
	srv   *httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	hits  map[string]int
}

func newFakeCDN(t *testing.T) *fakeCDN {
	t.Helper()
	cdn := &fakeCDN{files: map[string][]byte{}, hits: map[string]int{}}
	cdn.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		cdn.mu.Lock()
		body, ok := cdn.files[name]
		if ok {
			cdn.hits[name]++
		}
		cdn.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(cdn.srv.Close)
	return cdn
}

func (c *fakeCDN) add(name string, body []byte) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.files[name] = body
	return c.srv.URL + "/" + name
}

func (c *fakeCDN) downloads(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[name]
}

// tarball builds the .tar.gz release artifact the installer is tested against.
func tarball(t *testing.T, member string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: member, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeRelease publishes one asset plus a checksum manifest for it, the shape
// nearly every project uses.
func fakeRelease(t *testing.T, cdn *fakeCDN, tag, assetName string, body []byte) *ghrelease.Release {
	t.Helper()
	assetURL := cdn.add(assetName, body)
	sum := sha256.Sum256(body)
	manifest := []byte(fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName))
	// One manifest per release: sharing a single path would make the last
	// repository's digests answer for all of them.
	manifestName := "checksums-" + tag + ".txt"
	manifestURL := cdn.add(manifestName, manifest)
	return &ghrelease.Release{
		Tag: tag,
		Assets: []ghrelease.Asset{
			{Name: assetName, URL: assetURL, Size: int64(len(body))},
			{Name: manifestName, URL: manifestURL, Size: int64(len(manifest))},
		},
	}
}

// stubReleases replaces the Release API call for the duration of one test. The
// map is consulted on every call, so a test can move a tag between installs.
func stubReleases(t *testing.T, rels map[string]*ghrelease.Release) {
	t.Helper()
	previous := releaseFor
	t.Cleanup(func() { releaseFor = previous })
	releaseFor = func(_ context.Context, repo, tag string) (*ghrelease.Release, error) {
		rel, ok := rels[repo]
		if !ok {
			return nil, fmt.Errorf("%s has no published release", repo)
		}
		if tag != "" && tag != rel.Tag {
			return nil, fmt.Errorf("%s has no release tagged %q", repo, tag)
		}
		return rel, nil
	}
}

// isolateState points the install record at a temp file and the install
// directory at a temp dir, so tests never touch the developer's machine.
func isolateState(t *testing.T) (dest, statePath string) {
	t.Helper()
	dir := t.TempDir()
	statePath = filepath.Join(dir, "state", "installed.json")
	t.Setenv("GHFETCH_STATE_FILE", statePath)
	dest = filepath.Join(dir, "bin")
	return dest, statePath
}

func testOptions(dest, assetPattern string) installOptions {
	return installOptions{dest: dest, asset: assetPattern, json: true, attMode: attestationOff}
}

// fixture wires one installable repository and returns the options to install
// it with.
func fixture(t *testing.T) (cdn *fakeCDN, rels map[string]*ghrelease.Release, assetName string, opts installOptions) {
	t.Helper()
	cdn = newFakeCDN(t)
	dest, _ := isolateState(t)
	rels = map[string]*ghrelease.Release{}
	assetName = "tool_linux.tar.gz"
	rels["acme/tool"] = fakeRelease(t, cdn, "v1.2.3", assetName, tarball(t, "tool", []byte("#!/bin/sh\necho tool v1.2.3\n")))
	stubReleases(t, rels)
	return cdn, rels, assetName, testOptions(dest, `_linux\.tar\.gz$`)
}

func TestInstallIsIdempotentWithinATag(t *testing.T) {
	cdn, _, assetName, opts := fixture(t)

	first, err := installOne(context.Background(), "acme/tool", opts)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if first.Status != statusInstalled {
		t.Fatalf("first install status = %q, want %q", first.Status, statusInstalled)
	}
	if first.Verified != "sha256" {
		t.Errorf("first install verified = %q, want the digest mechanism", first.Verified)
	}

	second, err := installOne(context.Background(), "acme/tool", opts)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second.Status != statusSkipped {
		t.Fatalf("second install status = %q, want %q — a recorded tag must not be reinstalled", second.Status, statusSkipped)
	}
	if second.Installed != first.Installed {
		t.Errorf("skipped report points at %q, want %q", second.Installed, first.Installed)
	}
	if got := cdn.downloads(assetName); got != 1 {
		t.Errorf("asset downloaded %d times, want 1: a skipped install must not fetch the binary", got)
	}

	data, err := os.ReadFile(first.Installed)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(data) != "#!/bin/sh\necho tool v1.2.3\n" {
		t.Errorf("installed bytes = %q", data)
	}
}

func TestForceReinstallsRecordedTag(t *testing.T) {
	cdn, _, assetName, opts := fixture(t)
	if _, err := installOne(context.Background(), "acme/tool", opts); err != nil {
		t.Fatalf("first install: %v", err)
	}

	forced := opts
	forced.force = true
	report, err := installOne(context.Background(), "acme/tool", forced)
	if err != nil {
		t.Fatalf("forced install: %v", err)
	}
	if report.Status != statusInstalled {
		t.Errorf("--force status = %q, want %q", report.Status, statusInstalled)
	}
	if got := cdn.downloads(assetName); got != 2 {
		t.Errorf("asset downloaded %d times, want 2", got)
	}
}

// TestReinstallWhenLocalBinaryChanged pins the digest half of the decision: a
// record alone is not proof, the bytes on disk have to still match it.
func TestReinstallWhenLocalBinaryChanged(t *testing.T) {
	cdn, _, assetName, opts := fixture(t)
	first, err := installOne(context.Background(), "acme/tool", opts)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := os.WriteFile(first.Installed, []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := installOne(context.Background(), "acme/tool", opts)
	if err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	if report.Status != statusInstalled {
		t.Fatalf("status = %q, want %q: a modified binary must be replaced", report.Status, statusInstalled)
	}
	if got := cdn.downloads(assetName); got != 2 {
		t.Errorf("asset downloaded %d times, want 2", got)
	}
	data, err := os.ReadFile(report.Installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho tool v1.2.3\n" {
		t.Errorf("installed bytes = %q, want the release content back", data)
	}
}

func TestReinstallWhenTheTagMoves(t *testing.T) {
	cdn, rels, assetName, opts := fixture(t)
	if _, err := installOne(context.Background(), "acme/tool", opts); err != nil {
		t.Fatalf("first install: %v", err)
	}

	rels["acme/tool"] = fakeRelease(t, cdn, "v1.3.0", assetName, tarball(t, "tool", []byte("#!/bin/sh\necho tool v1.3.0\n")))
	report, err := installOne(context.Background(), "acme/tool", opts)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if report.Status != statusInstalled || report.Tag != "v1.3.0" {
		t.Fatalf("report = %s @ %s, want installed @ v1.3.0", report.Status, report.Tag)
	}
	data, err := os.ReadFile(report.Installed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "v1.3.0") {
		t.Errorf("installed bytes = %q, want the new release", data)
	}
}

func TestAlreadyInstalledExplainsItsDecision(t *testing.T) {
	dest, _ := isolateState(t)
	path := filepath.Join(dest, "tool")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\necho tool\n")
	if err := os.WriteFile(path, body, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	store, ok := stateStore()
	if !ok {
		t.Fatal("state store should resolve under a temp HOME")
	}
	record := state.Record{Repo: "acme/tool", Tag: "v1.2.3", Path: path, Binary: "tool", SHA256: digest, InstalledAt: time.Now().UTC()}
	if err := store.Put(state.Key("acme/tool", dest), record); err != nil {
		t.Fatal(err)
	}
	release := &ghrelease.Release{Tag: "v1.2.3"}

	if _, skip, why := alreadyInstalled(dest, "acme/tool", release); !skip || why != "" {
		t.Errorf("matching record: skip=%v why=%q, want skip with no complaint", skip, why)
	}
	if _, skip, why := alreadyInstalled(dest, "other/tool", release); skip || why != "" {
		t.Errorf("other repository: skip=%v why=%q, want no skip — it was never recorded", skip, why)
	}
	if _, skip, why := alreadyInstalled(dest, "acme/tool", &ghrelease.Release{Tag: "v1.3.0"}); skip || !strings.Contains(why, "v1.2.3") {
		t.Errorf("moved tag: skip=%v why=%q, want no skip naming the installed tag", skip, why)
	}
	if _, skip, why := alreadyInstalled(filepath.Join(dest, "otherdir"), "acme/tool", release); skip || why != "" {
		t.Errorf("other destination: skip=%v why=%q, want no skip — another directory has its own record", skip, why)
	}

	// A record that claims a path outside the destination says nothing about
	// the file this install would write.
	outside := state.Record{Repo: "acme/tool", Tag: "v1.2.3", Path: filepath.Join(t.TempDir(), "tool"), SHA256: digest}
	if err := store.Put(state.Key("acme/tool", dest), outside); err != nil {
		t.Fatal(err)
	}
	if _, skip, why := alreadyInstalled(dest, "acme/tool", release); skip || !strings.Contains(why, "outside") {
		t.Errorf("record outside dest: skip=%v why=%q, want no skip mentioning the destination", skip, why)
	}

	// A record with no path cannot be checked against anything.
	if err := store.Put(state.Key("acme/tool", dest), state.Record{Repo: "acme/tool", Tag: "v1.2.3", SHA256: digest}); err != nil {
		t.Fatal(err)
	}
	if _, skip, why := alreadyInstalled(dest, "acme/tool", release); skip || why == "" {
		t.Errorf("record without path: skip=%v why=%q, want no skip and a reason", skip, why)
	}

	if err := store.Put(state.Key("acme/tool", dest), record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("different"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, skip, why := alreadyInstalled(dest, "acme/tool", release); skip || !strings.Contains(why, "no longer matches") {
		t.Errorf("changed binary: skip=%v why=%q, want no skip mentioning the digest", skip, why)
	}
}

// TestSkipSurvivesAnArchiveMemberRenamedFromTheRepo is the regression test for
// the case found on the real machine with BurntSushi/ripgrep: the repository is
// "ripgrep" but the archive member is "rg", so the file name the installer will
// write is unknown before the archive is fetched. Keying the record on that name
// made every second run download the tool again.
func TestSkipSurvivesAnArchiveMemberRenamedFromTheRepo(t *testing.T) {
	cdn := newFakeCDN(t)
	dest, _ := isolateState(t)
	assetName := "grep_linux.tar.gz"
	rels := map[string]*ghrelease.Release{
		"acme/grep": fakeRelease(t, cdn, "v2.0.0", assetName, tarball(t, "rg", []byte("#!/bin/sh\necho rg\n"))),
	}
	stubReleases(t, rels)
	opts := testOptions(dest, `_linux\.tar\.gz$`)

	first, err := installOne(context.Background(), "acme/grep", opts)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if first.Binary != "rg" {
		t.Fatalf("binary = %q, want the archive member name rg", first.Binary)
	}

	second, err := installOne(context.Background(), "acme/grep", opts)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if second.Status != statusSkipped {
		t.Fatalf("second install status = %q, want %q — the renamed member must still be recognised", second.Status, statusSkipped)
	}
	if second.Installed != first.Installed || second.Binary != "rg" {
		t.Errorf("skipped report = %+v, want the recorded path and binary name", second)
	}
	if got := cdn.downloads(assetName); got != 1 {
		t.Errorf("asset downloaded %d times, want 1", got)
	}
}

// captureStdout runs fn with os.Stdout redirected, so the --json output can be
// parsed the way a pipeline would.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := fn()
	os.Stdout = saved
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	r.Close()
	return buf.String(), runErr
}

// TestInstallAllContinuesAfterAFailure is the point of the batch form: one
// repository that cannot be installed must not take the rest of the list down
// with it, and the exit status must still report the failure.
func TestInstallAllContinuesAfterAFailure(t *testing.T) {
	cdn := newFakeCDN(t)
	dest, _ := isolateState(t)
	rels := map[string]*ghrelease.Release{
		"acme/tool":  fakeRelease(t, cdn, "v1.2.3", "tool_linux.tar.gz", tarball(t, "tool", []byte("tool"))),
		"acme/other": fakeRelease(t, cdn, "v0.9.0", "other_linux.tar.gz", tarball(t, "other", []byte("other"))),
	}
	stubReleases(t, rels)
	opts := installOptions{dest: dest, asset: `_linux\.tar\.gz$`, json: true, attMode: attestationOff}

	specs := []string{"acme/tool", "ghost/missing", "acme/other"}
	out, err := captureStdout(t, func() error {
		return installAll(context.Background(), specs, opts)
	})
	if err == nil {
		t.Fatal("a batch with a failing item must return an error so the exit status is meaningful")
	}
	if !strings.Contains(err.Error(), "ghost/missing") {
		t.Errorf("error %q should name the repository that failed", err)
	}
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Errorf("error %q should count the failures", err)
	}

	var batch struct {
		OK      bool            `json:"ok"`
		Results []installReport `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &batch); err != nil {
		t.Fatalf("batch report is not JSON: %v\n%s", err, out)
	}
	if batch.OK {
		t.Error("ok = true although one install failed")
	}
	if len(batch.Results) != 3 {
		t.Fatalf("got %d results, want one per requested repository", len(batch.Results))
	}
	if batch.Results[0].Status != statusInstalled || batch.Results[2].Status != statusInstalled {
		t.Errorf("statuses = %q, %q, %q; want the items around the failure installed",
			batch.Results[0].Status, batch.Results[1].Status, batch.Results[2].Status)
	}
	if batch.Results[1].Status != statusFailed || batch.Results[1].Error == "" {
		t.Errorf("failed item = %+v, want status failed and a reason", batch.Results[1])
	}
	// The 0.7.0 contract holds for a failed item too: attestation is stated,
	// never left empty for the consumer to interpret.
	if batch.Results[1].Attestation == "" || batch.Results[1].AttestationMode == "" {
		t.Errorf("failed item = %+v, want an attestation state and mode", batch.Results[1])
	}
	// Every item that reported "installed" must actually be on disk; the path is
	// taken from the report because the file name is platform dependent
	// (tool.exe on Windows).
	for _, result := range batch.Results {
		if result.Status != statusInstalled {
			continue
		}
		if _, err := os.Stat(result.Installed); err != nil {
			t.Errorf("%s was not installed at %s: %v", result.Repo, result.Installed, err)
		}
	}
}

// TestInstallAllSkipsRecordedRepositories covers the second batch run: everything
// already at its tag, no downloads, ok true.
func TestInstallAllSkipsRecordedRepositories(t *testing.T) {
	cdn := newFakeCDN(t)
	dest, _ := isolateState(t)
	rels := map[string]*ghrelease.Release{
		"acme/tool":  fakeRelease(t, cdn, "v1.2.3", "tool_linux.tar.gz", tarball(t, "tool", []byte("tool"))),
		"acme/other": fakeRelease(t, cdn, "v0.9.0", "other_linux.tar.gz", tarball(t, "other", []byte("other"))),
	}
	stubReleases(t, rels)
	opts := installOptions{dest: dest, asset: `_linux\.tar\.gz$`, json: true, attMode: attestationOff}
	specs := []string{"acme/tool", "acme/other"}

	if _, err := captureStdout(t, func() error { return installAll(context.Background(), specs, opts) }); err != nil {
		t.Fatalf("first batch: %v", err)
	}
	before := cdn.downloads("tool_linux.tar.gz") + cdn.downloads("other_linux.tar.gz")

	out, err := captureStdout(t, func() error { return installAll(context.Background(), specs, opts) })
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	var batch batchReport
	if err := json.Unmarshal([]byte(out), &batch); err != nil {
		t.Fatalf("batch report is not JSON: %v\n%s", err, out)
	}
	if !batch.OK {
		t.Error("ok = false although every item was already installed")
	}
	for _, result := range batch.Results {
		if result.Status != statusSkipped {
			t.Errorf("%s status = %q, want %q", result.Repo, result.Status, statusSkipped)
		}
	}
	if after := cdn.downloads("tool_linux.tar.gz") + cdn.downloads("other_linux.tar.gz"); after != before {
		t.Errorf("downloads went from %d to %d: a second run must fetch nothing", before, after)
	}
}

func TestInstallRefusesSeveralSpecsWithoutAll(t *testing.T) {
	_, _ = isolateState(t)
	err := run([]string{"install", "acme/tool", "acme/other"})
	if err == nil {
		t.Fatal("two specs without --all must be rejected")
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error %q should point at --all", err)
	}
}

// TestStatusIsAlwaysInJSON pins the machine contract of the new field, the same
// way attestation was pinned in 0.7.0: a consumer must never have to infer the
// outcome from absent fields.
func TestStatusIsAlwaysInJSON(t *testing.T) {
	for _, report := range []installReport{
		{Repo: "acme/tool", Status: statusInstalled},
		{Repo: "acme/tool", Status: statusSkipped},
		{Repo: "acme/tool", Status: statusPlanned},
		{Repo: "ghost/missing", Status: statusFailed, Error: "no published release"},
	} {
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatalf("%s: %v", report.Status, err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("%s: %v", report.Status, err)
		}
		if decoded["status"] != report.Status {
			t.Errorf("status = %v, want %q", decoded["status"], report.Status)
		}
		if report.Status == statusFailed && decoded["error"] == nil {
			t.Errorf("a failed item must carry its reason, got %s", data)
		}
	}

	batch, err := json.Marshal(batchReport{OK: false, Results: []installReport{{Repo: "x/y", Status: statusFailed, Error: "boom"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(batch), `"ok":false`) {
		t.Errorf("batch report %s should state ok:false explicitly", batch)
	}
}
