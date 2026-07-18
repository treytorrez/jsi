# TP/1 — JSI Transfer Protocol (data-channel wire format)

Version: TP/1 · Status: normative for M2+ · Governing decisions: PLAN.md §3 (D9, D10)

One WebRTC data channel carries control messages and file bytes. The same format
is spoken by Pion (Go) and `RTCPeerConnection` (browser). No per-chunk framing:
the channel is **ordered and reliable**, so bytes are a stream per file.

## Channel

- Label: `jsi` · created by the offerer with default options (ordered, reliable).
- Text messages = JSON control, one object per message, UTF-8.
- Binary messages = raw file chunk bytes. JS senders MUST set
  `dc.binaryType = "arraybuffer"`.

## Flow

```
both        → {"type":"hello","v":1,"app":"jsi-cli/0.1.0"}        // app optional
sender      → {"type":"manifest","files":[{"id":0,"name":"a.bin","size":123456,"mime":"…"}]}
receiver    → {"type":"accept"}                                   // all files
           or {"type":"accept","files":[0,2]}                     // subset
           or {"type":"reject","reason":"…"}                      // nothing
per accepted file, ascending id:
sender      → {"type":"file-start","id":0}
sender      → <binary chunks>      // each 1..16384 bytes; concatenation == exactly "size"
sender      → {"type":"file-end","id":0,"sha256":"<64 lowercase hex>"}
receiver    → {"type":"file-ack","id":0,"status":"ok","bytes":123456}
           or {"type":"file-ack","id":0,"status":"hash-mismatch","bytes":123456}
sender      → {"type":"done"}                                     // after last ack
either      → close channel
```

Any time, either side:
`{"type":"cancel","reason":"…"}` → peer acknowledges by closing the channel.
Protocol violation (unknown type, out-of-order state, bad size):
`{"type":"error","code":"protocol","message":"…"}` → close.

## Rules

1. `name`: UTF-8, ≤ 255 bytes, MUST NOT contain `/`, `\`, or NUL. Receivers
   additionally strip `..` path segments and collisions get `-1`, `-2`, … suffixes.
2. `size`: integer, 0 … 2⁵³−1 (JSON-safe). `files` array ≤ 1024 entries.
3. Chunks arrive only between `file-start` and `file-end`, only for the current
   file, in order. Their total MUST equal the declared `size`; `file-end` before
   the byte count is reached is a protocol error.
4. **SHA-256 is mandatory (D10).** Sender hashes while reading (streaming);
   receiver hashes while writing. Mismatch → `file-ack status:"hash-mismatch"`;
   receiver chooses keep-or-delete locally (CLI: delete; flag `--keep-corrupt`).
5. Implementations MUST ignore unknown control fields and SHOULD ignore unknown
   `type` values after `hello` (forward compatibility) — except state violations
   per rule 3, which are errors.

## Flow control (normative, D9)

- Chunk size: **16 KiB** (16,384 B) max — RFC 8831 §6.6 interop value.
- Sender pauses when `BufferedAmount() ≥ 1 MiB`, resumes at ≤ **512 KiB**
  (`SetBufferedAmountLowThreshold(512*1024)` + `OnBufferedAmountLow`; browsers use
  `bufferedamountlow`). Sender MUST NEVER let `BufferedAmount()` exceed 4 MiB
  (browser hard ceilings are ~16 MiB; headroom is deliberate).
- Pacing hint: back-to-back chunk sends are fine until the high watermark; the
  watermark loop is the only required pacing.

## Versioning

`hello.v` is the TP version (`1`). Peers with mismatched major versions MUST send
`{"type":"error","code":"version","message":"unsupported TP version"}` and close.
