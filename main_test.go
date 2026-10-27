package main

import (
	"context"
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
	got, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "verified" {
		t.Errorf("got %q, want \"verified\"", got)
	}
}

// TestVerifyAttestationIgnoresZeroExitOn404 pins the real defect this wrapper
// was written against: `gh attestation verify` exits 0 when the attestations API
// answers 404, so the exit status alone must never be read as success.
func TestVerifyAttestationIgnoresZeroExitOn404(t *testing.T) {
	fakeGH(t, "", "Error: HTTP 404: Not Found (https://api.github.com/repos/owner/repo/attestations/sha256:...)", 0)
	_, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true)
	if err == nil {
		t.Fatal("a zero exit with no attestation report must not count as verified")
	}
}

func TestVerifyAttestationRejectsEmptyReport(t *testing.T) {
	fakeGH(t, "[]", "", 0)
	if _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true); err == nil {
		t.Fatal("an empty attestation list must not verify")
	}
}

func TestVerifyAttestationRejectsDigestMismatch(t *testing.T) {
	fakeGH(t, attestationJSON(strings.Repeat("0", 64)), "", 0)
	if _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true); err == nil {
		t.Fatal("an attestation for another digest must not verify this artifact")
	}
}

func TestVerifyAttestationRejectsNonJSON(t *testing.T) {
	fakeGH(t, "verification succeeded\n", "", 0)
	if _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true); err == nil {
		t.Fatal("unparseable verifier output must not verify")
	}
}

func TestVerifyAttestationFailureIsFatalWhenRequired(t *testing.T) {
	fakeGH(t, "", "no attestations found for subject\n", 1)
	_, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true)
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

func TestVerifyAttestationFailureIsWarningWhenOptional(t *testing.T) {
	fakeGH(t, "", "no attestations found for subject\n", 1)
	got, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, false)
	if err != nil {
		t.Fatalf("optional verification must not fail the install: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want an empty result", got)
	}
}

func TestVerifyAttestationWithoutGH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH handling differs on Windows")
	}
	// A directory with no gh in it, and no fallback entry that holds one either.
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+"/nonexistent")

	if _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, true); err == nil {
		t.Fatal("required provenance must fail when gh is missing")
	} else if !strings.Contains(err.Error(), "gh CLI not found") {
		t.Errorf("error should say gh is missing, got: %v", err)
	}

	if got, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), testDigest, false); err != nil || got != "" {
		t.Errorf("optional provenance with no gh = %q, %v; want \"\", nil", got, err)
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
