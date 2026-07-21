import { describe, expect, it } from "vitest";
import { env, runDurableObjectAlarm } from "cloudflare:test";
import type { Description } from "../src/sessiondo";

const OFFER: Description = { type: "offer", sdp: "v=0\r\no=- 1 IN IP4 127.0.0.1\r\n" };
const ANSWER: Description = { type: "answer", sdp: "v=0\r\no=- 2 IN IP4 127.0.0.1\r\n" };

const stub = (token: string) => env.SESSION_DO.getByName(token);

describe("SessionDO (M1.9, D17)", () => {
  it("create stores the offer and reports token collisions", async () => {
    const s = stub("ABC123");
    expect(await s.create(OFFER)).toBe(true);
    expect(await s.getOffer()).toEqual(OFFER);
    expect(await s.create(OFFER)).toBe(false); // collision → caller regenerates (D3)
    expect(await s.getOffer()).toEqual(OFFER); // first write wins
  });

  it("getOffer returns null for unknown tokens", async () => {
    expect(await stub("ZZZZ99").getOffer()).toBeNull();
  });

  it("hasSession reflects offer presence", async () => {
    const s = stub("GHI789");
    expect(await s.hasSession()).toBe(false);
    await s.create(OFFER);
    expect(await s.hasSession()).toBe(true);
  });

  it("putAnswer state machine: nosession → created → exists", async () => {
    expect(await stub("NOSE55").putAnswer(ANSWER)).toBe("nosession");

    const s = stub("DEF456");
    await s.create(OFFER);
    expect(await s.putAnswer(ANSWER)).toBe("created");
    expect(await s.putAnswer(ANSWER)).toBe("exists");
    expect(await s.getOffer()).toEqual(OFFER); // offer untouched
  });

  it("waitAnswer returns promptly once the answer lands", async () => {
    const s = stub("JKL333");
    await s.create(OFFER);
    const start = Date.now();
    const waiting = s.waitAnswer(5_000);
    await new Promise((r) => setTimeout(r, 50)); // let the long-poll start
    await s.putAnswer(ANSWER);
    expect(await waiting).toEqual(ANSWER);
    expect(Date.now() - start).toBeLessThan(2_000); // ~1 poll cycle, not the budget
  });

  it("waitAnswer returns null on timeout", async () => {
    const s = stub("MNO444");
    await s.create(OFFER);
    const start = Date.now();
    expect(await s.waitAnswer(300)).toBeNull();
    expect(Date.now() - start).toBeGreaterThanOrEqual(250);
  });

  it("waitAnswer returns instantly when the answer is already there", async () => {
    const s = stub("PQR555");
    await s.create(OFFER);
    await s.putAnswer(ANSWER);
    const start = Date.now();
    expect(await s.waitAnswer(5_000)).toEqual(ANSWER);
    expect(Date.now() - start).toBeLessThan(250);
  });

  it("alarm purges storage (TTL is the forget mechanism, D5)", async () => {
    const s = stub("STU666");
    await s.create(OFFER);
    await s.putAnswer(ANSWER);
    expect(await runDurableObjectAlarm(s)).toBe(true); // armed by create()
    expect(await s.hasSession()).toBe(false);
    expect(await s.getOffer()).toBeNull();
    expect(await s.waitAnswer(50)).toBeNull();
    expect(await runDurableObjectAlarm(s)).toBe(false); // nothing scheduled anymore
  });
});
