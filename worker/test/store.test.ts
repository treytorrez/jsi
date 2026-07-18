import { describe, expect, it } from "vitest";
import { env } from "cloudflare:test";
import * as store from "../src/store";

const OFFER = { type: "offer" as const, sdp: "v=0\r\no=- 1 IN IP4 127.0.0.1\r\n" };
const ANSWER = { type: "answer" as const, sdp: "v=0\r\no=- 2 IN IP4 127.0.0.1\r\n" };

describe("store (M1.2)", () => {
  it("round-trips an offer", async () => {
    await store.putOffer(env.SESSIONS, "ABC123", OFFER, 90);
    expect(await store.getOffer(env.SESSIONS, "ABC123")).toEqual(OFFER);
  });

  it("returns null for unknown tokens", async () => {
    expect(await store.getOffer(env.SESSIONS, "ZZZZ99")).toBeNull();
    expect(await store.getAnswer(env.SESSIONS, "ZZZZ99")).toBeNull();
  });

  it("keeps offer and answer under separate keys", async () => {
    await store.putOffer(env.SESSIONS, "DEF456", OFFER, 90);
    await store.putAnswer(env.SESSIONS, "DEF456", ANSWER, 90);
    expect(await store.getOffer(env.SESSIONS, "DEF456")).toEqual(OFFER);
    expect(await store.getAnswer(env.SESSIONS, "DEF456")).toEqual(ANSWER);
  });

  it("hasSession reflects offer presence", async () => {
    expect(await store.hasSession(env.SESSIONS, "GHI789")).toBe(false);
    await store.putOffer(env.SESSIONS, "GHI789", OFFER, 90);
    expect(await store.hasSession(env.SESSIONS, "GHI789")).toBe(true);
  });

  it("passes expirationTtl and the s:/a: key scheme through to KV", async () => {
    const puts: { key: string; value: string; opts: unknown }[] = [];
    const fake = {
      put: async (key: string, value: string, opts?: unknown) => {
        puts.push({ key, value, opts });
      },
      get: async () => null,
    } as unknown as KVNamespace;

    await store.putOffer(fake, "ABC123", OFFER, 42);
    await store.putAnswer(fake, "ABC123", ANSWER, 42);

    expect(puts).toHaveLength(2);
    expect(puts[0].key).toBe("s:ABC123");
    expect(puts[1].key).toBe("a:ABC123");
    expect(puts[0].opts).toEqual({ expirationTtl: 42 });
    expect(JSON.parse(puts[0].value)).toEqual({ offer: OFFER });
    expect(JSON.parse(puts[1].value)).toEqual({ answer: ANSWER });
  });

  it("treats corrupt entries as absent on read", async () => {
    await env.SESSIONS.put("s:BADBAD", "not json");
    expect(await store.getOffer(env.SESSIONS, "BADBAD")).toBeNull();
  });
});
