# Connectivity is not throughput

An installer that asks "did the server answer?" is measuring the wrong thing.
On a throttled link the answer is *yes* — with an HTTP 200, in under two
seconds — and then the bytes trickle in at 1.5 KB/s.

This is the measurement that shaped `ghfetch`'s source-selection rule: **a
source that answers but delivers below a floor is treated as a failure**, not as
a slow success, and the chain moves on to the next mirror instead of holding the
install hostage.

## The measurement

Same client (`curl 7.61.1`), same asset, same host, back to back:

```
asset   ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz
size    2265718 bytes
sha256  33e15bcf1624b25cdd2a55813a47a2f95dbe126268203e76aa6a585d1e7b149c  (published)
host    Alibaba Cloud ECS, 1 vCPU, UTC+8, no proxy configured
```

| # | Source | HTTP | time to first byte | delivered in 12 s | average rate |
|---|---|---|---|---|---|
| 1 | `github.com` direct | 200 after 302 | 1.88 s | 184 186 B | 15 KB/s |
| 2 | `github.com` direct | 200 after 302 | 10.61 s | 18 232 B | 1.5 KB/s |
| 3 | `ghfast.top` | 200 | 2.01 s | all 2 265 718 B in 5.40 s | 420 KB/s |
| 4 | `ghfast.top` | 200 | 1.33 s | all 2 265 718 B in 7.88 s | 287 KB/s |

Run 2 is the interesting one. The connection opened, the redirect was followed,
the status was 200 — and in twelve seconds it produced 18 KB. Any check that
looks at the status code, or at "can I reach the host", calls that source
healthy. A person watching the progress bar calls it broken.

Both mirror downloads matched the published digest exactly
(`sha256sum m1.part m2.part` → `33e15bcf…` for both), so the speed did not come
at the cost of integrity. That is what makes a mirror acceptable at all: the
digest is checked either way, so a mirror can only make the download faster or
useless, never wrong.

## Reproduce it

```console
$ curl -sSL --max-time 12 -o direct.part \
    -w 'bytes=%{size_download} time=%{time_total} speed=%{speed_download}\n' \
    https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz

$ curl -sSL --max-time 60 -o mirror.part \
    -w 'bytes=%{size_download} time=%{time_total} speed=%{speed_download}\n' \
    https://ghfast.top/https://github.com/BurntSushi/ripgrep/releases/download/15.2.0/ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz

$ sha256sum direct.part mirror.part
```

The `--max-time 12` is deliberate: it is long enough for a healthy source to
finish a 2 MiB asset and short enough that a dead one does not cost the
operator a minute of waiting before the fallback happens.

## The rule in ghfetch

`internal/source` enforces two independent limits while the body is read:

- **Rate floor.** 256 KiB must arrive inside an 8-second window, i.e. an average
  of 32 KiB/s. The check is on the average over the whole transfer, not on a
  single checkpoint, so a source that bursts 300 KB and then crawls still loses.
- **Stall.** Twelve seconds with no bytes at all abandons the source. Connections
  in mainland China often open and then sit silent; no overall timeout catches
  that soon enough to be useful.

A source that trips either limit returns `ErrUnusable`, which is **not retried** —
the failure is not transient, so retrying it just spends the operator's time.
The chain falls through to the next prefix immediately.

The effect on the same asset, with the same network conditions as the table
above:

```console
$ ghfetch install BurntSushi/ripgrep --dest demo2 --force
BurntSushi/ripgrep 15.2.0 -> ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz (2.2 MiB)
downloaded 2.2 MiB from ghfast.top
checksum verified (sha256, from ripgrep-15.2.0-x86_64-unknown-linux-musl.tar.gz.sha256)
installed demo2/rg (5.2 MiB)

real    0m17.1s
```

Seventeen seconds, of which roughly eight were spent proving that the direct
source was unusable. The alternative — trying `github.com` "because it is
first in the list" and waiting 20 minutes — is the behaviour this rule exists to
prevent. Note that the winner is printed: the operator learns which mirror
served them instead of having to guess.

## What this does not say

- It is one host, one asset, and four samples minutes apart. It is not a
  benchmark of either source's general quality; a mirror that is fast from one
  network can be slow from another, which is exactly why the decision is made
  per install, per byte, at runtime.
- A rate floor costs a healthy-but-slow source its turn. That is the intended
  trade: the next candidate in the chain is usually better, and the cost of
  being wrong is eight seconds rather than an unbounded wait.
- Speed says nothing about authenticity. The digest and the attestation are
  verified independently of which source delivered the bytes; a fast mirror is
  only ever a transport detail.