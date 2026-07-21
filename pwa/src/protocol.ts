// TP/1 wire protocol — TypeScript port of internal/protocol (proto/TRANSFER.md).
// Same types, same JSON, same chunk size, same watermarks. Interop proven by
// cross-client e2e (PWA ↔ CLI transfer).

export const Version = 1;
export const ChunkSize = 16384; // RFC 8831 §6.6 interop size (D9)
export const HighWatermark = 1 << 20; // 1 MiB pause threshold
export const LowWatermark = 512 << 10; // 512 KiB resume threshold
export const MaxFiles = 1024;
export const MaxNameBytes = 255;

export interface FileMeta {
  id: number;
  name: string;
  size: number;
  mime?: string;
}

export type Message =
  | { type: "hello"; v: number; app?: string }
  | { type: "manifest"; files: FileMeta[] }
  | { type: "accept"; files?: number[] }
  | { type: "reject"; reason: string }
  | { type: "file-start"; id: number }
  | { type: "file-end"; id: number; sha256: string }
  | { type: "file-ack"; id: number; status: "ok" | "hash-mismatch"; bytes: number }
  | { type: "done" }
  | { type: "cancel"; reason?: string }
  | { type: "error"; code: string; message: string };

export function marshal(msg: Message): string {
  return JSON.stringify(msg);
}

export function unmarshal(data: string): Message {
  return JSON.parse(data) as Message;
}

// sanitizeName implements TP/1 rule 1: reject NUL, take base after / or \,
// reject empty/dot/dotdot, enforce ≤255 bytes.
export function sanitizeName(name: string): string {
  if (name.includes("\0")) throw new Error("name contains NUL");
  const base = name.split(/[\\/]/).pop() ?? name;
  if (base === "" || base === "." || base === "..") {
    throw new Error(`unsafe name: ${JSON.stringify(name)}`);
  }
  if (new TextEncoder().encode(base).length > MaxNameBytes) {
    throw new Error(`name exceeds ${MaxNameBytes} bytes`);
  }
  return base;
}
