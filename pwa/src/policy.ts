// D15 external-services policy — TypeScript port of internal/policy.
// Same presets, same axes, same transparency lines. Default: none (zero contact).

export const ExternalsNone = "none";
export const ExternalsFallback = "fallback";
export const ExternalsFull = "full";

export const SignalPaste = "paste";
export const SignalWorker = "worker";
export const SignalQR = "qr";

export const PasteTimeout = 120_000; // ms (QP/1 §Sequence)

export const DefaultServerURL = "https://jsi-signal.treytorrez.workers.dev";

export type RelaySetting = "unset" | "on" | "off";

export interface Policy {
  preset: string;
  signal: string;
  escalate: boolean;
  relay: boolean;
  fetchICE: boolean;
}

export function resolvePolicy(
  preset: string,
  signalOverride: string,
  relay: RelaySetting,
): Policy {
  let p: Policy;
  switch (preset) {
    case ExternalsNone:
      p = { preset, signal: SignalPaste, escalate: false, relay: false, fetchICE: false };
      break;
    case ExternalsFallback:
      p = { preset, signal: SignalPaste, escalate: false, relay: true, fetchICE: true };
      break;
    case ExternalsFull:
      p = { preset, signal: SignalWorker, escalate: false, relay: true, fetchICE: true };
      break;
    default:
      throw new Error(`invalid externals "${preset}" (want none|fallback|full)`);
  }

  if (signalOverride) {
    if (![SignalPaste, SignalWorker, SignalQR].includes(signalOverride)) {
      throw new Error(`invalid signal "${signalOverride}" (want paste|qr|worker)`);
    }
    p.signal = signalOverride;
  }

  if (relay === "on") p.relay = true;
  if (relay === "off") p.relay = false;

  p.escalate = preset === ExternalsFallback && p.signal !== SignalWorker;
  p.fetchICE = p.fetchICE || p.relay;

  return p;
}

export function builtinSTUN(): RTCIceServer[] {
  return [
    { urls: "stun:stun.cloudflare.com:3478" },
    { urls: "stun:stun.l.google.com:19302" },
  ];
}

export function stripTURN(servers: RTCIceServer[]): RTCIceServer[] {
  return servers
    .map((s) => ({
      ...s,
      urls: (Array.isArray(s.urls) ? s.urls : [s.urls]).filter(
        (u) => !u.startsWith("turn:") && !u.startsWith("turns:"),
      ),
    }))
    .filter((s) => (Array.isArray(s.urls) ? s.urls.length > 0 : false));
}

// Summary is the D15 transparency line (PLAN.md §4).
export function summarize(p: Policy, server: string): string {
  if (p.preset === ExternalsNone && p.signal === SignalPaste && !p.relay) {
    return "mode: none — no servers will be contacted (STUN only)";
  }
  if (p.preset === ExternalsFull && p.signal === SignalWorker && p.relay) {
    return `mode: full — signaling via ${server} (TURN relay allowed)`;
  }
  if (p.escalate) {
    return `mode: fallback — offline signaling first, escalates to ${server} on failure (TURN relay allowed)`;
  }
  const sig = p.signal === SignalWorker ? `signaling via ${server}` : "offline paste signaling";
  const transport = p.relay ? "STUN + TURN relay" : "STUN only";
  return `mode: ${p.preset} (custom) — ${sig}; ${transport}`;
}
