// GET /v1/ice (M1.3, D6–D8). Mints per-request TURN credentials via the CF
// Realtime API and returns CF's iceServers array untouched (clients filter
// :53 URLs per platform, D8) plus a Google STUN fallback. Without TURN config
// (local dev) it returns STUN-only so no credentials are needed.

import type { Context } from "hono";
import type { Env } from "./env";

const TURN_API = "https://rtc.live.cloudflare.com/v1/turn/keys";
const GOOGLE_STUN = { urls: ["stun:stun.l.google.com:19302"] };
const STUN_ONLY = [{ urls: ["stun:stun.cloudflare.com:3478"] }, GOOGLE_STUN];

export async function iceServers(c: Context<{ Bindings: Env }>): Promise<Response> {
  const keyId = c.env.TURN_KEY_ID;
  const apiToken = c.env.TURN_API_TOKEN;
  if (!keyId || !apiToken) return c.json({ iceServers: STUN_ONLY, ttl: 3600 });

  try {
    const res = await fetch(`${TURN_API}/${keyId}/credentials/generate-ice-servers`, {
      method: "POST",
      headers: { authorization: `Bearer ${apiToken}`, "content-type": "application/json" },
      body: JSON.stringify({ ttl: 3600 }), // D7: long TURN ttl, not the 90 s session TTL
    });
    if (!res.ok) throw new Error(`TURN API status ${res.status}`);
    const data = (await res.json()) as { iceServers?: unknown };
    if (!Array.isArray(data.iceServers)) throw new Error("TURN API returned no iceServers array");
    return c.json({ iceServers: [...data.iceServers, GOOGLE_STUN], ttl: 3600 });
  } catch {
    return c.json({ error: { code: "internal", message: "failed to mint TURN credentials" } }, 500);
  }
}
