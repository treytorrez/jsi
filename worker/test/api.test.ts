import { afterEach, describe, expect, it, vi } from "vitest";
import { createExecutionContext, env, waitOnExecutionContext } from "cloudflare:test";
import app from "../src";
import type { Env } from "../src/env";

const OFFER = { type: "offer", sdp: "v=0\r\no=- 1 IN IP4 127.0.0.1\r\na=fingerprint:sha-256 AA\r\n" };
const ANSWER = { type: "answer", sdp: "v=0\r\no=- 2 IN IP4 127.0.0.1\r\na=fingerprint:sha-256 BB\r\n" };

async function call(req: Request, e: Env = env): Promise<Response> {
  const ctx = createExecutionContext();
  const res = await app.fetch(req, e, ctx);
  await waitOnExecutionContext(ctx);
  return res;
}

const get = (path: string, e?: Env) => call(new Request(`https://test${path}`), e);

const post = (path: string, body: unknown, e?: Env, headers: Record<string, string> = {}) =>
  call(
    new Request(`https://test${path}`, {
      method: "POST",
      headers: { "content-type": "application/json", ...headers },
      body: typeof body === "string" ? body : JSON.stringify(body),
    }),
    e,
  );

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const json = (r: Response): Promise<any> => r.json();

async function createSession(offer = OFFER, e?: Env): Promise<string> {
  const res = await post("/v1/sessions", { offer }, e);
  expect(res.status).toBe(201);
  return (await json(res)).token;
}

describe("SP/1 routes (M1.4–M1.6)", () => {
  it("GET /health → 200", async () => {
    const res = await get("/health");
    expect(res.status).toBe(200);
    expect(await json(res)).toEqual({ status: "ok" });
  });

  it("full handshake: ice → create → get offer → post answer → poll answer", async () => {
    // (1) both sides GET /v1/ice — STUN-only without TURN config
    const ice = await get("/v1/ice");
    expect(ice.status).toBe(200);
    const iceBody = await json(ice);
    expect(iceBody.ttl).toBe(3600);
    expect(iceBody.iceServers).toContainEqual({ urls: ["stun:stun.cloudflare.com:3478"] });
    expect(iceBody.iceServers).toContainEqual({ urls: ["stun:stun.l.google.com:19302"] });

    // (2) sender POSTs the offer → 201 {token, expiresAt}
    const created = await post("/v1/sessions", { offer: OFFER });
    expect(created.status).toBe(201);
    const { token, expiresAt } = await json(created);
    expect(token).toMatch(/^[0-9A-HJKMNP-TV-Z]{6}$/);
    expect(new Date(expiresAt).getTime()).toBeGreaterThan(Date.now());

    // (3) receiver GETs the offer
    const got = await get(`/v1/sessions/${token}/offer`);
    expect(got.status).toBe(200);
    expect((await json(got)).offer).toEqual(OFFER);

    // (4) receiver POSTs the answer → 201 {}
    const answered = await post(`/v1/sessions/${token}/answer`, { answer: ANSWER });
    expect(answered.status).toBe(201);
    expect(await json(answered)).toEqual({});

    // (5) sender polls the answer
    const polled = await get(`/v1/sessions/${token}/answer`);
    expect(polled.status).toBe(200);
    expect((await json(polled)).answer).toEqual(ANSWER);
  });

  it("accepts lowercase/mixed-case tokens (case-insensitive input, D4)", async () => {
    const token = await createSession();
    const res = await get(`/v1/sessions/${token.toLowerCase()}/offer`);
    expect(res.status).toBe(200);
    expect((await json(res)).offer).toEqual(OFFER);
  });

  it("404 session_not_found for unknown token on all three lookups", async () => {
    for (const res of [
      await get("/v1/sessions/ZZZZ99/offer"),
      await post("/v1/sessions/ZZZZ99/answer", { answer: ANSWER }),
      await get("/v1/sessions/ZZZZ99/answer"),
    ]) {
      expect(res.status).toBe(404);
      expect((await json(res)).error.code).toBe("session_not_found");
    }
  });

  it("404 answer_not_ready while the answer is not posted yet", async () => {
    const token = await createSession();
    const res = await get(`/v1/sessions/${token}/answer`);
    expect(res.status).toBe(404);
    expect((await json(res)).error.code).toBe("answer_not_ready");
  });

  it("409 answer_exists on a duplicate answer", async () => {
    const token = await createSession();
    expect((await post(`/v1/sessions/${token}/answer`, { answer: ANSWER })).status).toBe(201);
    const dup = await post(`/v1/sessions/${token}/answer`, { answer: ANSWER });
    expect(dup.status).toBe(409);
    expect((await json(dup)).error.code).toBe("answer_exists");
  });

  it("400 invalid_token for malformed tokens", async () => {
    for (const bad of ["ABCDE", "ABCDEFG", "IIIIII", "OOOOOO", "UUUUUU", "AB CD3", "AB-CD3"]) {
      const res = await get(`/v1/sessions/${bad}/offer`);
      expect(res.status, bad).toBe(400);
      expect((await json(res)).error.code).toBe("invalid_token");
    }
  });

  it("400 invalid_body for malformed creates", async () => {
    const cases: unknown[] = [
      "not json at all{",
      {},
      { offer: null },
      { offer: { type: "answer", sdp: "v=0" } },
      { offer: { type: "offer" } },
      { offer: { type: "offer", sdp: 42 } },
      { offer: { type: "offer", sdp: "x".repeat(32 * 1024 + 1) } },
      ["array"],
    ];
    for (const body of cases) {
      const res = await post("/v1/sessions", body);
      expect(res.status, JSON.stringify(body).slice(0, 40)).toBe(400);
      expect((await json(res)).error.code).toBe("invalid_body");
    }
  });

  it("400 invalid_body for malformed answers", async () => {
    const token = await createSession();
    for (const body of [{ answer: { type: "offer", sdp: "v=0" } }, { answer: { sdp: "v=0" } }, {}]) {
      const res = await post(`/v1/sessions/${token}/answer`, body);
      expect(res.status, JSON.stringify(body)).toBe(400);
      expect((await json(res)).error.code).toBe("invalid_body");
    }
  });

  it("accepts an sdp of exactly 32 KiB", async () => {
    const res = await post("/v1/sessions", { offer: { type: "offer", sdp: "x".repeat(32 * 1024) } });
    expect(res.status).toBe(201);
  });

  it("429 rate_limited when POST_LIMITER denies, keyed by CF-Connecting-IP", async () => {
    const limit = vi.fn(async (_opts: { key: string }) => ({ success: false }));
    const limited: Env = { ...env, POST_LIMITER: { limit } };
    const res = await post("/v1/sessions", { offer: OFFER }, limited, { "CF-Connecting-IP": "203.0.113.7" });
    expect(res.status).toBe(429);
    expect((await json(res)).error.code).toBe("rate_limited");
    expect(limit).toHaveBeenCalledWith({ key: "203.0.113.7" });

    const ice = await get("/v1/ice", limited);
    expect(ice.status).toBe(429);
  });

  it("allows requests when POST_LIMITER is undefined (local/test)", async () => {
    const noLimiter: Env = { ...env, POST_LIMITER: undefined };
    expect((await post("/v1/sessions", { offer: OFFER }, noLimiter)).status).toBe(201);
    expect((await get("/v1/ice", noLimiter)).status).toBe(200);
  });

  it("CORS: preflight and responses carry Access-Control-Allow-Origin: *", async () => {
    const preflight = await call(
      new Request("https://test/v1/sessions", {
        method: "OPTIONS",
        headers: { origin: "https://pwa.example", "access-control-request-method": "POST" },
      }),
    );
    expect(preflight.headers.get("access-control-allow-origin")).toBe("*");
    expect(preflight.headers.get("access-control-allow-methods")).toContain("POST");

    const res = await post("/v1/sessions", { offer: OFFER }, undefined, { origin: "https://pwa.example" });
    expect(res.headers.get("access-control-allow-origin")).toBe("*");
  });

  it("entries expire after SESSION_TTL_SECONDS (TTL is the forget mechanism, D5)", async () => {
    // Real KV enforces a 60 s minimum TTL, so expiry is exercised against a
    // TTL-honoring stub; the store unit test asserts expirationTtl reaches KV.
    const map = new Map<string, { value: string; expiresAt: number }>();
    const kv = {
      async get(key: string) {
        const e = map.get(key);
        if (!e) return null;
        if (Date.now() >= e.expiresAt) {
          map.delete(key);
          return null;
        }
        return e.value;
      },
      async put(key: string, value: string, opts?: { expirationTtl?: number }) {
        map.set(key, { value, expiresAt: opts?.expirationTtl ? Date.now() + opts.expirationTtl * 1000 : Infinity });
      },
    } as unknown as KVNamespace;
    const shortLived: Env = { ...env, SESSIONS: kv, SESSION_TTL_SECONDS: "1" };

    const token = await createSession(OFFER, shortLived);
    expect((await get(`/v1/sessions/${token}/offer`, shortLived)).status).toBe(200);
    await new Promise((r) => setTimeout(r, 1100));
    const res = await get(`/v1/sessions/${token}/offer`, shortLived);
    expect(res.status).toBe(404);
    expect((await json(res)).error.code).toBe("session_not_found");
  });
});

describe("GET /v1/ice TURN minting (M1.3, D7/D8)", () => {
  afterEach(() => vi.unstubAllGlobals());

  const turnEnv: Env = { ...env, TURN_KEY_ID: "key123", TURN_API_TOKEN: "sekrit" };
  const CF_RESPONSE = {
    iceServers: [
      { urls: ["stun:stun.cloudflare.com:3478"] },
      {
        urls: [
          "turn:turn.cloudflare.com:3478?transport=udp",
          "turn:turn.cloudflare.com:3478?transport=tcp",
          "turns:turn.cloudflare.com:5349?transport=tcp",
          "turn:turn.cloudflare.com:53?transport=udp",
        ],
        username: "abc123",
        credential: "def456",
      },
    ],
  };

  it("passes CF's iceServers through untouched (:53 kept, D8) and appends Google STUN", async () => {
    const spy = vi.fn(async () => new Response(JSON.stringify(CF_RESPONSE), { status: 200 }));
    vi.stubGlobal("fetch", spy);

    const res = await get("/v1/ice", turnEnv);
    expect(res.status).toBe(200);
    const body = await json(res);
    expect(body.ttl).toBe(3600);
    expect(body.iceServers).toEqual([...CF_RESPONSE.iceServers, { urls: ["stun:stun.l.google.com:19302"] }]);

    // minting call shape (D7)
    expect(spy).toHaveBeenCalledTimes(1);
    const [url, init] = spy.mock.calls[0] as unknown as [string, { method: string; headers: Record<string, string>; body: string }];
    expect(url).toBe("https://rtc.live.cloudflare.com/v1/turn/keys/key123/credentials/generate-ice-servers");
    expect(init.method).toBe("POST");
    expect(init.headers.authorization).toBe("Bearer sekrit");
    expect(JSON.parse(init.body)).toEqual({ ttl: 3600 });
  });

  it("500 internal when the CF API fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("denied", { status: 403 })));
    const res = await get("/v1/ice", turnEnv);
    expect(res.status).toBe(500);
    expect((await json(res)).error.code).toBe("internal");
  });

  it("500 internal when fetch throws", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new Error("network down"))));
    const res = await get("/v1/ice", turnEnv);
    expect(res.status).toBe(500);
    expect((await json(res)).error.code).toBe("internal");
  });

  it("500 internal when the CF API returns a bad shape", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response("{}", { status: 200 })));
    const res = await get("/v1/ice", turnEnv);
    expect(res.status).toBe(500);
    expect((await json(res)).error.code).toBe("internal");
  });
});
