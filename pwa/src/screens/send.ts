// Send flow screen: pick file → offer → signaling (paste/worker) → connect → transfer → done.
// Replaces the alert stub. Drives rtc/peer + rtc/transfer + signal/{worker,paste}.

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";
import "@material/web/progress/linear-progress.js";

import type { Policy } from "../policy";
import { SignalPaste, SignalWorker, summarize, builtinSTUN, stripTURN } from "../policy";
import { Peer } from "../rtc/peer";
import { send as transferSend, type SendFile, type TransferEvent } from "../rtc/transfer";
import { Worker } from "../signal/worker";
import { encodePayload, decodePayload } from "../signal/paste";

type Phase = "pick" | "offer" | "answer" | "connecting" | "transferring" | "done" | "error";

export function renderSend(
  container: HTMLElement,
  policy: Policy,
  serverURL: string,
  onBack: () => void,
): void {
  let phase: Phase = "pick";
  let selectedFiles: File[] = [];
  let peer: Peer | null = null;
  let offerSDP: RTCSessionDescriptionInit | null = null;
  let displayText = "";
  let token = "";
  let progressText = "";
  let errorText = "";

  const rerender = () => {
    container.innerHTML = `
      <style>
        .screen { max-width: 600px; margin: 0 auto; padding: 1rem; }
        h2 { color: #64b5f6; font-size: 1.2rem; margin-bottom: 1rem; }
        .file-pick { border: 2px dashed #455a64; border-radius: 8px; padding: 2rem; text-align: center; cursor: pointer; margin-bottom: 1rem; }
        .file-pick:hover { border-color: #64b5f6; }
        .file-list { color: #b0bec5; font-size: 0.9rem; margin-bottom: 1rem; }
        .blob-box { background: #0d1117; border: 1px solid #304050; border-radius: 8px; padding: 1rem;
                    word-break: break-all; font-family: monospace; font-size: 0.8rem; color: #c8e6c9;
                    max-height: 150px; overflow-y: auto; margin-bottom: 1rem; }
        .token-display { font-size: 2rem; font-weight: bold; color: #fff176; text-align: center; padding: 1rem; }
        .progress-text { color: #81c784; margin: 0.5rem 0; }
        .error { color: #ef5350; margin: 0.5rem 0; }
        .actions { display: flex; gap: 0.5rem; margin-top: 1rem; }
        .mode { color: #81c784; font-size: 0.85rem; margin-bottom: 1rem; }
        input[type=file] { display: none; }
      </style>
      <div class="screen">
        <h2>Send ${selectedFiles.length > 0 ? `(${selectedFiles.length} file${selectedFiles.length > 1 ? "s" : ""})` : ""}</h2>
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
      case "pick":
        return `
          <label class="file-pick" for="file-input">
            <div>Tap to choose a file</div>
          </label>
          <input type="file" id="file-input" multiple />
          ${selectedFiles.length > 0 ? `<div class="file-list">${selectedFiles.map((f) => `${escapeHtml(f.name)} (${formatBytes(f.size)})`).join("<br>")}</div>` : ""}
          ${selectedFiles.length > 0 ? `<md-filled-button id="btn-offer">Create offer</md-filled-button>` : ""}
        `;
      case "offer":
        return `
          <p>Share this with the receiver:</p>
          ${token ? `<div class="token-display">${token}</div><p style="text-align:center;color:#90a4ae">waiting for receiver…</p>` : ""}
          ${displayText ? `<div class="blob-box" id="offer-blob">${escapeHtml(displayText)}</div><div style="display:flex;gap:0.5rem"><md-filled-button id="btn-copy">Copy to clipboard</md-filled-button><md-outlined-button id="btn-copy-fallback">Select all</md-outlined-button></div><p style="color:#90a4ae;margin-top:0.5rem">Copy this blob and send it to the receiver via any channel.</p>` : ""}
        `;
      case "answer":
        return `
          <p>Paste the receiver's answer blob:</p>
          <textarea id="answer-input" placeholder="jsi1:..." rows="4"
            style="width:100%;background:#0d1117;border:1px solid #304050;border-radius:8px;
            color:#e0e0e0;padding:0.75rem;box-sizing:border-box;font-family:monospace;font-size:0.85rem"></textarea>
          <md-filled-button id="btn-connect" style="margin-top:1rem">Connect</md-filled-button>
        `;
      case "connecting":
        return `<p class="progress-text">Connecting…</p>`;
      case "transferring":
        return `<p class="progress-text">${escapeHtml(progressText)}</p>`;
      case "done":
        return `<p style="color:#81c784;font-size:1.1rem">Transfer complete!</p><p class="progress-text">${escapeHtml(progressText)}</p>`;
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

    const fileInput = container.querySelector("#file-input");
    if (fileInput) {
      fileInput.addEventListener("change", (e) => {
        const target = e.target as HTMLInputElement;
        if (target.files && target.files.length > 0) {
          selectedFiles = Array.from(target.files);
          rerender();
        }
      });
    }

    container.querySelector("#btn-offer")?.addEventListener("click", startOffer);
    container.querySelector("#btn-connect")?.addEventListener("click", connectWithAnswer);
    container.querySelector("#btn-copy")?.addEventListener("click", () => copyToClipboard(displayText, container));
    container.querySelector("#btn-copy-fallback")?.addEventListener("click", () => selectElement(container, "#offer-blob"));
  };

  async function startOffer() {
    phase = "connecting";
    progressText = "Creating offer…";
    rerender();
    try {
      const iceServers = await getICEServers(policy, serverURL);
      peer = new Peer({ iceServers });
      offerSDP = await peer.createOffer();

      if (policy.signal === SignalWorker) {
        const w = new Worker(serverURL);
        const result = await w.announce(offerSDP);
        token = result.token;
        phase = "offer";
        rerender();
        // Poll for the answer in the background.
        const answer = await result.wait();
        await connect(answer);
      } else {
        // Paste mode: display the blob and wait for the user to paste the answer.
        displayText = await encodePayload(offerSDP);
        phase = "answer";
        rerender();
      }
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
    }
  }

  async function connectWithAnswer() {
    const input = container.querySelector("#answer-input") as HTMLTextAreaElement | null;
    if (!input) return;
    const value = input?.value?.trim();
    if (!value || !offerSDP) return;
    try {
      const answer = await decodePayload(value);
      await connect(answer);
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
    }
  }

  async function connect(answer: RTCSessionDescriptionInit) {
    phase = "connecting";
    progressText = "Connecting…";
    rerender();
    try {
      await peer!.setRemote(answer);
      await peer!.waitOpen();
      const path = await peer!.connectionPath();
      phase = "transferring";
      progressText = `connected: ${path}`;
      rerender();

      const files: SendFile[] = selectedFiles.map((f, i) => ({
        path: "",
        name: f.name,
        size: f.size,
        data: f,
        mime: f.type,
      }));

      await transferSend(peer!.channel!, files, (ev: TransferEvent) => {
        if (ev.kind === "progress") {
          progressText = `file ${ev.fileId}: ${formatBytes(ev.bytesDone)} / ${formatBytes(ev.bytesTotal)}`;
          rerender();
        } else if (ev.kind === "file-done") {
          progressText = `file ${ev.fileId} done`;
          rerender();
        }
      });

      phase = "done";
      progressText = `Sent ${selectedFiles.length} file(s)`;
      rerender();
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
    }
  }

  rerender();
}

// --- Shared helpers ---

async function getICEServers(policy: Policy, serverURL: string): Promise<RTCIceServer[]> {
  if (!policy.fetchICE) {
    return builtinSTUN();
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
    // Fallback: select the blob text so the user can Ctrl+C manually.
    selectElement(container, "#offer-blob");
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
