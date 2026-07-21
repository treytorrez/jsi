// SP/1 Worker signaling client — TypeScript port of internal/signal/worker.go.
// Uses fetch() instead of net/http; same endpoints, same JSON, same error codes.
// proto/SIGNALING.md is the normative contract.

import type { Policy } from "../policy";

export interface IceServer {
  urls: string | string[];
  username?: string;
  credential?: string;
}

interface ErrorResponse {
  error: { code: string; message: string };
}

export class SignalError extends Error {
  constructor(
    public code: string,
    public httpStatus: number,
    message?: string,
  ) {
    super(message ?? code);
    this.name = "SignalError";
  }
}

export const ErrTimeout = new SignalError("timeout", 0, "signaling timed out");

export interface Session {
  token: string;
  expiresAt: Date;
}

interface SessionResponse {
  token: string;
  expiresAt: string;
}

interface Description {
  type: "offer" | "answer";
  sdp: string;
}

interface IceResponse {
  iceServers: IceServer[];
  ttl: number;
}

export class Worker {
  baseURL: string;
  pollInterval: number; // ms

  constructor(baseURL: string, pollInterval = 1000) {
    this.baseURL = baseURL.replace(/\/$/, "");
    this.pollInterval = pollInterval;
  }

  async iceServers(signal?: AbortSignal): Promise<RTCIceServer[]> {
    const resp = await fetch(`${this.baseURL}/v1/ice`, { signal });
    const body = await resp.json();
    if (!resp.ok) {
      const err = (body as ErrorResponse).error;
      throw new SignalError(err?.code ?? "internal", resp.status, err?.message);
    }
    const ice = (body as IceResponse).iceServers;
    // Filter :53 URLs (browsers block port 53, D8).
    return ice.map((s) => {
      const urls = Array.isArray(s.urls) ? s.urls : [s.urls];
      return {
        urls: urls.filter((u) => !u.includes(":53")),
        username: s.username,
        credential: s.credential,
      } as RTCIceServer;
    }).filter((s) => (Array.isArray(s.urls) ? s.urls.length > 0 : true));
  }

  async announce(
    offer: RTCSessionDescriptionInit,
    signal?: AbortSignal,
  ): Promise<{ token: string; wait: (signal?: AbortSignal) => Promise<RTCSessionDescriptionInit> }> {
    const resp = await fetch(`${this.baseURL}/v1/sessions`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ offer }),
      signal,
    });
    const body = await resp.json();
    if (!resp.ok) {
      const err = (body as ErrorResponse).error;
      throw new SignalError(err?.code ?? "internal", resp.status, err?.message);
    }
    const { token, expiresAt } = body as SessionResponse;
    const expiry = new Date(expiresAt);

    const wait = async (waitSignal?: AbortSignal): Promise<RTCSessionDescriptionInit> => {
      const deadline = expiry.getTime();
      while (Date.now() < deadline) {
        const r = await fetch(`${this.baseURL}/v1/sessions/${token}/answer`, {
          signal: waitSignal,
        });
        if (r.status === 200) {
          const b = await r.json();
          return (b as { answer: Description }).answer as RTCSessionDescriptionInit;
        }
        const b = await r.json().catch(() => ({}));
        const code = (b as ErrorResponse)?.error?.code;
        if (code === "session_not_found") {
          throw new SignalError("session_not_found", 404);
        }
        // answer_not_ready — poll again
        await new Promise((resolve) => setTimeout(resolve, this.pollInterval));
      }
      throw ErrTimeout;
    };

    return { token, wait };
  }

  async join(
    token: string,
    signal?: AbortSignal,
  ): Promise<{
    offer: RTCSessionDescriptionInit;
    respond: (answer: RTCSessionDescriptionInit, signal?: AbortSignal) => Promise<void>;
  }> {
    // Fetch the offer.
    const resp = await fetch(`${this.baseURL}/v1/sessions/${token}/offer`, { signal });
    const body = await resp.json();
    if (!resp.ok) {
      const err = (body as ErrorResponse).error;
      throw new SignalError(err?.code ?? "internal", resp.status, err?.message);
    }
    const offer = (body as { offer: Description }).offer as RTCSessionDescriptionInit;

    const respond = async (
      answer: RTCSessionDescriptionInit,
      respondSignal?: AbortSignal,
    ): Promise<void> => {
      const r = await fetch(`${this.baseURL}/v1/sessions/${token}/answer`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ answer }),
        signal: respondSignal,
      });
      if (r.status === 201) return;
      const b = await r.json().catch(() => ({}));
      const code = (b as ErrorResponse)?.error?.code;
      if (code === "answer_exists") {
        throw new SignalError("answer_exists", 409);
      }
      throw new SignalError(code ?? "internal", r.status);
    };

    return { offer, respond };
  }
}
