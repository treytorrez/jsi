// Session Durable Object (M1.9, D17). One object per session — the token IS
// the object name, so both parties route to the same strongly-consistent
// store. Replaces KV (M1.2): edge-cached KV reads flapped 200↔404 under the
// sender's 1 s polling and broke SP/1's sub-90 s read-after-write handshake.
// The alarm is the only forget mechanism (D5 preserved): armed on first write,
// it wipes storage at expiry; there are no deletes on the request path.

import { DurableObject } from "cloudflare:workers";
import type { Env } from "./env";

export interface Description {
  type: "offer" | "answer";
  sdp: string;
}

export type PutAnswerResult = "created" | "nosession" | "exists";

const POLL_MS = 250; // waitAnswer re-read interval — local reads are cheap

export class SessionDO extends DurableObject<Env> {
  // false = offer already exists (token collision) → caller regenerates (D3).
  async create(offer: Description): Promise<boolean> {
    if ((await this.ctx.storage.get("offer")) !== undefined) return false;
    this.ctx.storage.put("offer", offer);
    this.ctx.storage.setAlarm(Date.now() + this.ttlMs()); // coalesced with the put
    return true;
  }

  async getOffer(): Promise<Description | null> {
    return (await this.ctx.storage.get<Description>("offer")) ?? null;
  }

  async putAnswer(answer: Description): Promise<PutAnswerResult> {
    if ((await this.ctx.storage.get("offer")) === undefined) return "nosession";
    if ((await this.ctx.storage.get("answer")) !== undefined) return "exists";
    await this.ctx.storage.put("answer", answer);
    return "created";
  }

  // Server-side long-poll: strongly-consistent local reads until the answer
  // lands or the deadline passes (null). The setTimeout yields, so a
  // concurrent putAnswer interleaves between polls.
  async waitAnswer(timeoutMs: number): Promise<Description | null> {
    const deadline = Date.now() + timeoutMs;
    let answer = await this.getAnswer();
    while (answer === null && Date.now() < deadline) {
      await new Promise((resolve) => setTimeout(resolve, POLL_MS));
      answer = await this.getAnswer();
    }
    return answer;
  }

  async hasSession(): Promise<boolean> {
    return (await this.ctx.storage.get("offer")) !== undefined;
  }

  // TTL expiry (D5): wipe the session. Alarms auto-retry on failure; deleteAll
  // is idempotent.
  async alarm(): Promise<void> {
    await this.ctx.storage.deleteAll();
  }

  private async getAnswer(): Promise<Description | null> {
    return (await this.ctx.storage.get<Description>("answer")) ?? null;
  }

  private ttlMs(): number {
    const n = Number.parseInt(this.env.SESSION_TTL_SECONDS ?? "", 10);
    return (n > 0 ? n : 90) * 1000; // SP/1 default
  }
}
