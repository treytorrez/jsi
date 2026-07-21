# AGENTS.md — working agreements for coding agents

JSI ("Just Send It"): serverless-first P2P file transfer. WebRTC data channels
(DTLS-encrypted) carry the bytes; a ~200-line Cloudflare Worker brokers the
handshake and forgets everything in ≤90 s; QR mode (M4) works with no server at all.

**Before writing any code: read `PLAN.md` (scope, decisions D1–D14, task IDs) and
the relevant `proto/*.md` file (normative wire formats).** The spec is the contract
for parallel work — do not relitigate decisions D1–D14 in code; if the spec is
wrong or incomplete, fix the spec file in the same commit and say so in the message.

## Status

Spec v0.5. Landed: **M1 complete + deployed** (tags `m1`, `m3`) — worker SP/1 at
https://jsi-signal.treytorrez.workers.dev (M1.9/D17: per-session SessionDO +
20 s answer long-poll after KV eventual-consistency broke live polling; STUN-only
— TURN deferred by owner, no card on file; enable via scripts/deploy-worker.sh);
**M2 complete** (tag `m2`); **M3 complete** (CLI + strace-proven e2e);
**M4 complete** (tag `m4` — measured 2-frame QR handshakes, internal/qr renderer,
signal.QR, `--signal qr`; camera receive is PWA/S4 scope per D16);
**M5 complete** (tag `m5` — Bubble Tea TUI with send/receive/settings/help,
all three signaling modes, D15 policy + transparency, progress bars).
Next: M6 (polish/release). Update this section as milestones land.

## Layout

See `PLAN.md` §5. Go module: `github.com/treyt/jsi`. Worker: `worker/` (Hono +
wrangler.jsonc). Clients: `cmd/jsi`, `cmd/jsi-tui`, `pwa/` (S4).

## Commands (once code exists)

```sh
nix develop                          # dev shell (go, node 22, wrangler, lint)
go build ./... && go test ./...      # Go core + clients
golangci-lint run
cd worker && npm install && npm test # vitest-pool-workers, isolated KV
cd worker && npx wrangler dev        # local signaling for e2e
scripts/e2e-local.sh                 # two CLIs through local worker
```

## Git policy (binding — user-mandated)

- **Commit early, commit often.** Smallest atomic unit per commit; reference the
  task ID: `m1.4: add POST /v1/sessions`. Spec docs, tests, and code for a task
  land together.
- **Prototypes/spikes on `spike/<topic>` branches** (e.g. `spike/qr-size`,
  `spike/qr-camera`). Accepted spikes merge to `master` via squash; rejected ones
  are deleted with findings committed to the spec first.
- **Never rewrite `master` history** (no force-push, no rebase of published
  commits). Undo with `git revert`.
- Milestone tags `m1`…`m6` after Definition of Done is verified.
- Never commit secrets: `.dev.vars`, API tokens. `.gitignore` covers the rest.

## Style & dependency rules

- **Go**: stdlib first. Approved deps only: `pion/webrtc/v4`, `pion/datachannel`,
  `mdp/qrterminal/v3`, `schollz/progressbar/v3`, and (M5) Bubble Tea/Bubbles/
  Lipgloss. Anything else needs a one-line justification in the commit message.
  `go vet` clean; table-driven tests; race detector in CI.
- **Worker (TS)**: Hono only — no validation framework; validate manually (the
  API surface is 5 routes). Target ~200 lines for `src/`. Never log SDP bodies.
- **Protocol changes**: update `proto/*.md` and the version note in the same
  commit. TP/1 and SP/1 changes are cross-client — check the PWA section before
  assuming Go-only impact.
- **Docs sync**: if you change structure, commands, or policy, update this file
  and `PLAN.md` in the same commit.

## Testing bar

Per PLAN.md §9: unit tests per package; M2.7 loopback integration (no internet);
worker vitest with isolated KV; e2e scripts before milestone tags. M4 requires the
network-isolation proof (no CF traffic in QR mode).
