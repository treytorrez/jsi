// Session routes (M1.4/M1.5, SP/1): sender POSTs one offer → worker mints a
// 6-char Crockford token (D3/D4); receiver fetches the offer, POSTs one
// answer; sender polls the answer (D13). Validation is manual per AGENTS.md.

import type { Context } from "hono";
import type { Env } from "./env";
import * as store from "./store";

type Ctx = Context<{ Bindings: Env }>;
type Status = 400 | 404 | 409 | 500;

const ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"; // Crockford Base32, no I/L/O/U (D4)
const TOKEN_RE = /^[0-9A-HJKMNP-TV-Z]{6}$/;
const MAX_SDP_BYTES = 32 * 1024;
const TOKEN_ATTEMPTS = 3; // collision retries before 500 (SP/1 §Token)
const TOKEN_HINT = "token must be 6 Crockford Base32 characters";

const err = (c: Ctx, status: Status, code: string, message: string): Response =>
  c.json({ error: { code, message } }, status);

function newToken(): string {
  const bytes = new Uint8Array(6);
  crypto.getRandomValues(bytes);
  let token = "";
  for (const b of bytes) token += ALPHABET[b % ALPHABET.length]; // 256 % 32 == 0 → uniform
  return token;
}

// Case-insensitive input, uppercase-normalized before lookup (D4). null = invalid.
function normalizeToken(raw: string): string | null {
  const token = raw.trim().toUpperCase();
  return TOKEN_RE.test(token) ? token : null;
}

function sessionTtl(c: Ctx): number {
  const n = Number.parseInt(c.env.SESSION_TTL_SECONDS ?? "", 10);
  return n > 0 ? n : 90; // SP/1 default
}

async function parseDescription(c: Ctx, field: "offer" | "answer"): Promise<store.Description | null> {
  let body: unknown;
  try {
    body = await c.req.json();
  } catch {
    return null; // malformed JSON
  }
  if (typeof body !== "object" || body === null) return null;
  const d = (body as Record<string, unknown>)[field];
  if (typeof d !== "object" || d === null) return null;
  const { type, sdp } = d as Record<string, unknown>;
  if (type !== field || typeof sdp !== "string") return null;
  if (new TextEncoder().encode(sdp).length > MAX_SDP_BYTES) return null;
  return { type: field, sdp };
}

// POST /v1/sessions — sender publishes its offer (M1.4).
export async function createSession(c: Ctx): Promise<Response> {
  const offer = await parseDescription(c, "offer");
  if (!offer) return err(c, 400, "invalid_body", 'expected {"offer":{"type":"offer","sdp":"…"}} with sdp ≤ 32 KiB');
  const ttl = sessionTtl(c);
  for (let attempt = 0; attempt < TOKEN_ATTEMPTS; attempt++) {
    const token = newToken();
    if (await store.hasSession(c.env.SESSIONS, token)) continue; // collision → regenerate
    await store.putOffer(c.env.SESSIONS, token, offer, ttl);
    return c.json({ token, expiresAt: new Date(Date.now() + ttl * 1000).toISOString() }, 201);
  }
  return err(c, 500, "internal", "could not allocate a token");
}

// GET /v1/sessions/:token/offer — receiver fetches the offer (M1.4).
export async function getSessionOffer(c: Ctx): Promise<Response> {
  const token = normalizeToken(c.req.param("token") ?? "");
  if (!token) return err(c, 400, "invalid_token", TOKEN_HINT);
  const offer = await store.getOffer(c.env.SESSIONS, token);
  if (!offer) return err(c, 404, "session_not_found", "unknown or expired token");
  return c.json({ offer });
}

// POST /v1/sessions/:token/answer — receiver publishes its answer (M1.5).
export async function postSessionAnswer(c: Ctx): Promise<Response> {
  const token = normalizeToken(c.req.param("token") ?? "");
  if (!token) return err(c, 400, "invalid_token", TOKEN_HINT);
  const answer = await parseDescription(c, "answer");
  if (!answer) return err(c, 400, "invalid_body", 'expected {"answer":{"type":"answer","sdp":"…"}} with sdp ≤ 32 KiB');
  if (!(await store.hasSession(c.env.SESSIONS, token)))
    return err(c, 404, "session_not_found", "unknown or expired token");
  if (await store.getAnswer(c.env.SESSIONS, token))
    return err(c, 409, "answer_exists", "an answer was already posted for this session");
  await store.putAnswer(c.env.SESSIONS, token, answer, sessionTtl(c));
  return c.json({}, 201);
}

// GET /v1/sessions/:token/answer — sender polls at 1 s intervals (M1.5, D13).
export async function getSessionAnswer(c: Ctx): Promise<Response> {
  const token = normalizeToken(c.req.param("token") ?? "");
  if (!token) return err(c, 400, "invalid_token", TOKEN_HINT);
  if (!(await store.hasSession(c.env.SESSIONS, token)))
    return err(c, 404, "session_not_found", "unknown or expired token");
  const answer = await store.getAnswer(c.env.SESSIONS, token);
  if (!answer) return err(c, 404, "answer_not_ready", "session exists but no answer has been posted yet");
  return c.json({ answer });
}
