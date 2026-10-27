# Security policy

`ghfetch` downloads a binary from a third party and puts it on your `PATH`. That
is a trust decision, so this document says exactly where the trust sits — and
where it does not.

## Reporting a vulnerability

Use GitHub's private advisory form:
<https://github.com/xiemocun/ghfetch/security/advisories/new>

Please do not open a public issue for anything that could be exploited before a
fix ships. A useful report contains the command, the repository and tag you were
installing, the observed behaviour, and what you expected instead.

This is a one-person project with no funding: expect a first reply within about a
week, not within an hour. If a report is valid, the fix ships in a patch release
and the advisory is published with credit unless you ask otherwise.

## Supported versions

The latest minor release gets fixes. Older lines are not patched — installing the
new version is the upgrade path, and `ghfetch install xiemocun/ghfetch` is the
supported way to do it.

## What ghfetch does and does not protect against

**Verified by ghfetch itself**

- **Corrupt or truncated downloads.** When the release publishes a checksum file,
  the downloaded bytes are hashed and compared before anything is renamed into
  place. `--require-checksum` makes a missing manifest a hard failure instead of
  a warning.
- **A mirror substituting a different file.** Only partially, and this is the part
  worth reading carefully. The asset *and* the checksum file are fetched through
  the same source chain (`github.com`, then `ghfast.top`, then `gh-proxy.com` by
  default). A mirror that serves both can make them agree, so the digest check
  catches a broken transfer, not a hostile mirror. If the integrity of the bytes
  matters — regulated environments, installing into production — pin the source
  with `--source direct`, or use the attestation path below.
- **TLS trust.** The trust store is compiled into the binary, not read from the
  host, and the toolchain is pinned to a Go release that distrusts the revoked
  eMudhra intermediate CAs (GO-2026-6809). Running `ghfetch` on an unpatched host
  does not make it trust those chains.
- **Archive path traversal.** Entries whose name is absolute or contains `..` are
  skipped, and only the selected executable is written, into a temporary file in
  the destination directory.
- **Partial installs.** Bytes go to a temporary file, are chmodded and fsynced,
  then renamed into place, so a crashed or killed install never leaves a
  half-written binary on `PATH`.

**Verified only when you ask for it**

- **Provenance.** With `--attestation=required`, ghfetch runs `gh attestation
  verify` and requires the attestation to name the requested repository (the
  certificate's source repository URI is compared, not just the digest). This is
  the only check in ghfetch that a compromised mirror cannot satisfy, because the
  attestation is a signature over the digest, bound to a repository.
  It needs `gh` installed and authenticated, and it is `off` by default.
  Verification parses the JSON report; the exit status alone is never trusted,
  because older `gh` releases returned `0` for a missing attestation.

**Out of scope**

- **The downloaded binary itself.** ghfetch never executes what it installs, and
  cannot tell a good upstream release from a malicious one. It verifies transport
  and, optionally, provenance.
- **Your local state file.** `~/.local/state/ghfetch/installed.json` is trusted
  input; whoever can write it can already write your `PATH`.
- **Decompression bombs.** Downloads are capped at 512 MiB, but there is no cap
  on the expansion ratio of an archive. Reported as a known gap, not a promise.
- **Upstream trust.** A release that publishes a checksum over its own malicious
  binary passes every check here. Use provenance and read what you install.

## This repository's own releases

Releases are built by GitHub Actions from the tagged commit, with SLSA build
provenance, and immutable releases are enabled so a published tag cannot be
re-pointed at different assets. Each release binary records the toolchain that
built it (`go version -m`), and the release job refuses to publish a binary whose
recorded toolchain is older than the one that carries the CA fix.