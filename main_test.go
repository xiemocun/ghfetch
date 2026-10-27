package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const testDigest = "b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f"

// fakeGH puts a stub `gh` first on PATH that prints the given streams and exits
// with the given status, so the provenance path is tested without the real CLI,
// a network, or a repository that attests.
func fakeGH(t *testing.T, stdout, stderr string, exit int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub gh is a POSIX shell script")
	}
	dir := t.TempDir()
	outPath := filepath.Join(dir, "stdout.json")
	errPath := filepath.Join(dir, "stderr.txt")
	if err := os.WriteFile(outPath, []byte(stdout), 0o644); err != nil {
		t.Fatalf("write stdout fixture: %v", err)
	}
	if err := os.WriteFile(errPath, []byte(stderr), 0o644); err != nil {
		t.Fatalf("write stderr fixture: %v", err)
	}
	script := fmt.Sprintf("#!/bin/sh\ncat %q 2>/dev/null\ncat %q >&2 2>/dev/null\nexit %d\n", outPath, errPath, exit)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	// Keep the rest of PATH: the stub needs cat, and being first means LookPath
	// still resolves `gh` to the stub.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func attestationJSON(digest string) string {
	return attestationJSONFor("https://github.com/owner/repo", digest)
}

func attestationJSONFor(sourceRepo, digest string) string {
	return `[{"attestation":{"bundle":{}},"verificationResult":{` +
		`"signature":{"certificate":{"sourceRepositoryURI":"` + sourceRepo + `"}},` +
		`"statement":{"subject":[{"name":"release.tar.gz","digest":{"sha256":"` + digest + `"}}]}}}]`
}

func TestVerifyAttestationVerified(t *testing.T) {
	fakeGH(t, attestationJSON(testDigest), "", 0)
	status, detail, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != attestationVerified {
		t.Errorf("got %q, want %q", status, attestationVerified)
	}
	if detail != "" {
		t.Errorf("a verified result should carry no detail, got %q", detail)
	}
}

// TestVerifyAttestationOffIsSkippedNotEmpty pins the --json contract: "off" is
// reported as skipped, and skipping never shells out to gh.
func TestVerifyAttestationOffIsSkippedNotEmpty(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+"/nonexistent")
	}
	status, detail, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationOff)
	if err != nil {
		t.Fatalf("off must not error even without gh: %v", err)
	}
	if status != attestationSkipped {
		t.Errorf("got %q, want %q", status, attestationSkipped)
	}
	if detail != "" {
		t.Errorf("got detail %q, want none", detail)
	}
}

// TestVerifyAttestationIgnoresZeroExitOn404 pins the real defect this wrapper
// was written against: `gh attestation verify` exits 0 when the attestations API
// answers 404, so the exit status alone must never be read as success.
func TestVerifyAttestationIgnoresZeroExitOn404(t *testing.T) {
	fakeGH(t, "", "Error: HTTP 404: Not Found (https://api.github.com/repos/owner/repo/attestations/sha256:...)", 0)
	_, _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired)
	if err == nil {
		t.Fatal("a zero exit with no attestation report must not count as verified")
	}
}

func TestVerifyAttestationRejectsEmptyReport(t *testing.T) {
	fakeGH(t, "[]", "", 0)
	if _, _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired); err == nil {
		t.Fatal("an empty attestation list must not verify")
	}
}

func TestVerifyAttestationRejectsDigestMismatch(t *testing.T) {
	fakeGH(t, attestationJSON(strings.Repeat("0", 64)), "", 0)
	if _, _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired); err == nil {
		t.Fatal("an attestation for another digest must not verify this artifact")
	}
}

func TestVerifyAttestationRejectsNonJSON(t *testing.T) {
	fakeGH(t, "verification succeeded\n", "", 0)
	if _, _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired); err == nil {
		t.Fatal("unparseable verifier output must not verify")
	}
}

func TestVerifyAttestationFailureIsFatalWhenRequired(t *testing.T) {
	fakeGH(t, "", "no attestations found for subject\n", 1)
	_, _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no attestations found") {
		t.Errorf("error should surface the verifier output, got: %v", err)
	}
	if !strings.Contains(err.Error(), "owner/repo") {
		t.Errorf("error should name the repository, got: %v", err)
	}
}

// TestVerifyAttestationFailureIsVisibleWhenOptional is the regression test for
// the silent downgrade: the install still succeeds, but the result must say
// failed and carry the reason, not come back empty.
func TestVerifyAttestationFailureIsVisibleWhenOptional(t *testing.T) {
	fakeGH(t, "", "no attestations found for subject\n", 1)
	status, detail, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationOptional)
	if err != nil {
		t.Fatalf("optional verification must not fail the install: %v", err)
	}
	if status != attestationFailed {
		t.Errorf("got %q, want %q — an unproven artifact must not look like an unchecked one", status, attestationFailed)
	}
	if !strings.Contains(detail, "no attestations found") {
		t.Errorf("detail %q should carry the verifier's reason", detail)
	}
}

func TestVerifyAttestationWithoutGH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH handling differs on Windows")
	}
	// A directory with no gh in it, and no fallback entry that holds one either.
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+"/nonexistent")

	if _, _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationRequired); err == nil {
		t.Fatal("required provenance must fail when gh is missing")
	} else if !strings.Contains(err.Error(), "gh CLI not found") {
		t.Errorf("error should say gh is missing, got: %v", err)
	}

	// Optional and gh missing is a downgrade, so it has to read as failed.
	status, detail, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, attestationOptional)
	if err != nil {
		t.Errorf("optional provenance with no gh must not fail the install: %v", err)
	}
	if status != attestationFailed {
		t.Errorf("got %q, want %q: a check that could not run is not a check that passed", status, attestationFailed)
	}
	if !strings.Contains(detail, "gh CLI not found") {
		t.Errorf("detail %q should say gh is missing", detail)
	}
}

func TestResolveAttestationMode(t *testing.T) {
	cases := []struct {
		name            string
		policy          string
		verify, require bool
		want            attestationMode
		wantErr         bool
	}{
		{"no flags", "", false, false, attestationOff, false},
		{"legacy verify", "", true, false, attestationOptional, false},
		{"legacy require", "", false, true, attestationRequired, false},
		{"both legacy flags", "", true, true, attestationRequired, false},
		{"policy off", "off", false, false, attestationOff, false},
		{"policy optional", "optional", false, false, attestationOptional, false},
		{"policy required", "required", false, false, attestationRequired, false},
		{"policy cannot weaken legacy verify", "off", true, false, attestationOptional, false},
		{"policy cannot weaken legacy require", "optional", false, true, attestationRequired, false},
		{"stricter policy wins", "required", true, false, attestationRequired, false},
		{"bogus policy", "maybe", false, false, attestationOff, true},
		{"bogus policy beats legacy flags", "maybe", false, true, attestationOff, true},
	}
	for _, c := range cases {
		got, err := resolveAttestationMode(c.policy, c.verify, c.require)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected an error", c.name)
			} else if !strings.Contains(err.Error(), "off, optional or required") {
				t.Errorf("%s: error %q should list the accepted policies", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: resolveAttestationMode = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestAttestationModeString(t *testing.T) {
	cases := map[attestationMode]string{
		attestationOff:      "off",
		attestationOptional: "optional",
		attestationRequired: "required",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("attestationMode(%d).String() = %q, want %q", int(mode), got, want)
		}
	}
	// Round trip: the JSON policy name must be accepted by the parser.
	for mode, name := range cases {
		if got, err := parseAttestationMode(name); err != nil || got != mode {
			t.Errorf("parseAttestationMode(%q) = %v, %v; want %v", name, got, err, mode)
		}
	}
}

// TestAttestationJSONHasNoEmptyState pins the --json contract that 0.6.0 broke:
// attestation is never omitted and never empty, so a pipeline can branch on it.
func TestAttestationJSONHasNoEmptyState(t *testing.T) {
	cases := []struct {
		name string
		in   installReport
		want string
	}{
		{"off", installReport{Repo: "owner/repo", Attestation: attestationSkipped, AttestationMode: attestationOff.String()}, "skipped"},
		{"optional failed", installReport{Repo: "owner/repo", Attestation: attestationFailed, AttestationMode: attestationOptional.String(), AttestationDetail: "gh CLI not found"}, "failed"},
		{"required verified", installReport{Repo: "owner/repo", Attestation: attestationVerified, AttestationMode: attestationRequired.String()}, "verified"},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		state, ok := got["attestation"]
		if !ok {
			t.Fatalf("%s: attestation key missing from %s", c.name, data)
		}
		if state != c.want {
			t.Errorf("%s: attestation = %v, want %q", c.name, state, c.want)
		}
		if _, ok := got["attestation_mode"]; !ok {
			t.Errorf("%s: attestation_mode missing from %s", c.name, data)
		}
		if c.want == "failed" && got["attestation_detail"] == nil {
			t.Errorf("%s: a failed check must carry its reason, got %s", c.name, data)
		}
	}
}

// TestInstallRejectsUnknownAttestationPolicy keeps a typo from reaching the
// network, and keeps the error message actionable.
func TestInstallRejectsUnknownAttestationPolicy(t *testing.T) {
	if err := run([]string{"install", "owner/repo", "--attestation=maybe"}); err == nil {
		t.Fatal("an unknown policy must be rejected")
	} else if !strings.Contains(err.Error(), "off, optional or required") {
		t.Errorf("error %q should list the accepted policies", err)
	}
}

// TestAttestationProblemShortensThe404 keeps the --json detail readable: the
// raw gh stderr for a missing attestation is a full API URL.
func TestAttestationProblemShortensThe404(t *testing.T) {
	raw := "Error: HTTP 404: Not Found (https://api.github.com/repos/BurntSushi/ripgrep/attestations/sha256:" + testDigest + "?per_page=30)\n"
	got := attestationProblem(raw, errors.New("exit status 1"))
	if !strings.Contains(got, "no attestations published") || strings.Contains(got, "https://") {
		t.Errorf("attestationProblem = %q, want the short 404 wording", got)
	}

	long := strings.Repeat("x", 500)
	if got := attestationProblem(long, errors.New("exit status 1")); len(got) > 210 {
		t.Errorf("a long verifier line must be truncated, got %d bytes", len(got))
	}

	if got := attestationProblem("", errors.New("signal: killed")); got != "signal: killed" {
		t.Errorf("attestationProblem with no stderr = %q, want the error text", got)
	}
}

func TestAttestationCovers(t *testing.T) {
	cases := []struct {
		name   string
		report string
		digest string
		want   bool
	}{
		{"match", attestationJSON(testDigest), testDigest, true},
		{"case insensitive digest", attestationJSON(strings.ToUpper(testDigest)), testDigest, true},
		{"other digest", attestationJSON(strings.Repeat("a", 64)), testDigest, false},
		{"other repository", attestationJSONFor("https://github.com/attacker/ghfetch", testDigest), testDigest, false},
		{"repo uri without scheme", attestationJSONFor("github.com/Owner/Repo", testDigest), testDigest, true},
		{"empty", "[]", testDigest, false},
		{"blank", "  ", testDigest, false},
		{"garbage", "<html>", testDigest, false},
		{"no certificate", `[{"verificationResult":{"statement":{"subject":[{"digest":{"sha256":"` + testDigest + `"}}]}}}]`, testDigest, false},
		{"no subjects", `[{"verificationResult":{"signature":{"certificate":{"sourceRepositoryURI":"https://github.com/owner/repo"}},"statement":{"subject":[]}}}]`, testDigest, false},
	}
	for _, c := range cases {
		if got := attestationCovers([]byte(c.report), c.digest, "owner/repo"); got != c.want {
			t.Errorf("%s: attestationCovers = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNormalizeRepoURI(t *testing.T) {
	cases := map[string]string{
		"https://github.com/Owner/Repo":     "owner/repo",
		"https://github.com/owner/repo.git": "owner/repo",
		"http://github.com/owner/repo/":     "owner/repo",
		"github.com/owner/repo":             "owner/repo",
		"  https://github.com/owner/repo  ": "owner/repo",
		"":                                  "",
	}
	for in, want := range cases {
		if got := normalizeRepoURI(in); got != want {
			t.Errorf("normalizeRepoURI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"":                       "no output",
		"   ":                    "no output",
		"one line":               "one line",
		"first\nsecond\n":        "first",
		"\n\n  padded first  \n": "padded first",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
