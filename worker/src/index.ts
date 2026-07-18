// JSI signaling worker — SP/1 (proto/SIGNALING.md). Stateless between
// requests; KV TTL is the only forget mechanism (D5). SDP bodies are never
// logged — observability is route + status only (SP/1 §Cross-cutting).

import { Hono } from "hono";
import type { MiddlewareHandler } from "hono";
import { cors } from "hono/cors";
import type { Env } from "./env";
import { iceServers } from "./ice";
import { createSession, getSessionAnswer, getSessionOffer, postSessionAnswer } from "./sessions";

const app = new Hono<{ Bindings: Env }>();

// CORS: the protocol is public; the PWA may be hosted anywhere (SP/1).
app.use("*", cors({ origin: "*", allowMethods: ["GET", "POST", "OPTIONS"], allowHeaders: ["content-type"] }));

// M1.6: per-IP rate limiting (60 req / 60 s, key = CF-Connecting-IP). Fail-open
// when the binding is absent (local dev / tests) or errors — this is abuse
// mitigation, not accounting (SP/1 §Cross-cutting).
const limiter: MiddlewareHandler<{ Bindings: Env }> = async (c, next) => {
  const rl = c.env.POST_LIMITER;
  if (rl) {
    try {
      const { success } = await rl.limit({ key: c.req.header("CF-Connecting-IP") ?? "unknown" });
      if (!success) return c.json({ error: { code: "rate_limited", message: "too many requests" } }, 429);
    } catch {
      // binding unavailable — allow
    }
  }
  return next();
};

app.get("/health", (c) => c.json({ status: "ok" }));

app.get("/v1/ice", limiter, iceServers);
app.post("/v1/sessions", limiter, createSession);
app.get("/v1/sessions/:token/offer", getSessionOffer);
app.post("/v1/sessions/:token/answer", limiter, postSessionAnswer);
app.get("/v1/sessions/:token/answer", getSessionAnswer);

app.notFound((c) => c.json({ error: { code: "not_found", message: "unknown route" } }, 404));
app.onError((_, c) => c.json({ error: { code: "internal", message: "unexpected error" } }, 500));

export default app;
