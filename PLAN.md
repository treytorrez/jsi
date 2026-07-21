# JSI — Master Plan & Spec

**Just Send It** — serverless-first, P2P, private file transfer.
Version: spec v0.5 · Status: approved-for-implementation · Source: `proj-proposal.docx` v0.2

> v0.5: QP/1 payload codec + paste channel move from M4 into M3 (tasks M3.6/M3.7)
> — the D15 `none` default is honored from the first CLI build; M4 becomes
> QR presentation (frames, rendering, camera) only.
> v0.4: adds D15 — user-controlled external-services policy (signaling × transport
> axes; default = no external infrastructure except STUN).

This document is the single source of truth for architecture, component boundaries,
milestones, and task decomposition. Wire formats live in `proto/`. Working agreements
for coding agents live in `AGENTS.md`. If any of these files disagree, **the protocol
files win for wire formats, this file wins for scope and sequencing**.

---

## 1. Core deliverable & alignment test

> Two people share a QR code or short token. A WebRTC data channel opens directly
> between their devices. The file crosses it, encrypted. No accounts, no size limits,
> no server ever touches the bytes.

Every design decision below was checked against four pillars:

1. **P2P** — bytes flow device-to-device; infrastructure only brokers introductions.
2. **Private** — end-to-end DTLS encryption is mandatory and non-optional; the
   signaling layer learns nothing about content and forgets everything within 90 s.
3. **Architecture-less** — total infrastructure is one stateless ~200-line Cloudflare
   Worker on the free tier, self-hostable in one command; QR mode (M4) removes even
   that for local transfers.
4. **Multiplatform** — one protocol, three clients (Go CLI, Go TUI, browser PWA);
   any pair interoperates.

Where the original proposal conflicts with these pillars or with verified platform
facts, this plan diverges — each divergence is listed in §3 with rationale.

---

## 2. Verified platform facts (research-backed, 2026-07)

These numbers are load-bearing for the design. Sources: Cloudflare docs, RFC 8831/8841,
pkg.go.dev, BC-UR papers. Full citations were gathered during planning; key ones inline.

### Cloudflare free tier (hard limits)

| Resource | Free limit | Impact on JSI |
|---|---|---|
| Workers requests | 100,000/day | ~1,000 sessions/day ceiling (≈100 req/session with polling) |
| KV reads | 100,000/day | not the binding constraint |
| **KV writes** | **1,000/day** | **binding constraint: ~500 sessions/day** (2 writes/session) |
| KV deletes | 1,000/day | reason v1 has no DELETE endpoint (TTL-only expiry) |
| KV min `expirationTtl` | 60 s | 90 s session TTL is valid |
| Worker CPU | 10 ms/req | trivially fine (KV I/O wait doesn't count) |
| TURN (Realtime) | 1 TB free, then $0.05/GB egress | fallback path only (~10% of connections) |
| STUN | `stun.cloudflare.com:3478` free, unlimited | primary |
| Pages / Workers Static Assets | unlimited free static bandwidth | PWA hosting (S4) |
| Rate-limit binding | available on free tier, per-location | abuse mitigation on POST endpoints |

### WebRTC / Pion facts

- `pion/webrtc` is at **v4** — import path `github.com/pion/webrtc/v4`.
- Non-trickle pattern: `GatheringCompletePromise(pc)` created **before**
  `SetLocalDescription`; final SDP (with all `a=candidate` lines) read from
  `pc.LocalDescription()` after the channel closes.
- Data channel default (`CreateDataChannel(label, nil)`) = ordered + reliable,
  matching browser defaults. Keep it — file transfer must not use lossy modes.
- **Chunk size: 16 KiB** (RFC 8831 §6.6 conservative interop size; 64 KiB is the
  negotiated default ceiling per RFC 8841; Chromium accepts ~256 KiB but that's
  not portable). Spec uses 16 KiB.
- Flow control trio exists in Pion and browsers: `BufferedAmount()`,
  `SetBufferedAmountLowThreshold()`, `OnBufferedAmountLow()`. Official Pion example
  uses **512 KiB low / 1 MiB high** — adopted as spec values (browser hard ceiling
  ~16 MiB, so 1 MiB is safely below).
- DTLS is **mandatory and automatic** in Pion (no off switch); SDP carries
  `a=fingerprint:SHA-256 …` (RFC 8122). Cipher: ECDHE-ECDSA AES-GCM.
- Browser non-trickle: `onicecandidate` fires with `candidate === null` at end of
  gathering → send `pc.localDescription`.

### QR facts (M4)

- QR byte-mode max: 2,953 B at version 40-L, but V40 needs ~185 terminal columns
  and is effectively unscannable from a terminal. Comfortable terminal QR ≈ V10–V20
  (≈271–858 B at EC level L).
- A typical full SDP (1.5–3 KB) **does not fit one scannable frame** → multi-frame
  animated QR is the M4 default, not an edge case.
- Precedent: Blockchain Commons **UR/MUR** — indexed frames with a whole-message
  CRC-32; optional Luby-transform fountain frames for loss tolerance. QP/1 (§6.3)
  adopts the fixed-rate degenerate case; fountain frames are a documented upgrade.
- Deflate on SDP: **measured 3.1x–7.0x (spike M4.1)** — fatter SDPs compress
  better; worst config deflates to ≤830 B → 2–3 frames at 400 B chunks.
  See QP/1 Appendix A in `proto/SIGNALING.md`.
- Go QR: `mdp/qrterminal/v3` (maintained, half-block terminal renderer).
  `skip2/go-qrcode` is unmaintained since 2020 — avoid.

### Yggdrasil / QUIC facts (stretch)

- Yggdrasil: encrypted IPv6 overlay (`0200::/7`), addresses derived from ed25519
  keys. Embeddable via `yggdrasil-go/src/core` (`core.New(...)`) — exposes a
  `net.PacketConn`-like interface, **no streams, no TUN needed** when embedded.
  Status: self-declared alpha, active. License **LGPLv3 + static-linking exception**
  (read exception terms before embedding — flagged, S1 gate).
- quic-go: production-ready, `quic.Transport{Conn: net.PacketConn}` is the exact
  seam for QUIC-over-Yggdrasil. TLS 1.3 mandatory → runtime self-signed ECDSA cert
  + peer key pinning (libp2p precedent), no PSK support. Supports latest two Go
  releases only — pin Go deliberately.

---

## 3. Decisions (incl. divergences from the proposal)

| # | Decision | Rationale |
|---|---|---|
| D1 | **Milestones table is authoritative** over the Client Targets table (which listed CLI=M1, TUI=M2). | Internal consistency; the table was stale. |
| D2 | **Non-trickle ICE everywhere.** Clients gather all candidates, then exchange one fat SDP. | Worker needs only 2 KV writes and zero ICE endpoints; trickle is impossible over QR anyway → one uniform protocol. Cost: ~1–2 s slower connect. Accepted. |
| D3 | **Worker generates the token** (crypto RNG, retry on collision), not the sender. | Guarantees RNG quality; removes client-side collision handling. |
| D4 | **Token = 6 chars, Crockford Base32** (`0-9 A-H J-K M-N P-T V-Z`, no I/L/O/U), uppercase display, case-insensitive input. ~1.07×10⁹ space, 90 s TTL. | Ambiguity-free for verbal/QR exchange. Slightly smaller than the proposal's 36⁶ (~2.2×10⁹) — still brute-force-proof at 90 s + rate limits. |
| D5 | **No DELETE endpoint in v1.** TTL is the only forget mechanism. | KV deletes are metered (1k/day free); TTL alone guarantees "the worker forgets in ≤90 s" — the proposal's privacy promise — at zero quota cost. |
| D6 | **New endpoint `GET /v1/ice`** returns ICE server config (STUN + freshly minted TURN creds) *before* offer creation. | Non-trickle embeds candidates in the SDP, so TURN credentials must exist **before** `CreateOffer` — a chicken-and-egg problem the proposal missed. One extra request/session, no KV cost. |
| D7 | TURN credentials: worker mints via `POST https://rtc.live.cloudflare.com/v1/turn/keys/{TURN_KEY_ID}/credentials/generate-ice-servers` (Bearer token, `{"ttl": 3600}`). TTL = **3600 s**, not 90. | TURN allocations die when creds expire; a long relayed transfer must not drop at 90 s. 1 h covers realistic transfers; refresh-via-`setConfiguration` documented as future work. |
| D8 | Worker returns the full ICE server list; **clients filter `:53` URLs** (browsers block port 53; Go clients may keep them). | Platform-specific filtering belongs at the edge. |
| D9 | **16 KiB chunks, 512 KiB/1 MiB flow-control watermarks, ordered+reliable channel.** | RFC 8831 + official Pion example values; portable to browsers. |
| D10 | **SHA-256 per file is mandatory in TP/1 v1** (streamed during transfer, verified by receiver). | Cheap integrity from day one; foundation for S2 identity/integrity work. |
| D11 | **PWA hosts on Workers Static Assets, not Pages.** | Cloudflare now steers new static sites to Workers Static Assets; same unlimited free bandwidth, one fewer product, config lives beside the signaling worker. |
| D12 | M4 QR = **multi-frame animated by default** + copy-paste base64 fallback channel; camera scanning in the Go CLI via `pion/mediadevices` + `gozxing` is a **spike first** (cgo/platform risk). | QR density math (§2) makes single-frame infeasible; paste fallback guarantees M4 works even if camera deps fail. |
| D13 | Polling (not WebSocket/Durable Objects) for the answer. 1 s interval, client-side. | Keeps the worker stateless and free-tier; DOs/WebSockets are a documented future optimization (long-poll variant noted in SP/1). **Amended by D17.** |
| D14 | mDNS host candidates enabled in Go clients (`SettingEngine.SetICEMulticastDNSMode`). | Privacy parity with browsers — hides LAN IPs from the remote peer's view of SDP. |
| D15 | **External-services policy is user-controlled, default `none`.** Two orthogonal axes — signaling × transport — three presets plus per-axis overrides (C1-style mixing). `none`: QR signaling + host/STUN only, zero CF contact. `fallback`: QR first, escalate to worker on failure + TURN allowed (ICE-native last resort). `full`: worker signaling + TURN allowed (C2). Clients MUST display the established path (direct / STUN-assisted / TURN-relayed) and MUST announce any escalation to externals before using it. | User mandate: no accidental data through servers. STUN is the only always-permitted external (stateless, sees IPs only, never content). In `fallback`, `/v1/ice` is fetched upfront (anonymous, session-less) because adding TURN after a failed attempt would require a fresh QR scan cycle (ICE restart → new SDP exchange). |
| D16 | **Camera-based QR reading is application-scope, not CLI-scope.** The camera receiver is the PWA (S4): `getUserMedia` + native `BarcodeDetector` (Chrome/Android) with jsQR fallback (Safari), adaptive frame-sync. The CLI's offline receive path is paste-first; the Go-webcam spike (`mediadevices`+`gozxing`) is parked/optional, off the critical path. No dedicated mobile app in v1 — the PWA *is* the camera application. | User instinct: camera means building an application, so it gets a real framework/plan (S4) instead of cgo-haunted webcam deps in the CLI. M4 stays deliverable with paste guaranteed. |
| D17 | **Session storage: one SQLite Durable Object per session token** (`SESSION_DO`, token = object name), replacing the M1.2 KV namespace. `GET …/answer` long-polls server-side ~20 s. DO alarm = TTL forget mechanism (D5 unchanged). SP/1 HTTP contract byte-identical; clients unchanged. | KV reads are edge-cached (incl. misses) and eventually consistent: under 1 s polling the answer key flapped 200→404→200 and the sender never saw it inside the 90 s TTL. A per-session DO is strongly consistent, and the server-side long-poll collapses the client's 1 s polling into a couple of requests. Amends D13. |

Open question parked for M6: **license choice** (proposal says "open source from day
one"; default candidate MIT — note Yggdrasil stretch introduces LGPLv3+exception
code, which is compatible but must be attributed).

Deployment note (2026-07, owner decision): **TURN relay enabled** — Cloudflare
Realtime TURN key provisioned (key ID in `wrangler.jsonc`, API token as secret).
The worker returns STUN + TURN iceServers from `GET /v1/ice`. Cross-NAT and
AP-isolated networks now relay encrypted traffic through Cloudflare's TURN
(1 TB free tier, $0.05/GB after). Self-hosters: see `scripts/deploy-worker.sh`.

---

## 4. Architecture

```
 Sender (CLI/TUI/PWA)                    Receiver (CLI/TUI/PWA)
 ┌──────────────────┐                   ┌──────────────────┐
 │ cmd / ui         │                   │ cmd / ui         │
 │ transfer         │                   │ transfer         │
 │ peer (Pion/JS)   │                   │ peer (Pion/JS)   │
 │ signal client ───┼───────┐   ┌───────┼─── signal client │
 └──────────────────┘       │   │       └──────────────────┘
            │              ▼   ▼              │
            │        ┌────────────────┐       │
            │        │ CF Worker      │       │
            │        │  /v1/ice       │       │   (1) both sides GET /v1/ice
            │        │  /v1/sessions  │       │   (2) sender POSTs offer → token
            │        │  KV (TTL 90s)  │       │   (3) receiver GETs offer by token
            │        │  TURN minting  │       │   (4) receiver POSTs answer
            │        └────────────────┘       │   (5) sender polls answer
            │                                 │
            └─────── WebRTC data channel ─────┘
                     DTLS-encrypted, P2P
              (STUN direct, or TURN relay ~10%)

 M4 alternate path: signal client ⇄ QR frames / paste blob (no worker at all)
```

### Connection policy (D15 — user-controlled)

Signaling and transport are **orthogonal axes**. Presets (default **`none`**) or
per-axis overrides (`--signal`, `--relay/--no-relay`) — the C1/C2 mixing modes:

| Preset | Signaling | Transport | External contact |
|---|---|---|---|
| `none` (default) | QR / paste | host + STUN | STUN only |
| `fallback` | QR → worker on failure | host + STUN + TURN (relay = last resort) | anonymous `/v1/ice` upfront; worker only if QR fails |
| `full` (C2) | CF worker | host + STUN + TURN | worker session (≤90 s) |
| C1 mixed | explicit `--signal` | explicit `--relay`/`--no-relay` | per choice |

Transparency rules (normative): clients MUST show which path was established
(direct / STUN-assisted / TURN-relayed) and MUST announce any escalation to
externals before using it.

> **Is STUN necessary?** Same LAN: no (host/mDNS candidates suffice). Across the
> internet: practically yes — without it a peer can't learn its public IP:port and
> hole-punching can't be attempted (exceptions: global IPv6 both ends, or one side
> publicly reachable). STUN is stateless, carries no content, sees only IPs — the
> most benign external in the design. (Parked alternative: NAT-PMP/PCP/UPnP
> router mappings, torrent-client style.)

---

## 5. Repository layout (monorepo)

```
jsi/
├── PLAN.md                  # this file — scope, sequencing, tasks
├── AGENTS.md                # working agreements for coding agents
├── LICENSE                  # chosen at M6
├── flake.nix                # dev shell (go, node, wrangler, lint)
├── go.mod                   # module github.com/treyt/jsi
├── cmd/
│   ├── jsi/main.go          # CLI binary (M3)
│   └── jsi-tui/main.go      # TUI binary (M5)
├── internal/
│   ├── token/               # token validate/normalize (Crockford Base32)
│   ├── protocol/            # TP/1 message types shared by sender+receiver
│   ├── signal/              # signaling: worker client, QR codec — one interface
│   ├── peer/                # Pion wrapper: non-trickle handshake, channel, flow ctl
│   └── transfer/            # file send/receive state machines over the channel
├── worker/                  # Cloudflare Worker (TypeScript, Hono)
│   ├── src/index.ts
│   ├── test/
│   ├── wrangler.jsonc
│   └── package.json
├── proto/
│   ├── SIGNALING.md         # SP/1 (worker HTTP) + QP/1 (QR frames)
│   └── TRANSFER.md          # TP/1 (data-channel wire format)
├── pwa/                     # S4 — vanilla TypeScript, Workers Static Assets
└── scripts/                 # e2e helpers, deploy helpers (M3.5, M6)
```

`internal/` is deliberate: the protocol is versioned as a whole with the repo, and
no external consumers are supported yet. Promotion to `pkg/` is an M6+ decision.

---

## 6. Protocols (summary — normative text in `proto/`)

### 6.1 SP/1 — Worker signaling protocol (`proto/SIGNALING.md`)

| Step | Call | Notes |
|---|---|---|
| 1 | `GET /v1/ice` | → `{iceServers:[…]}` incl. fresh TURN creds (ttl 3600). No session needed. |
| 2 | `POST /v1/sessions` `{offer}` | → `201 {token, expiresAt}`. Worker mints Crockford token, writes KV (TTL 90 s). |
| 3 | `GET /v1/sessions/:token/offer` | → `200 {offer}` or 404. Receiver side. |
| 4 | `POST /v1/sessions/:token/answer` `{answer}` | → `201`; 404 unknown token; 409 answer exists. |
| 5 | `GET /v1/sessions/:token/answer` | → `200 {answer}` or 404. Sender polls at 1 s. |

SDP blobs are JSEP JSON `{type, sdp}` (identical shape in Pion and browsers).
Errors are `{error:{code,message}}` with a closed code set. Rate-limit binding on
all POSTs. CORS `*` (protocol is public; PWA may be hosted anywhere).

### 6.2 TP/1 — Transfer protocol (`proto/TRANSFER.md`)

One data channel, label `jsi`, ordered+reliable. Text messages = JSON control
(`hello`, `manifest`, `accept`/`reject`, `file-start`, `file-end`, `file-ack`,
`cancel`, `error`); binary messages = raw file chunks ≤16 KiB, bare bytes (the
ordered channel + declared size make per-chunk headers unnecessary). Files transfer
sequentially. Sender pauses at 1 MiB buffered, resumes at 512 KiB. SHA-256 per file
streamed by both sides; receiver verifies and reports in `file-ack`.

### 6.3 QP/1 — QR signaling (`proto/SIGNALING.md` §QP/1)

Payload = zlib-compressed JSEP JSON → split into indexed frames:
`JSI1 <seq>/<total> <crc32(payload)> <chunk>`. Rendered as animated QR loop
(~400 B/frame default, EC level M) or emitted as a single base64url blob for
copy-paste. Handshake is two-phase and reverses direction: sender's offer frames →
receiver scans; receiver's answer frames → sender scans. ICE servers follow the
D15 policy: preset `none` = host + STUN (zero CF contact); `fallback` adds TURN
via an anonymous `/v1/ice` call. Delivery: the paste blob ships first (M3.6) as
the default `none` channel; animated frames are the M4 UX upgrade.

---

## 7. Component specs

### 7.1 Worker (`worker/`) — M1

TypeScript, **Hono 4.x**, **wrangler.jsonc** (Wrangler v4; `compatibility_date` set
at scaffold time), Durable Object binding `SESSION_DO` (one SQLite-backed object
per session token, M1.9/D17 — supersedes the M1.2 KV namespace), rate-limit
binding `POST_LIMITER` (namespace per wrangler docs, 60 s period). Vars:
`SESSION_TTL_SECONDS=90`, `TURN_KEY_ID`; optional `ANSWER_WAIT_MS` (answer
long-poll budget, default 20000). Secrets (via `wrangler secret put`, `.dev.vars`
locally): `TURN_API_TOKEN`. Tests: **@cloudflare/vitest-pool-workers** (Vitest
^4.1, isolated per-test storage incl. DOs). Stateless; no logging of SDP bodies
(only route+status observability).

### 7.2 `internal/peer` — M2

```go
type Config struct{ ICEServers []webrtc.ICEServer }

// Offerer: creates pc + data channel "jsi", gathers fully, returns offer.
func Offer(ctx context.Context, cfg Config) (*Conn, webrtc.SessionDescription, error)
// Answerer: sets remote offer, creates+sets answer, gathers fully.
func Answer(ctx context.Context, cfg Config, offer webrtc.SessionDescription) (*Conn, webrtc.SessionDescription, error)

func (c *Conn) SetRemote(ctx context.Context, desc webrtc.SessionDescription) error
func (c *Conn) WaitOpen(ctx context.Context) error          // resolves on channel OnOpen
func (c *Conn) Channel() *webrtc.DataChannel                // valid after WaitOpen
func (c *Conn) Close() error
```

Enables mDNS (D14); sets `SetSCTPMaxMessageSize(0)` default; never disables
certificate verification. Flow-control helpers (`PauseSend`/`ResumeSend` watermark
wiring) live here so `transfer` stays pure state machine.

### 7.3 `internal/signal` — M2/M4

```go
type Channel interface {
    // Sender: publish offer, get token + a waiter for the answer.
    Announce(ctx context.Context, offer webrtc.SessionDescription) (token string, wait func(context.Context) (webrtc.SessionDescription, error), err error)
    // Receiver: fetch offer by token, get a responder for the answer.
    Join(ctx context.Context, token string) (offer webrtc.SessionDescription, respond func(context.Context, webrtc.SessionDescription) error, err error)
}
```

Implementations: `Worker{BaseURL}` (M2), `Paste{In, Out}` (M3.6),
`QR{Display, Scanner}` (M4). This single
seam is also where Yggdrasil signaling (S1) plugs in. Worker impl requires
`FetchICEServers(ctx)` before `Offer` (D6) — surfaced as
`Worker.ICEServers(ctx) ([]webrtc.ICEServer, error)`.

### 7.4 `internal/transfer` — M2

```go
type Event struct { Kind EventKind; FileID int; BytesDone, BytesTotal int64; Err error }
// Endpoint abstracts the peer connection (satisfied by *peer.Conn): control
// messages ride Channel(); file chunks go through WriteFlow (D9 watermarks).
type Endpoint interface {
    Channel() *webrtc.DataChannel
    WriteFlow(ctx context.Context, data []byte) error
}
func Send(ctx context.Context, ep Endpoint, files []File, ev chan<- Event) error
func Receive(ctx context.Context, ep Endpoint, destDir string, ev chan<- Event) error
```

Pure TP/1 state machines; UI-agnostic (events feed CLI bars and TUI bubbles alike).

### 7.5 CLI (`cmd/jsi`) — M3

- `jsi send <file...>` → ICE servers → offer → token + terminal QR of
  `https://<pwa-host>/#t=<token>` + wait → transfer → summary.
- `jsi receive <token> [-o dir]` → mirror flow.
- Flags (D15): `--externals none|fallback|full` (default `none`),
  `--signal paste|worker` (`qr` added in M4) and `--relay/--no-relay` (per-axis
  C1 overrides), `--server` (worker URL override), `--pwa-url` (base URL for the
  send-side QR; default TBD at S4 — bare token payload until then),
  `--no-stun` (host candidates only — true zero-contact), `--no-mdns` (broken-
  multicast escape hatch), `-o dir`, `-y`, `-v`.
  Default `none` works from M3 onward via the paste channel (M3.6): out-of-band
  blob exchange, zero CF contact.
- Progress: `schollz/progressbar/v3` (only UI dep). Exit codes: 0 ok, 1 generic,
  2 signaling timeout, 3 peer rejected, 4 integrity failure.

### 7.6 TUI (`cmd/jsi-tui`) — M5

Bubble Tea + Bubbles (filepicker, progress) + Lipgloss. Screens: home → send
(file pick → token+QR view → progress → done) / receive (token input → progress →
done). Same `internal/*` stack; zero new protocol code.

### 7.7 PWA (`pwa/`) — S4

Vanilla TypeScript + Vite (no runtime framework). Styling: **Material Web
Components** (Material 3, framework-free web components, tree-shaken by Vite —
Material look without a JS framework's cost; user-requested). `RTCPeerConnection`
non-trickle (`candidate === null` sentinel), `binaryType='arraybuffer'`, same
TP/1. QR scan: native `BarcodeDetector` where available, jsQR fallback (D16).
Host: Workers Static Assets (D11). Mobile-first; installable (manifest +
service worker).

---

## 8. Milestones, tasks, and dependency graph

Task IDs are stable — agents reference them in commits (`m2.4: offer/answer
gathering`). `∥` = parallelizable with siblings after deps met. Every task's
"acceptance" is its Definition of Done.

### M1 — Signaling worker ✦ *two clients exchange SDP via worker*

| ID | Task | Deps | Acceptance |
|---|---|---|---|
| M1.1 | Scaffold worker: wrangler.jsonc, Hono, `GET /health` | — | `wrangler dev` serves 200 |
| M1.2 | KV namespace + session store module (put/get offer/answer, TTL) | M1.1 | unit-tested against vitest-pool KV |
| M1.3 | `GET /v1/ice` + TURN minting + filtering per D7 | M1.1 ∥ M1.2 | returns iceServers incl. `turn:`+`turns:` URLs; secret via `.dev.vars` |
| M1.4 | `POST /v1/sessions`, `GET …/offer` | M1.2 | 201+token (Crockford, D4); 404 unknown |
| M1.5 | `POST …/answer`, `GET …/answer` | M1.2 ∥ M1.4 | 409 on duplicate answer |
| M1.6 | Errors `{error:{code,message}}`, CORS, rate-limit binding on POSTs | M1.3–1.5 | closed code set per SP/1; 429 path tested |
| M1.7 | Vitest suite: full handshake flow, TTL behavior, validation | M1.4–1.6 | green in `worker/test/` |
| M1.8 | Deploy: `npm run deploy`, `scripts/deploy-worker.sh` (secrets prompt, self-host README section) | M1.7, M1.9 | fresh CF account → working worker in ≤5 min |
| M1.9 | Replace KV with per-session SessionDO (D17) + server-side answer long-poll | M1.7 | tsc clean, vitest green, `wrangler dev` smoke: full SP/1 flow + observed 20 s long-poll |

### M2 — Core Go library ✦ *direct WebRTC transfer between two Go processes*

| ID | Task | Deps | Acceptance |
|---|---|---|---|
| M2.1 | `go mod init github.com/treyt/jsi`, package skeletons, lint baseline | — | `go build ./...` clean |
| M2.2 | `internal/token`: validate/normalize Crockford | M2.1 ∥ | table-driven tests, case-insensitive |
| M2.3 | `internal/protocol`: TP/1 types + JSON codec | M2.1 ∥ | round-trip tests per TRANSFER.md |
| M2.4 | `internal/peer`: Offer/Answer/SetRemote/WaitOpen, mDNS, non-trickle gather | M2.1 ∥ | two PCs connect over host candidates in-test (no network) |
| M2.5 | `internal/signal` Worker impl + `ICEServers` | M2.1, SP/1 stable ∥ | tests vs `httptest` fake worker |
| M2.6 | `internal/transfer`: Send/Receive state machines, chunking, watermarks, SHA-256 | M2.3, M2.4 | in-process channel test: 10 MiB random file, hash match, cancel mid-flight |
| M2.7 | Integration: two processes, real Pion, in-process fake signaling | M2.5, M2.6 | file arrives bit-identical (loopback) |
| M2.8 | Integration: two processes via `wrangler dev` (real SP/1) | M1.8, M2.7 | CI-runnable script `scripts/e2e-local.sh` |

### M3 — CLI ✦ *file transfers across the internet via terminal*

| ID | Task | Deps | Acceptance |
|---|---|---|---|
| M3.1 | `jsi send`: flags, offer flow, token + QR display (`mdp/qrterminal/v3`) | M2.7 | token printed; QR decodes to `…#t=TOKEN` |
| M3.2 | `jsi receive`: token arg, `-o`, answer flow | M2.7 ∥ | receives to dir |
| M3.3 | Progress bars + events → UX, exit codes per §7.5 | M3.1, M3.2 | live progress both sides |
| M3.4 | Error paths: timeout (exit 2), reject (3), hash fail (4), SIGINT cancel | M3.3 | each path exercised in test |
| M3.5 | e2e: two CLIs over deployed dev worker transfer 100 MiB, sha256 compare | M1.8, M3.3 | `scripts/e2e-cli.sh` green |
| M3.6 | QP/1 payload codec (zlib, CRC-32, base64url) + `signal.Paste` + CLI wiring; default preset `none` goes live | M2.5, M3.2 | offline paste transfer between two terminals; `jsi send` with no flags never contacts CF |
| M3.7 | e2e paste: blobs piped between two CLIs, network-isolation assertion (no CF traffic) | M3.6 | `scripts/e2e-paste.sh` green — strace-proven: every sendto/connect destination is local (unshare-netns abandoned: Pion rightly omits loopback host candidates) |

### M4 — Serverless contact (QR signaling) ✦ *SDP+ICE over QR, CF only as fallback*

| ID | Task | Deps | Acceptance |
|---|---|---|---|
| M4.1 | **SPIKE** `spike/qr-size`: measure deflated SDP sizes vs QR capacities; decide frame size | M2.4 | report committed to `proto/SIGNALING.md` appendix |
| M4.2 | QP/1 frame codec: split/join indexed frames, CRC-32 verify (payload codec + paste channel landed in M3.6) | M4.1 | round-trip + corruption + frame-loss tests |
| M4.3 | Terminal animated-QR renderer (frame loop, adjustable fps/size) | M4.2 | scans reliably with a phone camera |
| M4.4 | ~~SPIKE webcam capture~~ **PARKED** (D16): camera receive is PWA scope (S4); CLI offline receive is paste-first | — | no CLI camera dep on the critical path |
| M4.5 | `signal.QR` implements `Channel`; CLI `--signal qr` both roles (receive via paste or QR display scan by PWA) | M4.3 | CLI↔CLI transfer; zero-contact proof via the strace approach from M3.7 (namespace proof impossible: Pion omits loopback candidates) |
| M4.6 | D15 policy wiring: `--externals` presets + per-axis overrides; `fallback` escalation to worker with explicit user notice; anonymous `/v1/ice` upfront fetch | M4.5 | zero CF contact provable in `none`; escalation notice shown in `fallback` |

### M5 — TUI ✦ *full interactive send/receive flow*

| ID | Task | Deps | Acceptance |
|---|---|---|---|
| M5.1 | App shell: Bubble Tea model, routing, theme | M3 (reuses stack) | navigable skeleton |
| M5.2 | Send flow: filepicker → token/QR → progress → done | M5.1 | end-to-end in TUI |
| M5.3 | Receive flow: token input → progress → done | M5.1 ∥ | end-to-end |
| M5.4 | Cancel, errors, retry; QR + paste views for `--signal qr` | M5.2, M5.3, M4.5 | parity with CLI flags |
| M5.5 | Help screen, keymap polish, README GIF source (vhs tape) | M5.4 | demo-recordable |

### M6 — Polish ✦ *self-hostable, documented, demo*

| ID | Task | Deps | Acceptance |
|---|---|---|---|
| M6.1 | README: pitch, install, quickstart, screenshots | M5 | renders on GitHub |
| M6.2 | Self-host guide: worker deploy + `--server` + TURN keys | M1.8 | followed by a fresh user successfully |
| M6.3 | LICENSE + NOTICE (decide MIT; pre-clear Yggdrasil LGPL note for S1) | — | file present |
| M6.4 | CI (GitHub Actions): `go build/test`, worker vitest, lint | M2.8, M1.7 | green on PR |
| M6.5 | goreleaser: cross-compile CLI+TUI, checksums, GitHub releases | M6.4 | tag → release artifacts |
| M6.6 | Demo GIF (vhs), proto docs final review, PLAN/AGENTS sync | M6.1–6.5 | docs match code |

### Stretch (each gated on all M-milestones)

| ID | Task | Notes/Acceptance |
|---|---|---|
| S1 | Yggdrasil signaling | License gate (LGPLv3+exception) → embed `yggdrasil-go/src/core`; `signal.Ygg` implements `Channel`; token carries sender's ed25519-derived address; two clients exchange SDP with **no CF and no LAN**. Alpha-status caveat documented. |
| S2 | Integrity & identity | SHA-256 already baseline (D10). Add: post-connect exchange of SSH/GPG public keys over the channel; fingerprint displayed both sides for out-of-band compare; known-peers file (opt-in). Defends signaling-MITM (§10). |
| S3 | QUIC transport over Yggdrasil | quic-go `Transport{Conn: yggCore}` (PacketConn seam verified); self-signed cert + key pinning; parallel streams per file; benchmark vs WebRTC path. |
| S4 | PWA | Per §7.7. DoD: browser↔CLI transfer of a 1 GiB file, QR scan-to-receive on mobile Safari + Chrome. |

### S4 QR scanning — PWA↔PWA (in progress)

**Scope:** PWA↔PWA only. Encodes the paste blob (`jsi1:...`) as a single large
static text-safe QR (no QP/1 binary frames — those are CLI-only). Two-scan
flow: sender displays offer QR → receiver scans → receiver displays answer QR
→ sender scans → connect.

**Sizes (M4.1 spike):** paste blobs are 767–924 B — fits one QR at V20-L (858 B)
or V23-M (~1,100 B). No animation needed.

**Camera:** `BarcodeDetector` (Chrome/Edge/Android) with `jsQR` fallback
(Safari/Firefox). `getUserMedia` for camera access.

**Future interop note:** CLI↔PWA QR interop requires the PWA to decode QP/1
binary frames (JSI1 magic) and handle animated multi-frame scanning. The CLI
encodes binary frames; the PWA currently encodes text-safe paste blobs. A
future task should port `JoinFrames` to TS and add multi-frame scanning
support. Until then, CLI↔PWA uses paste or worker mode.

**Tasks:**
| ID | Task | Deps | Effort |
|---|---|---|---|
| S4.Q1 | Install jsqr dep + create `rtc/scanner.ts` (camera + decode) | — | Small |
| S4.Q2 | Create `screens/qr-send.ts` (display offer QR + scan answer QR) | S4.Q1 | Medium |
| S4.Q3 | Create `screens/qr-receive.ts` (scan offer QR + display answer QR) | S4.Q1 | Medium |
| S4.Q4 | Wire QR mode into privacy→action→send/receive flow | S4.Q2, S4.Q3 | Small |
| S4.Q5 | Test PWA↔PWA QR transfer end-to-end | S4.Q4 | Small |

---

## 9. Testing strategy

- **Go**: table-driven unit tests per package; integration without internet (host
  candidates + in-process fake signaler, M2.7); race detector on.
- **Worker**: `@cloudflare/vitest-pool-workers` with isolated KV per test; TTL
  behavior via mocked timers/advance.
- **e2e**: `scripts/e2e-local.sh` (wrangler dev + two CLIs), `scripts/e2e-cli.sh`
  (deployed dev worker). Both produce random files, verify sha256.
- **M4 proof**: network-namespace isolation test demonstrating zero CF traffic.
- **CI** (M6.4) runs everything except S-milestones.

## 10. Security & privacy model

- DTLS (ECDHE-ECDSA, AES-GCM) encrypts all channel traffic; **cannot be disabled**
  in Pion or browsers. TURN relays see ciphertext only.
- Worker sees: IPs, tokens, SDP blobs (candidates + DTLS fingerprint). Retention
  ≤90 s by TTL. No content, no transfer logs.
- **Known residual risk**: a malicious signaling server could swap SDPs and MITM
  the DTLS handshake. Mitigations: self-hosting (one command), S2 fingerprint
  verification, M4 bypassing the server entirely. Documented honestly in README.
- Tokens: 30 bits entropy / 90 s / rate-limited POSTs → brute force impractical.
- TURN creds are per-request, TTL 3600 s, revocable via CF API; API token lives
  only in worker secrets.
- mDNS host candidates hide LAN IPs (D14).
- **External contact is opt-in (D15).** Default preset `none` touches only STUN
  (stateless; sees IPs, never content). TURN is reachable only in
  `fallback`/`full` and carries DTLS ciphertext. The established path is always
  displayed to the user.
- Future consideration: forced-relay mode (`iceTransportPolicy: "relay"`) to hide
  client IPs from the *peer* as well — not in v1.

## 11. Risks & mitigations

| Risk | Mitigation |
|---|---|
| KV free tier caps ~500 sessions/day | Accept for launch; Workers Paid ($5) lifts to ~16k/day; self-hosters scale themselves |
| QR camera deps (cgo/platform quirks) | M4.4 spike + paste-blob fallback guarantees M4 ships |
| TURN free-tier wording ambiguous (1 TB reset cadence) | TURN is fallback-only (~10%); monitor via CF analytics; coturn documented as self-host alt |
| Yggdrasil alpha + LGPL | S1 gated on license review; stretch-only, never in core path |
| quic-go tracks latest 2 Go releases | pin Go in flake.nix; upgrade deliberately |
| Non-trickle adds ~1–2 s connect latency | Accepted (D2); uniform protocol > marginal latency |
| SDP munging breaking interop (M4 optimization) | v1 does **no** munging — zlib + multi-frame only |

## 12. Git workflow (binding — see AGENTS.md)

- **Commit early, commit often**: smallest atomic unit per commit; every task ID
  lands as ≥1 commit. Prefix messages with task/area (`m1.4: add POST /v1/sessions`).
- **Prototypes on `spike/<topic>` branches** (M4.1, M4.4, S1…); merge to `master`
  via squash once accepted, or delete. `master` history is never rewritten — undo
  with `git revert`.
- **Milestone tags** `m1`…`m6` after DoD verification; releases via goreleaser (M6.5).
- Planning baseline: initial commit contains this plan, proto specs, AGENTS.md,
  flake.nix, .gitignore, and the original proposal docx.

## 13. Out of scope (unchanged from proposal)

Async transfers (offline recipient), persistent identity/contact lists, group
transfers (>2 parties), chat/non-file functionality. S2 lays identity groundwork
without building contact lists.

## 14. Dev environment

`flake.nix` provides: Go (pinned), Node.js 22, Wrangler, golangci-lint, gopls.
`nix develop` enters the shell; non-Nix users get the same tool list from
`AGENTS.md`. No other global tooling required.
