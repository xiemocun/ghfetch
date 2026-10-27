# ghfetch

[![ci](https://github.com/xiemocun/ghfetch/actions/workflows/ci.yml/badge.svg)](https://github.com/xiemocun/ghfetch/actions/workflows/ci.yml)
[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Install binaries published on GitHub Releases — even when `github.com` is slow,
throttled, or unreachable — and never install a byte that fails its published
sha256 digest.

```console
$ ghfetch install cli/cli
cli/cli v2.103.3 -> gh_2.103.3_linux_amd64.tar.gz (14.3 MiB)
downloaded 14.3 MiB from ghfast.top
checksum verified (sha256)
installed /usr/local/bin/gh (14.3 MiB)
```

## Why

On a small cloud server in mainland China a GitHub release download can crawl at
10 KB/s while the GitHub API itself answers instantly. Every developer there has
written the same ad-hoc shell loop: try github.com, try a proxy, retry, unpack
by hand, forget to check the checksum. `ghfetch` is that loop, made boring:

- **Mirror chain, in order.** Direct first, then known-good CDNs; the first one
  that answers wins and the winner is printed.
- **Slow is a failure.** A source that answers but delivers under ~32 KiB/s is
  dropped after 8 seconds, and one that goes silent for 12 seconds is dropped
  too. Both fall through to the next mirror instead of holding the install
  hostage — which is the exact failure a plain "try github, then a proxy" loop
  gets wrong.
- **Digest checked before anything lands on disk.** If the release publishes a
  checksum file, a mismatch aborts the install. `--require-checksum` turns a
  missing manifest into a hard failure instead of a warning.
- **Platform matching done once.** `x86_64`, `amd64`, `x64`, `macOS`, `osx`,
  `aarch64` — the aliases are resolved so you never guess an asset name again.
- **Atomic install.** Bytes go to a temp file in the destination directory, are
  chmodded and fsynced, then renamed into place. A crashed install never leaves
  a half-written binary on your `PATH`.

No runtime dependencies. Single static binary. Linux, macOS, Windows; amd64 and
arm64.

## Install

From a release (yes, using itself):

```console
$ ghfetch install xiemocun/ghfetch
```

Or with the Go toolchain:

```console
$ go install github.com/xiemocun/ghfetch@latest
```

## Usage

```console
ghfetch install <owner/repo>[@tag] [flags]
ghfetch list    <owner/repo>[@tag]
ghfetch version
```

| Flag | Meaning |
|---|---|
| `--to <file>` | use this exact release asset |
| `--asset <regexp>` | select the asset with a regular expression |
| `--bin <name>` | executable inside the archive (default: `repo` name) |
| `--dest <dir>` | install directory (default: `/usr/local/bin`, else `~/.local/bin`) |
| `--source <list>` | comma-separated mirror prefixes, `direct` to skip mirrors |
| `--require-checksum` | fail when the release publishes no checksum file |
| `--verify-attestation` | also verify SLSA build provenance with the `gh` CLI |
| `--require-attestation` | fail unless build provenance verifies |
| `--dry-run` | resolve and print the plan, install nothing |
| `--json` | print a machine-readable report on stdout |

### Examples

Inspect what a release offers and which asset would be picked:

```console
$ ghfetch list cli/cli
cli/cli v2.103.3 (host: linux/amd64)
* gh_2.103.3_linux_amd64.tar.gz                     14.3 MiB
  gh_2.103.3_linux_arm64.tar.gz                     13.1 MiB
  gh_2.103.3_checksums.txt                           1.5 KiB
```

Pin a version, install into `~/.local/bin`, and demand a checksum:

```console
$ ghfetch install jqlang/jq@jq-1.7.1 --bin jq --dest ~/.local/bin --require-checksum
```

Use a company mirror first:

```console
$ ghfetch install cli/cli --source https://mirror.corp/github/,direct
```

See the plan without touching disk:

```console
$ ghfetch install cli/cli --dry-run
```

Machine-readable output for scripts and CI:

```console
$ ghfetch install jqlang/jq --json
{
  "repo": "jqlang/jq",
  "tag": "jq-1.8.2",
  "asset": "jq-linux-amd64",
  "size": 2267912,
  "binary": "jq",
  "dest": "/tmp/j1",
  "installed": "/tmp/j1/jq",
  "source": "ghfast.top",
  "sha256": "b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f",
  "verified": "sha256"
}
```

## Supply chain: two independent checks

A checksum answers *did these bytes arrive intact and as published*. It does not
answer *did the publisher's build actually produce this file* — a compromised
release upload passes a checksum happily.

`ghfetch` therefore supports both, and they are independent:

```console
# 1. digest (always on when the release publishes one)
$ ghfetch install xiemocun/ghfetch --require-checksum

# 2. SLSA build provenance, verified by `gh attestation verify`
$ ghfetch install xiemocun/ghfetch --require-attestation
```

Provenance verification is delegated to `gh attestation verify` rather than
reimplemented: that command already ships Sigstore's trust roots, the Rekor
lookup and the bundle format, and it is the verifier GitHub documents. Vendoring
a TUF root store into a release installer would add more trust surface than the
installer is worth. The trade-off is a dependency on `gh` (2.49+) for this one
flag; without it, `--verify-attestation` warns and `--require-attestation`
fails.

Its exit status is not trusted on its own. `gh attestation verify` exits 0 when
the attestations API answers 404 for a digest, printing the HTTP error to
stderr, so a wrapper that reads only the exit code reports "verified" for an
artifact nobody ever attested — which is what v0.4.0 did. `ghfetch` therefore
parses the `--format json` report and requires an attested subject whose
`sha256` equals the digest of the bytes it downloaded.

## What it supports

- Archives: `.tar.gz`, `.tgz`, `.tar.xz`, `.txz`, `.tar.bz2`, `.tbz2`, `.zip`,
  plain `.gz`, and bare binaries.
- Checksum manifests: `sha256sum` output, `sha256sum --binary` output, and BSD
  `SHA256 (file) = digest`.
- Per-asset sidecar digests: `<asset>.sha256`, `.sha256sum`, `.sha256.txt`,
  `.digest` — for projects that never publish a combined manifest.
- Set `GH_TOKEN` or `GITHUB_TOKEN` to raise the API rate limit (60 → 5000
  requests/hour).

## Naming

The installed binary is named after the repository (`cli/cli` → `gh` because
the archive member matches by prefix; `BurntSushi/ripgrep` → `rg` because the
member carries no platform tokens). When a repo uses a platform-stamped member
name, `--bin` names it explicitly:

```console
$ ghfetch install cli/cli --bin gh
```

## Deliberate limits

- **sha256 only.** That is what projects publish. A signature-verification layer
  is a different tool with a different trust model.
- **File names with spaces** in a checksum manifest are not parsed; no release
  artifact worth installing has one.
- **No self-update.** Re-run `install`; the atomic rename makes that safe.
- **Mirrors are convenience, not trust.** Every mirror is treated as untrusted
  transport, which is exactly why the digest is verified after download rather
  than trusting the transport.

## Development

```console
$ go test ./...
$ go vet ./...
$ go build .
```

CI runs the tests on Linux, macOS and Windows plus a cross-compile matrix on
every push.

## License

MIT — see [LICENSE](LICENSE).