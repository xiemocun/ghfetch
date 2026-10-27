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

Why the source selector judges throughput instead of reachability — with the
measurements that produced the rule: [docs/why-speed-not-connectivity.md](docs/why-speed-not-connectivity.md).

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
ghfetch install [--all] <owner/repo>[@tag]... [flags]
ghfetch list    <owner/repo>[@tag]
ghfetch completion <bash|zsh|fish>
ghfetch version
```

| Flag | Meaning |
|---|---|
| `--all` | install every repository named on the command line |
| `--force` | reinstall even when the recorded install already matches |
| `--to <file>` | use this exact release asset |
| `--asset <regexp>` | select the asset with a regular expression |
| `--bin <name>` | executable inside the archive (default: `repo` name) |
| `--dest <dir>` | install directory (default: `/usr/local/bin`, else `~/.local/bin`) |
| `--source <list>` | comma-separated mirror prefixes, `direct` to skip mirrors |
| `--require-checksum` | fail when the release publishes no checksum file |
| `--attestation <policy>` | SLSA build provenance: `off` (default), `optional`, `required` |
| `--verify-attestation` | alias for `--attestation=optional` |
| `--require-attestation` | alias for `--attestation=required` |
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
  "status": "installed",
  "source": "ghfast.top",
  "sha256": "b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f",
  "verified": "sha256",
  "attestation": "skipped",
  "attestation_mode": "off"
}
```

`attestation` is always present, always one of `verified`, `skipped` or
`failed`, and `attestation_detail` explains a `failed` — so a pipeline never has
to guess whether provenance was not requested, attempted and unproven, or proved:

The same holds for `status`: `installed`, `skipped` (already on disk at that
tag), `planned` (`--dry-run`) or `failed`, with `error` carrying the reason.
A consumer never has to infer the outcome from which fields happen to be empty.

## Batch install, and running it twice

A tool list that has to be installed on a fresh machine is one command, and the
second run costs nothing:

```console
$ ghfetch install --all jqlang/jq BurntSushi/ripgrep --dest /usr/local/bin
[1/2] jqlang/jq
jqlang/jq jq-1.8.2 -> jq-linux-amd64 (2.2 MiB)
downloaded 2.2 MiB from ghfast.top
checksum verified (sha256, from sha256sum.txt)
installed /usr/local/bin/jq (2.2 MiB)
[2/2] BurntSushi/ripgrep
BurntSushi/ripgrep 15.2.0 -> ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz (2.2 MiB)
downloaded 2.2 MiB from ghfast.top
checksum verified (sha256, from ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz.sha256)
note: "ripgrep" is not in ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz, using the largest executable instead
installed /usr/local/bin/rg (5.2 MiB)
2 installed, 0 skipped, 0 failed (of 2)

$ ghfetch install --all jqlang/jq BurntSushi/ripgrep --dest /usr/local/bin
[1/2] jqlang/jq
jqlang/jq jq-1.8.2 is already installed at /usr/local/bin/jq (--force reinstalls)
[2/2] BurntSushi/ripgrep
BurntSushi/ripgrep 15.2.0 is already installed at /usr/local/bin/rg (--force reinstalls)
0 installed, 2 skipped, 0 failed (of 2)
```

The second run is a no-op: nothing is downloaded, nothing is rewritten.

- **One failure does not stop the list.** Every repository is attempted, the
  failures are named at the end, and the exit status is `1`, so a provisioning
  script fails loudly without leaving the rest of the list uninstalled.
- **The record is what decides.** `~/.local/state/ghfetch/installed.json`
  (override with `GHFETCH_STATE_FILE`, or `$XDG_STATE_HOME`) maps
  `repo@destination` to the tag, the file it wrote and the sha256 of *that file*.
  A repeat install is skipped only when the tag matches, the recorded path is
  still inside the destination, and the bytes on disk still hash to the recorded
  digest — a binary someone replaced by hand is installed again, not trusted.
- **The version is never guessed** by running `<binary> --version`: projects
  disagree about the flag, and a guess that parses wrong is worse than a
  download.
- **A pinned tag saves the download, not the API call.** Even with
  `jq@jq-1.8.2` the release metadata is fetched to pick the asset; the skip
  happens before the asset download, which is the part that is slow.

Batch output for scripts:

```console
$ ghfetch install --all jqlang/jq ghost/nope --json | jq -c '.ok, (.results[] | {repo, status})'
false
{"repo":"jqlang/jq","status":"skipped"}
{"repo":"ghost/nope","status":"failed"}
```

`--all --json` prints `{"ok": bool, "results": [...]}` with one entry per
requested repository, in the order they were named, so the failures can be
selected without parsing stderr.

## Shell completion

bash, zsh and fish scripts are generated by the binary itself, so they always
match the flags this version implements:

```console
# bash
$ ghfetch completion bash > /etc/bash_completion.d/ghfetch   # or ~/.local/share/bash-completion/completions/ghfetch
# zsh (anywhere in $fpath)
$ ghfetch completion zsh > "${fpath[1]}/_ghfetch"
# fish
$ ghfetch completion fish > ~/.config/fish/completions/ghfetch.fish
```

Completion covers the subcommands, every `install` and `list` flag, directory
completion after `--dest`, and file completion after `--to` and `--source`. It
does not call the GitHub API, so pressing tab never hangs behind a slow network.
Repository names are not completed from the network for the same reason.

## Supply chain: two independent checks

A checksum answers *did these bytes arrive intact and as published*. It does not
answer *did the publisher's build actually produce this file* — a compromised
release upload passes a checksum happily.

`ghfetch` therefore supports both, and they are independent:

```console
# 1. digest (always on when the release publishes one)
$ ghfetch install xiemocun/ghfetch --require-checksum

# 2. SLSA build provenance, verified by `gh attestation verify`
$ ghfetch install xiemocun/ghfetch --attestation=required   # or --require-attestation

# prove it in CI, but still install when the project does not attest
$ ghfetch install BurntSushi/ripgrep --attestation=optional --json | jq -r .attestation
failed
```

`--attestation` is the whole policy in one flag: `off` (default) never runs the
verifier, `optional` runs it and reports the result without failing the install,
`required` fails the install when provenance does not verify. `--verify-attestation`
and `--require-attestation` remain as aliases for `optional` and `required`, and
when both forms are given the stricter one wins — a script cannot lose a check it
asked for because of argument order.

Provenance verification is delegated to `gh attestation verify` rather than
reimplemented: that command already ships Sigstore's trust roots, the Rekor
lookup and the bundle format, and it is the verifier GitHub documents. Vendoring
a TUF root store into a release installer would add more trust surface than the
installer is worth. The trade-off is a dependency on `gh` (2.49+) for this one
flag; without it, `--attestation=optional` warns and reports `failed`, while
`--attestation=required` stops the install.

Its exit status is not trusted on its own. `gh attestation verify` has exited 0
while the attestations API answered 404 for a digest — printing the HTTP error to
stderr and verifying nothing — which is what v0.4.0 hit, and later releases exit
1 for the same request (2.103.3 does). Either way the status is not the signal: a
wrapper that reads only the exit code reports "verified" for an artifact nobody
ever attested. `ghfetch` therefore parses the `--format json` report and requires
an attested subject whose `sha256` equals the digest of the bytes it downloaded.

### TLS trust is part of the binary

The checksum and the attestation are downloaded over TLS, so the CA store matters
as much as the digest does. Go compiles its own root store and its own denylist,
which means two binaries on the same host can disagree: after the eMudhra
intermediate CAs were revoked in October 2026, a system `curl` still completed a
verified handshake (`Verify return code: 0 (ok)`) through a chain ending in
`emSign ECC Root CA - G3`, while Go 1.27.3 refused the same chain with
`x509: no valid chains built` (GO-2026-6809, golang/go#82345).

`go.mod` therefore pins `toolchain go1.27.3`, so `go install
github.com/xiemocun/ghfetch@latest` downloads that toolchain instead of building
with whatever older one is on the user's `PATH`; `TestToolchainPinsRevokedCAFix`
fails if the pin is removed or lowered, and the release workflow reads `go
version -m` on the built binary and refuses to publish anything older.

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
- **No self-update.** Re-run `install`; a recorded tag is a no-op and a newer
  tag replaces the binary through the same atomic rename.
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