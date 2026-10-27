package main

import (
	govers "go/version"
	"os"
	"strings"
	"testing"
)

// minToolchain is the first Go release whose crypto/x509 denylist refuses the
// revoked eMudhra intermediate CAs (GO-2026-6809, golang/go#82345). The CA
// store is compiled into the binary, so building ghfetch with an older
// toolchain yields an installer whose checksum and attestation checks travel
// over a TLS chain an attacker can terminate.
const minToolchain = "go1.27.3"

// TestToolchainPinsRevokedCAFix fails when go.mod stops pinning a toolchain new
// enough to carry the denylist. Without the directive, `go install ...@latest`
// honors whatever toolchain the user already has and silently builds the
// trusting variant.
func TestToolchainPinsRevokedCAFix(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	pinned := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "toolchain "); ok {
			pinned = strings.TrimSpace(rest)
			break
		}
	}

	if pinned == "" {
		t.Fatalf("go.mod has no toolchain directive: pin %s, otherwise a user running `go install github.com/xiemocun/ghfetch@latest` builds with their own toolchain, which may still trust the revoked eMudhra CAs (GO-2026-6809)", minToolchain)
	}
	if !govers.IsValid(pinned) {
		t.Fatalf("go.mod toolchain %q is not a valid Go version", pinned)
	}
	if govers.Compare(pinned, minToolchain) < 0 {
		t.Fatalf("go.mod pins toolchain %s, but %s or newer is required: older toolchains still trust the revoked eMudhra CAs (GO-2026-6809), so every checksum and attestation check would run over a chain an attacker can terminate", pinned, minToolchain)
	}
	if govers.Compare(minToolchain, "go1.21") < 0 {
		t.Fatalf("the toolchain directive is ignored before go1.21; minToolchain floor no longer needs %s", minToolchain)
	}
}
