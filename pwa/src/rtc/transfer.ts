// TP/1 transfer engine — TypeScript port of internal/transfer/transfer.go.
// Send/receive state machines over a WebRTC data channel: JSON control +
// raw 16 KiB binary chunks, flow-controlled watermarks, streaming SHA-256.
// proto/TRANSFER.md is the normative contract.

import { sha256 } from "@noble/hashes/sha256";
import {
  type Message,
  type FileMeta,
  marshal,
  unmarshal,
  sanitizeName,
  ChunkSize,
  HighWatermark,
  LowWatermark,
  Version,
} from "../protocol";

export interface SendFile {
  path: string; // unused in browser, kept for API parity
  name: string;
  size: number;
  data: Blob | File; // the actual bytes
  mime?: string;
}

export type EventKind = "file-start" | "progress" | "file-done" | "done" | "error";

export interface TransferEvent {
  kind: EventKind;
  fileId: number;
  bytesDone: number;
  bytesTotal: number;
  err?: Error;
}

export type ProgressCallback = (ev: TransferEvent) => void;

export class TransferError extends Error {
  constructor(
    public code: string,
    message: string,
  ) {
    super(message);
    this.name = "TransferError";
  }
}

export const ErrRejected = new TransferError("rejected", "the receiver rejected the transfer");
export const ErrDeclined = new TransferError("declined", "transfer declined");
export const ErrHashMismatch = new TransferError("hash-mismatch", "sha-256 mismatch");
export const ErrPeerCancel = new TransferError("peer-cancel", "canceled by the peer");
export const ErrProtocol = new TransferError("protocol", "protocol violation");

// --- Sender ---

export async function send(
  channel: RTCDataChannel,
  files: SendFile[],
  onProgress?: ProgressCallback,
  signal?: AbortSignal,
): Promise<void> {
  const inbox = setupInbox(channel);

  // hello
  sendText(channel, { type: "hello", v: Version, app: "jsi-pwa/0.1.0" });

  // manifest
  const manifest: FileMeta[] = files.map((f, i) => ({
    id: i,
    name: sanitizeName(f.name),
    size: f.size,
    mime: f.mime,
  }));
  sendText(channel, { type: "manifest", files: manifest });

  // wait for accept / reject
  const acceptMsg = await inbox.next(signal);
  if (acceptMsg.type === "reject") throw ErrRejected;
  if (acceptMsg.type !== "accept") throw ErrProtocol;

  const acceptedIds: number[] =
    acceptMsg.files && acceptMsg.files.length > 0
      ? acceptMsg.files
      : manifest.map((m) => m.id);

  // send each accepted file
  for (const id of acceptedIds) {
    const file = files[id];
    onProgress?.({ kind: "file-start", fileId: id, bytesDone: 0, bytesTotal: file.size });

    sendText(channel, { type: "file-start", id });

    // stream the file in 16 KiB chunks with flow control
    const reader = file.data.stream().getReader();
    const hasher = sha256.create();
    let sent = 0;

    while (true) {
      if (signal?.aborted) {
        sendText(channel, { type: "cancel" });
        throw new DOMException("Aborted", "AbortError");
      }
      // Flow control (D9): pause at 1 MiB, resume at 512 KiB.
      if (channel.bufferedAmount >= HighWatermark) {
        await waitForBufferLow(channel, signal);
      }
      const { done, value } = await reader.read();
      if (done) break;
      if (value && value.byteLength > 0) {
        hasher.update(value);
        // Split large reads into ≤16 KiB chunks.
        for (let off = 0; off < value.byteLength; off += ChunkSize) {
          const chunk = value.subarray(off, Math.min(off + ChunkSize, value.byteLength));
          channel.send(chunk);
          sent += chunk.byteLength;
          onProgress?.({ kind: "progress", fileId: id, bytesDone: sent, bytesTotal: file.size });
        }
      }
    }
    reader.releaseLock();

    const hash = bytesToHex(hasher.digest());
    sendText(channel, { type: "file-end", id, sha256: hash });

    // wait for file-ack
    const ack = await inbox.next(signal);
    if (ack.type !== "file-ack") throw ErrProtocol;
    if (ack.status === "hash-mismatch") throw ErrHashMismatch;
    onProgress?.({ kind: "file-done", fileId: id, bytesDone: file.size, bytesTotal: file.size });
  }

  sendText(channel, { type: "done" });
  onProgress?.({ kind: "done", fileId: -1, bytesDone: 0, bytesTotal: 0 });
  inbox.close();
}

// --- Receiver ---

export interface ReceiveResult {
  name: string;
  size: number;
  sha256: string;
  blob: Blob;
}

export async function receive(
  channel: RTCDataChannel,
  onProgress?: ProgressCallback,
  signal?: AbortSignal,
): Promise<ReceiveResult[]> {
  const inbox = setupInbox(channel);

  // hello
  sendText(channel, { type: "hello", v: Version, app: "jsi-pwa/0.1.0" });

  // wait for manifest
  const manifestMsg = await inbox.next(signal);
  if (manifestMsg.type !== "manifest") throw ErrProtocol;

  // accept all
  sendText(channel, { type: "accept" });

  const results: ReceiveResult[] = [];

  for (const meta of manifestMsg.files) {
    const startMsg = await inbox.next(signal);
    if (startMsg.type !== "file-start") throw ErrProtocol;

    onProgress?.({
      kind: "file-start",
      fileId: meta.id,
      bytesDone: 0,
      bytesTotal: meta.size,
    });

    // receive binary chunks until file-end
    const chunks: ArrayBuffer[] = [];
    let received = 0;
    const hasher = sha256.create();

    while (true) {
      const msg = await inbox.next(signal);
      if (msg.type === "binary") {
        hasher.update(new Uint8Array(msg.data));
        chunks.push(msg.data);
        received += msg.data.byteLength;
        onProgress?.({
          kind: "progress",
          fileId: meta.id,
          bytesDone: received,
          bytesTotal: meta.size,
        });
      } else if (msg.type === "file-end") {
        if (received !== meta.size) throw ErrProtocol;
        const hash = bytesToHex(hasher.digest());
        const status = hash === msg.sha256 ? "ok" : "hash-mismatch";
        sendText(channel, {
          type: "file-ack",
          id: meta.id,
          status,
          bytes: received,
        });
        if (status === "hash-mismatch") throw ErrHashMismatch;

        const blob = new Blob(chunks, { type: meta.mime ?? "application/octet-stream" });
        results.push({ name: meta.name, size: meta.size, sha256: hash, blob });
        onProgress?.({
          kind: "file-done",
          fileId: meta.id,
          bytesDone: received,
          bytesTotal: meta.size,
        });
        break;
      } else if (msg.type === "cancel") {
        throw ErrPeerCancel;
      } else {
        throw ErrProtocol;
      }
    }
  }

  // wait for done
  const doneMsg = await inbox.next(signal);
  if (doneMsg.type !== "done") throw ErrProtocol;
  onProgress?.({ kind: "done", fileId: -1, bytesDone: 0, bytesTotal: 0 });
  inbox.close();
  return results;
}

// --- Inbox: demultiplexes text (JSON control) and binary (chunks) messages ---

type InboxMessage =
  | { type: "binary"; data: ArrayBuffer }
  | (Message & { type: string });

interface Inbox {
  next: (signal?: AbortSignal) => Promise<InboxMessage>;
  close: () => void;
}

function setupInbox(channel: RTCDataChannel): Inbox {
  const queue: InboxMessage[] = [];
  let waiter: ((msg: InboxMessage) => void) | null = null;
  let closed = false;

  channel.binaryType = "arraybuffer";
  channel.onmessage = (e: MessageEvent) => {
    if (closed) return;
    if (typeof e.data === "string") {
      try {
        const msg = unmarshal(e.data);
        const inboxMsg = { ...msg, type: msg.type } as InboxMessage;
        if (waiter) {
          waiter(inboxMsg);
          waiter = null;
        } else {
          queue.push(inboxMsg);
        }
      } catch {
        // ignore malformed control
      }
    } else if (e.data instanceof ArrayBuffer) {
      const inboxMsg: InboxMessage = { type: "binary", data: e.data };
      if (waiter) {
        waiter(inboxMsg);
        waiter = null;
      } else {
        queue.push(inboxMsg);
      }
    }
  };

  channel.onclose = () => {
    closed = true;
    if (waiter) {
      waiter({ type: "cancel", reason: "channel closed" } as unknown as InboxMessage);
      waiter = null;
    }
  };

  return {
    next: (signal?: AbortSignal) =>
      new Promise((resolve, reject) => {
        if (queue.length > 0) {
          resolve(queue.shift()!);
          return;
        }
        if (closed) {
          reject(ErrPeerCancel);
          return;
        }
        waiter = (msg) => resolve(msg);
        signal?.addEventListener("abort", () => {
          reject(new DOMException("Aborted", "AbortError"));
        });
      }),
    close: () => {
      closed = true;
      channel.onmessage = null;
      channel.onclose = null;
    },
  };
}

function sendText(channel: RTCDataChannel, msg: Message): void {
  channel.send(marshal(msg));
}

function waitForBufferLow(channel: RTCDataChannel, signal?: AbortSignal): Promise<void> {
  channel.bufferedAmountLowThreshold = LowWatermark;
  return new Promise((resolve, reject) => {
    const onLow = () => {
      cleanup();
      resolve();
    };
    const onAbort = () => {
      cleanup();
      reject(new DOMException("Aborted", "AbortError"));
    };
    const cleanup = () => {
      channel.removeEventListener("bufferedamountlow", onLow);
      signal?.removeEventListener("abort", onAbort);
    };
    channel.addEventListener("bufferedamountlow", onLow);
    signal?.addEventListener("abort", onAbort);
  });
}

function bytesToHex(bytes: Uint8Array): string {
  let hex = "";
  for (const b of bytes) hex += b.toString(16).padStart(2, "0");
  return hex;
}
