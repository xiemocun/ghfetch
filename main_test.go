package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeGH puts a stub `gh` first on PATH so the provenance path can be tested
// without the real CLI, a network, or a repository that attests.
func fakeGH(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub gh is a POSIX shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write stub gh: %v", err)
	}
	t.Setenv("PATH", dir)
}

func TestVerifyAttestationVerified(t *testing.T) {
	fakeGH(t, "echo 'verification succeeded'\nexit 0\n")
	got, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "verified" {
		t.Errorf("got %q, want \"verified\"", got)
	}
}

func TestVerifyAttestationFailureIsFatalWhenRequired(t *testing.T) {
	fakeGH(t, "echo 'no attestations found for subject'\nexit 1\n")
	_, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), true)
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
	fakeGH(t, "echo 'no attestations found for subject'\nexit 1\n")
	got, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), false)
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
	t.Setenv("PATH", t.TempDir()) // a directory with no gh in it

	if _, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), true); err == nil {
		t.Fatal("required provenance must fail when gh is missing")
	} else if !strings.Contains(err.Error(), "gh CLI not found") {
		t.Errorf("error should say gh is missing, got: %v", err)
	}

	if got, err := verifyAttestation(context.Background(), "owner/repo", []byte("asset"), false); err != nil || got != "" {
		t.Errorf("optional provenance with no gh = %q, %v; want \"\", nil", got, err)
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
