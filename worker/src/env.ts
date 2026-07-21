// Worker bindings & vars (SP/1 §Cross-cutting; secrets land at M1.8).

import type { SessionDO } from "./sessiondo";

// Minimal shape of the Workers `ratelimits` binding. Declared locally so the
// type doesn't depend on a specific @cloudflare/workers-types release.
export interface RateLimitBinding {
  limit(options: { key: string }): Promise<{ success: boolean }>;
}

export interface Env {
  SESSION_DO: DurableObjectNamespace<SessionDO>; // M1.9 (D17): one object per session token
  SESSION_TTL_SECONDS?: string; // var; default 90 (SP/1)
  ANSWER_WAIT_MS?: string; // var; answer long-poll budget, default 20000 (tests shrink it)
  TURN_KEY_ID?: string; // var; empty/unset → STUN-only (local dev)
  TURN_API_TOKEN?: string; // secret: .dev.vars locally, `wrangler secret put` in prod
  POST_LIMITER?: RateLimitBinding; // absent in local/test → allow
}
