# JSI — Just Send It

> Peer-to-peer file transfer over WebRTC. No accounts, no size limits, no servers ever touch your files.

Two people share a short token or paste a blob. A WebRTC data channel opens
directly between their devices. The file crosses it, DTLS-encrypted end-to-end.
The only infrastructure is a ~250-line Cloudflare Worker that brokers the
handshake and forgets everything in 90 seconds — or use paste mode and run with
zero infrastructure at all.

## How it works

```
 Sender                                Receiver
   │                                      │
   │  ── offer SDP (paste blob / token) ──▶│
   │  ◀── answer SDP (paste blob) ─────── │
   │                                      │
   │ ══════ WebRTC data channel ═════════ │
   │        DTLS-encrypted, P2P           │
   │        (STUN direct, or TURN relay   │
   │         as last resort)              │
```

Three signaling modes (the `--externals` policy, decision D15):

| Preset | Signaling | Transport | External contact |
|---|---|---|---|
| `none` (default) | paste blob | host + STUN | STUN only |
| `fallback` | paste → worker on failure | + TURN relay | anonymous `/v1/ice`; worker if paste fails |
| `full` | Cloudflare Worker | + TURN relay | worker session (≤90 s) |

Default is `none` — no accidental data through servers.

## Quick start

### Install

**Download a binary** from [releases](https://github.com/treytorrez/jsi/releases)
(Linux / macOS / Windows, amd64 / arm64).

**Or build from source:**

```sh
git clone https://github.com/treytorrez/jsi.git
cd jsi
nix develop          # provides Go, Node, Wrangler, lint — or install Go 1.24+ manually
go build -o jsi ./cmd/jsi
go build -o jsi-tui ./cmd/jsi-tui   # optional: interactive TUI
```

### Transfer a file (zero infrastructure — the default)

Two terminals, same machine or same LAN:

```sh
# Terminal A (sender):
jsi send photo.jpg
# → prints a jsi1:... blob. Copy it.

# Terminal B (receiver):
jsi receive -o ~/downloads
# → paste the blob, Enter
# → prints an answer blob. Copy it.

# Back in Terminal A: paste the answer blob, Enter
# → "connected: direct (host)" → file transfers → sha256 verified
```

### Transfer over the internet (worker signaling)

Uses the deployed signaling worker at `jsi-signal.treytorrez.workers.dev`:

```sh
# Terminal A (sender):
jsi send big-file.zip --externals full
# → prints a 6-character token, e.g. "token: 7KQX2A"

# Terminal B (anywhere with internet):
jsi receive 7KQX2A --externals full -o ~/downloads
# → connects, transfers, hash-verified
```

### Interactive TUI

```sh
jsi-tui    # or: go run ./cmd/jsi-tui
```

Bubble Tea interface with send/receive/settings/help screens. Navigate with
j/k or arrows, Enter to select.

## Self-hosting the signaling worker

The worker is a single Cloudflare Worker + Durable Object. Deploy to your own
account in one command:

```sh
cd worker
npx wrangler login          # if not already authenticated
../scripts/deploy-worker.sh
```

Then point clients at your worker:

```sh
jsi send file --externals full --server https://your-worker.workers.dev
```

**TURN relay** (needed by ~10% of NATs): the worker runs STUN-only by default.
To enable TURN fallback, create a TURN key in the Cloudflare dashboard (Realtime
→ TURN) and follow the instructions in `scripts/deploy-worker.sh`.

## Testing

```sh
# Unit + integration tests (all packages, race detector)
go test -race ./...

# Worker tests (27 tests, isolated DO storage)
cd worker && npm install && npm test

# End-to-end (real transfers)
scripts/e2e-paste.sh      # zero-contact paste transfer, strace-proven
scripts/e2e-cli.sh        # 100 MiB through a local wrangler dev worker
```

## Project status

| Milestone | Description | Status |
|---|---|---|
| M1 | Signaling worker (SP/1) | ✅ deployed |
| M2 | Go core library | ✅ |
| M3 | CLI | ✅ |
| M4 | QR signaling (QP/1) | ✅ |
| M5 | TUI (Bubble Tea) | ✅ |
| M6 | Polish / release | ✅ |

**What works:** paste/worker/QR signaling, direct P2P transfer with SHA-256
integrity, the TUI, self-hostable worker with Durable Objects.

**Not yet built:** TURN relay (deferred — no card on file), PWA with camera QR
scanning (S4), Yggdrasil network transport (S1–S3).

## Documentation

- [PLAN.md](PLAN.md) — architecture, decisions (D1–D17), full task breakdown
- [proto/SIGNALING.md](proto/SIGNALING.md) — SP/1 (worker HTTP) + QP/1 (QR/paste) wire formats
- [proto/TRANSFER.md](proto/TRANSFER.md) — TP/1 data-channel protocol
- [AGENTS.md](AGENTS.md) — contributing guide and working agreements

## License

MIT — see [LICENSE](LICENSE).
