import { describe, it, expect } from "vitest";
import {
  resolvePolicy,
  summarize,
  builtinSTUN,
  stripTURN,
  ExternalsNone,
  ExternalsFallback,
  ExternalsFull,
  SignalPaste,
  SignalWorker,
  SignalQR,
} from "./policy";

describe("policy", () => {
  it("none defaults to paste, no relay", () => {
    const p = resolvePolicy(ExternalsNone, "", "unset");
    expect(p.signal).toBe(SignalPaste);
    expect(p.relay).toBe(false);
    expect(p.fetchICE).toBe(false);
    expect(p.escalate).toBe(false);
  });

  it("full defaults to worker + relay", () => {
    const p = resolvePolicy(ExternalsFull, "", "unset");
    expect(p.signal).toBe(SignalWorker);
    expect(p.relay).toBe(true);
    expect(p.fetchICE).toBe(true);
  });

  it("fallback escalates on offline failure", () => {
    const p = resolvePolicy(ExternalsFallback, "", "unset");
    expect(p.escalate).toBe(true);
    expect(p.signal).toBe(SignalPaste);
    expect(p.relay).toBe(true);
  });

  it("fallback + worker signal drops escalation", () => {
    const p = resolvePolicy(ExternalsFallback, SignalWorker, "unset");
    expect(p.escalate).toBe(false);
  });

  it("signal override works", () => {
    const p = resolvePolicy(ExternalsNone, SignalWorker, "unset");
    expect(p.signal).toBe(SignalWorker);
  });

  it("invalid preset throws", () => {
    expect(() => resolvePolicy("sometimes", "", "unset")).toThrow();
  });

  it("invalid signal throws", () => {
    expect(() => resolvePolicy(ExternalsNone, "pigeon", "unset")).toThrow();
  });

  it("none summary says STUN only", () => {
    const p = resolvePolicy(ExternalsNone, "", "unset");
    expect(summarize(p, "https://example.dev")).toContain("no servers will be contacted");
  });

  it("builtinSTUN returns cloudflare + google", () => {
    const stun = builtinSTUN();
    expect(stun).toHaveLength(2);
    expect(stun[0].urls).toContain("stun:stun.cloudflare.com:3478");
  });

  it("stripTURN removes turn/turns URLs", () => {
    const servers: RTCIceServer[] = [
      { urls: ["stun:stun.cloudflare.com:3478"] },
      { urls: ["turn:turn.cloudflare.com:3478?transport=udp", "stun:stun.cloudflare.com:3478"] },
    ];
    const stripped = stripTURN(servers);
    expect(stripped).toHaveLength(2);
    for (const s of stripped) {
      const urls = Array.isArray(s.urls) ? s.urls : [];
      for (const u of urls) {
        expect(u).not.toMatch(/^turn/);
      }
    }
  });
});
