module github.com/xiemocun/ghfetch

go 1.23

// The TLS trust store is compiled into the binary: Go 1.27.3 is the first
// toolchain that distrusts the revoked eMudhra intermediate CAs (GO-2026-6809).
// Building ghfetch with anything older produces a verifier that talks to a
// network which still accepts those chains. Pinning the toolchain means
// `go install ...@latest` downloads 1.27.3 instead of quietly using an old one.
// Guarded by TestToolchainPinsRevokedCAFix in toolchain_test.go.
toolchain go1.27.3

require (
	github.com/ulikunitz/xz v0.5.17

	// Test-only: the issue forms are validated as the YAML GitHub will parse.
	gopkg.in/yaml.v3 v3.0.1
)
