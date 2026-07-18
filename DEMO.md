# JSI demo (spike/demo branch)

**Throwaway demo build.** May ignore design niceties (no policy flags, no worker
mode, no QR, no TURN, no progress bars). What it *does* prove is the real thing:
two processes exchange SDP over copy-paste blobs (QP/1) and move a file over a
**DTLS-encrypted WebRTC data channel** with SHA-256 integrity — **zero
infrastructure contact** (host candidates, no STUN even).

Under the hood it's 100% production code: `internal/peer`, `internal/signal.Paste`,
`internal/transfer`. Only `cmd/jsi-demo` is demo glue.

## Automated (one command)

```sh
nix develop
scripts/demo.sh
```

Builds `bin/jsi-demo`, wires a sender and receiver together with named pipes,
transfers 8 MiB, prints both logs, and verifies the SHA-256 match
(`DEMO OK: … sha256 match`).

## Manual (the fun version — two terminals)

```sh
nix develop
go build -o bin/jsi-demo ./cmd/jsi-demo

# terminal A:
./bin/jsi-demo send /path/to/big.file
#   → prints a long jsi1:... blob (the SDP offer). Copy it.

# terminal B:
./bin/jsi-demo receive /tmp/jsi-out
#   → paste the offer blob, Enter
#   → prints an answer blob. Copy it.

# back in A: paste the answer blob, Enter
#   → channel opens, file transfers, both sides print sha256.
```

Works across two machines on the same LAN too (host candidates only, so no
internet/NAT traversal in this build).

## What this does NOT show

- CF Worker signaling (SP/1) — done on master (M1) and e2e-tested (M2.8), just
  not wired into this binary.
- QR codes, TURN fallback, D15 policy presets, CLI UX (all M3/M4).
- Anything browser/PWA (S4).

## Resuming real work

`master` is the source of truth: landed through **M2.8** (worker SP/1 + full Go
core + e2e). Next: **M3 CLI** (agent was mid-flight at interruption, task
`ses_0891e6d30ffe2iVBdMFMADSSc0`; only deps landed — restart it), then M3.5/M3.7
e2e scripts, then M4. This branch is disposable per spike policy.
