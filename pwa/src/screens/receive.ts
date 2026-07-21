// Receive flow screen: pick destination → signaling (paste/worker) → answer → connect → transfer → save.
// Drives rtc/peer + rtc/transfer + signal/{worker,paste}.

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import type { Policy } from "../policy";
import { SignalPaste, SignalWorker, summarize, builtinSTUN, stripTURN } from "../policy";
import { Peer } from "../rtc/peer";
import { receive as transferReceive, type TransferEvent } from "../rtc/transfer";
import { Worker } from "../signal/worker";
import { encodePayload, decodePayload } from "../signal/paste";

declare global {
  interface Window { __jsiAdvanced?: { server: string; mdns: boolean; stun: boolean } }
}

type Phase = "input" | "answer" | "connecting" | "transferring" | "done" | "error";

export function renderReceive(
  container: HTMLElement,
  policy: Policy,
  serverURL: string,
  onBack: () => void,
  prefillToken: string | null = null,
): void {
  let phase: Phase = "input";
  let peer: Peer | null = null;
  let answerSDP: RTCSessionDescriptionInit | null = null;
  let displayText = "";
  let progressText = "";
  let errorText = "";
  let savedFiles: { name: string; size: number; sha256: string; blob: Blob }[] = [];

  const rerender = () => {
    container.innerHTML = `
      <style>
        .screen { max-width: 600px; margin: 0 auto; padding: 1rem; }
        h2 { color: #64b5f6; font-size: 1.2rem; margin-bottom: 1rem; }
        .blob-box { background: #0d1117; border: 1px solid #304050; border-radius: 8px; padding: 1rem;
                    word-break: break-all; font-family: monospace; font-size: 0.8rem; color: #c8e6c9;
                    max-height: 150px; overflow-y: auto; margin-bottom: 1rem; }
        .progress-text { color: #81c784; margin: 0.5rem 0; }
        .error { color: #ef5350; margin: 0.5rem 0; }
        .actions { display: flex; gap: 0.5rem; margin-top: 1rem; }
        .mode { color: #81c784; font-size: 0.85rem; margin-bottom: 1rem; }
        .saved { color: #b0bec5; font-size: 0.9rem; margin-top: 0.5rem; }
      </style>
      <div class="screen">
        <h2>Receive</h2>
        <p class="mode">${summarize(policy, serverURL)}</p>
        ${renderPhase()}
        <div class="actions">
          <md-outlined-button id="btn-back">Back</md-outlined-button>
        </div>
      </div>
    `;
    attachListeners();
  };

  const renderPhase = (): string => {
    switch (phase) {
      case "input":
        return policy.signal === SignalWorker
          ? `
            <p>Enter the sender's token:</p>
            <input type="text" id="token-input" placeholder="6-character token" maxlength="6"
              value="${prefillToken ?? ""}"
              style="width:100%;font-size:1.5rem;text-align:center;text-transform:uppercase;
              background:#0d1117;border:1px solid #304050;border-radius:8px;color:#e0e0e0;
              padding:0.75rem;box-sizing:border-box" />
            <md-filled-button id="btn-join" style="margin-top:1rem">Connect</md-filled-button>
          `
          : `
            <p>Paste the sender's offer blob:</p>
            <textarea id="offer-input" placeholder="jsi1:..." rows="4"
              style="width:100%;background:#0d1117;border:1px solid #304050;border-radius:8px;
              color:#e0e0e0;padding:0.75rem;box-sizing:border-box;font-family:monospace;font-size:0.85rem"></textarea>
            <md-filled-button id="btn-join" style="margin-top:1rem">Connect</md-filled-button>
          `;
      case "answer":
        return `
          <p>Send this blob back to the sender:</p>
          <div class="blob-box" id="answer-blob">${escapeHtml(displayText)}</div>
          <div style="display:flex;gap:0.5rem">
            <md-filled-button id="btn-copy">Copy to clipboard</md-filled-button>
            <md-outlined-button id="btn-copy-fallback">Select all</md-outlined-button>
          </div>
          <md-filled-button id="btn-connect" style="margin-top:1rem">Connected — start transfer</md-filled-button>
        `;
      case "connecting":
        return `<p class="progress-text">Connecting…</p>`;
      case "transferring":
        return `<p class="progress-text">${escapeHtml(progressText)}</p>`;
      case "done":
        return `
          <p style="color:#81c784;font-size:1.1rem">Transfer complete!</p>
          <div class="saved">
            ${savedFiles.map((f) => `${escapeHtml(f.name)} (${formatBytes(f.size)}) — sha256: ${f.sha256.slice(0, 16)}…`).join("<br>")}
          </div>
          <md-filled-button id="btn-save" style="margin-top:1rem">Save files</md-filled-button>
        `;
      case "error":
        return `<p class="error">${escapeHtml(errorText)}</p>`;
    }
    return "";
  };

  const attachListeners = () => {
    container.querySelector("#btn-back")?.addEventListener("click", () => {
      peer?.close();
      onBack();
    });
    container.querySelector("#btn-join")?.addEventListener("click", startJoin);
    container.querySelector("#btn-connect")?.addEventListener("click", waitForTransfer);
    container.querySelector("#btn-save")?.addEventListener("click", saveFiles);
    container.querySelector("#btn-copy")?.addEventListener("click", () => copyToClipboard(displayText, container));
    container.querySelector("#btn-copy-fallback")?.addEventListener("click", () => selectElement(container, "#answer-blob"));
    // Enter (without Shift) in the offer textarea submits.
    const offerInput = container.querySelector("#offer-input") as HTMLElement | null;
    offerInput?.addEventListener("keydown", (e: Event) => {
      const ke = e as KeyboardEvent;
      if (ke.key === "Enter" && !ke.shiftKey) {
        ke.preventDefault();
        startJoin();
      }
    });
    // Enter in the token input submits.
    const tokenInput = container.querySelector("#token-input") as HTMLElement | null;
    tokenInput?.addEventListener("keydown", (e: Event) => {
      const ke = e as KeyboardEvent;
      if (ke.key === "Enter") {
        ke.preventDefault();
        startJoin();
      }
    });
  };

  async function startJoin() {
    // Read input values BEFORE rerendering (rerender destroys the DOM elements).
    let offer: RTCSessionDescriptionInit;
    let respond: ((answer: RTCSessionDescriptionInit) => Promise<void>) | null = null;

    if (policy.signal === SignalWorker) {
      const input = container.querySelector("#token-input") as HTMLInputElement | null;
      const token = input?.value?.trim().toUpperCase();
      if (!token) {
        phase = "error";
        errorText = "Enter a token";
        rerender();
        return;
      }
      phase = "connecting";
      progressText = "Joining session…";
      rerender();
      try {
        const w = new Worker(serverURL);
        const result = await w.join(token);
        offer = result.offer;
        respond = result.respond;
      } catch (e) {
        phase = "error";
        errorText = e instanceof Error ? e.message : String(e);
        rerender();
        return;
      }
    } else {
      const input = container.querySelector("#offer-input") as HTMLTextAreaElement | null;
      const blob = input?.value?.trim();
      if (!blob) {
        phase = "error";
        errorText = "Paste the offer blob";
        rerender();
        return;
      }
      phase = "connecting";
      progressText = "Creating answer…";
      rerender();
      try {
        offer = await decodePayload(blob);
      } catch (e) {
        phase = "error";
        errorText = e instanceof Error ? e.message : String(e);
        rerender();
        return;
      }
    }

    try {
      const iceServers = await getICEServers(policy, serverURL);
      peer = new Peer({ iceServers });
      answerSDP = await peer.createAnswer(offer);

      if (respond) {
        await respond(answerSDP);
        // Worker mode: skip the answer-display phase.
        await waitForConnection();
      } else {
        // Paste mode: display the answer blob for the user to send back.
        displayText = await encodePayload(answerSDP);
        phase = "answer";
        rerender();
      }
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
    }
  }

  async function waitForTransfer() {
    if (!answerSDP || !peer) return;
    await waitForConnection();
  }

  async function waitForConnection() {
    phase = "connecting";
    progressText = "Connecting…";
    rerender();
    try {
      await peer!.waitOpen();
      const path = await peer!.connectionPath();
      phase = "transferring";
      progressText = `connected: ${path}`;
      rerender();

      const results = await transferReceive(peer!.channel!, (ev: TransferEvent) => {
        if (ev.kind === "progress") {
          progressText = `file ${ev.fileId}: ${formatBytes(ev.bytesDone)} / ${formatBytes(ev.bytesTotal)}`;
          rerender();
        }
      }, undefined, () => peer!.drainBufferedMessages());

      savedFiles = results;
      phase = "done";
      progressText = `Received ${results.length} file(s)`;
      rerender();
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
    }
  }

  function saveFiles() {
    for (const f of savedFiles) {
      const url = URL.createObjectURL(f.blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = f.name;
      a.click();
      URL.revokeObjectURL(url);
    }
  }

  rerender();
}

async function getICEServers(policy: Policy, serverURL: string): Promise<RTCIceServer[]> {
  const adv = window.__jsiAdvanced ?? { server: serverURL, mdns: true, stun: true };
  if (!policy.fetchICE) {
    return adv.stun ? builtinSTUN() : [];
  }
  const w = new Worker(serverURL);
  let servers = await w.iceServers();
  if (!policy.relay) {
    servers = stripTURN(servers);
  }
  return servers;
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    c === "&" ? "&amp;" : c === "<" ? "&lt;" : c === ">" ? "&gt;" : c === '"' ? "&quot;" : "&#39;",
  );
}

async function copyToClipboard(text: string, container: HTMLElement): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    const btn = container.querySelector("#btn-copy");
    if (btn) {
      const original = btn.textContent;
      btn.textContent = "Copied!";
      setTimeout(() => { btn.textContent = original; }, 2000);
    }
  } catch {
    selectElement(container, "#answer-blob");
  }
}

function selectElement(container: HTMLElement, selector: string): void {
  const el = container.querySelector(selector) as HTMLElement | null;
  if (!el) return;
  const range = document.createRange();
  range.selectNodeContents(el);
  const sel = window.getSelection();
  sel?.removeAllRanges();
  sel?.addRange(range);
}
