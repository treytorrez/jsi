// QP/1 paste codec — TypeScript port of internal/signal/paste.go.
// Payload: JSON → zlib compress → "jsi1:" + base64url (no padding).
// Uses the browser's CompressionStream / DecompressionStream (no pako dep).
// proto/SIGNALING.md §QP/1 Paste format is the normative contract.

const PREFIX = "jsi1:";

export async function encodePayload(desc: RTCSessionDescriptionInit): Promise<string> {
  const json = JSON.stringify({ type: desc.type, sdp: desc.sdp });
  const compressed = await compress(new TextEncoder().encode(json));
  return PREFIX + base64url(compressed);
}

export async function decodePayload(s: string): Promise<RTCSessionDescriptionInit> {
  const trimmed = s.trim();
  if (!trimmed.startsWith(PREFIX)) {
    throw new Error('missing "jsi1:" prefix');
  }
  const compressed = base64urlDecode(trimmed.slice(PREFIX.length));
  const json = await decompress(compressed);
  const obj = JSON.parse(new TextDecoder().decode(json)) as { type: string; sdp: string };
  if (obj.type !== "offer" && obj.type !== "answer") {
    throw new Error(`type "${obj.type}", want offer or answer`);
  }
  return { type: obj.type as RTCSdpType, sdp: obj.sdp };
}

async function compress(data: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([data.slice()]).stream().pipeThrough(new CompressionStream("deflate"));
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}

async function decompress(data: Uint8Array): Promise<Uint8Array> {
  const stream = new Blob([data.slice()]).stream().pipeThrough(new DecompressionStream("deflate"));
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}

function base64url(data: Uint8Array): string {
  let binary = "";
  for (const b of data) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function base64urlDecode(s: string): Uint8Array {
  const padded = s + "=".repeat((4 - (s.length % 4)) % 4);
  const binary = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}
