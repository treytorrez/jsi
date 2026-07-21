// QR receive screen: scan the sender's offer QR → display answer QR → connect → transfer.
// PWA↔PWA only (encodes paste blob, not QP/1 binary frames).

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import QRCode from "qrcode";

import type { Policy } from "../policy";
import { summarize, builtinSTUN, stripTURN, DefaultServerURL } from "../policy";
import { Peer } from "../rtc/peer";
import { receive as transferReceive, type TransferEvent } from "../rtc/transfer";
import { encodePayload, decodePayload } from "../signal/paste";
import { scanQR } from "../rtc/scanner";
import { Worker } from "../signal/worker";

declare global {
  interface Window { __jsiAdvanced?: { server: string; mdns: boolean; stun: boolean } }
}

type Phase = "scanning-offer" | "answer-qr" | "connecting" | "transferring" | "done" | "error";

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

export function renderQRReceive(
  container: HTMLElement,
  policy: Policy,
  serverURL: string,
  onBack: () => void,
): void {
  let phase: Phase = "scanning-offer";
  let peer: Peer | null = null;
  let answerBlob = "";
  let qrDataUrl = "";
  let progressText = "";
  let errorText = "";
  let scanController: AbortController | null = null;
  let savedFiles: { name: string; size: number; sha256: string; blob: Blob }[] = [];

  const rerender = () => {
    container.innerHTML = `
      <style>
        .screen { max-width: 600px; margin: 0 auto; padding: 1rem; }
        h2 { color: #64b5f6; font-size: 1.2rem; margin-bottom: 1rem; }
        .mode { color: #81c784; font-size: 0.85rem; margin-bottom: 1rem; }
        .scanner-hint { color: #90a4ae; text-align: center; margin: 1rem 0; }
        .scanner-video { width: 100%; max-width: 400px; border-radius: 12px; display: block; margin: 0 auto; }
        .qr-display { text-align: center; margin: 1rem 0; }
        .qr-display img { background: #fff; padding: 12px; border-radius: 12px; max-width: 400px; width: 100%; }
        .progress-text { color: #81c784; margin: 0.5rem 0; }
        .error { color: #ef5350; margin: 0.5rem 0; }
        .saved { color: #b0bec5; font-size: 0.9rem; margin-top: 0.5rem; }
        .actions { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: center; }
      </style>
      <div class="screen">
        <h2>Receive via QR</h2>
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
      case "scanning-offer":
        return `
          <p style="text-align:center;color:#b0bec5">Point your camera at the sender's QR code:</p>
          <video class="scanner-video" id="scan-video" autoplay muted playsinline></video>
          <p class="scanner-hint">Scanning…</p>
        `;
      case "answer-qr":
        return `
          <p style="text-align:center;color:#b0bec5">Show this code to the sender:</p>
          <div class="qr-display">${qrDataUrl ? `<img src="${qrDataUrl}" alt="QR code" />` : ""}</div>
          <p class="scanner-hint">The sender scans this, then the transfer starts automatically.</p>
        `;
      case "connecting":
        return `<p class="progress-text">Connecting…</p>`;
      case "transferring":
        return `<p class="progress-text">${escapeHtml(progressText)}</p>`;
      case "done":
        return `
          <p style="color:#81c784;font-size:1.1rem">Transfer complete!</p>
          <div class="saved">
            ${savedFiles.map((f) => `${escapeHtml(f.name)} (${formatBytes(f.size)})`).join("<br>")}
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
      stopScan();
      peer?.close();
      onBack();
    });
    container.querySelector("#btn-save")?.addEventListener("click", saveFiles);
  };

  // Start scanning immediately on render.
  if (phase === "scanning-offer") {
    setTimeout(startScanOffer, 100);
  }

  async function startScanOffer() {
    scanController = new AbortController();
    try {
      const video = container.querySelector("#scan-video") as HTMLVideoElement;
      const result = await scanQR(video, scanController.signal);
      const offer = await decodePayload(result);
      await createAnswerAndDisplay(offer);
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") return;
      phase = "error";
      errorText = formatError(e, policy);
      rerender();
    }
  }

  async function createAnswerAndDisplay(offer: RTCSessionDescriptionInit) {
    phase = "connecting";
    progressText = "Creating answer…";
    stopScan();
    rerender();
    try {
      const iceServers = await getICEServers(policy, serverURL);
      peer = new Peer({ iceServers });
      const answer = await peer.createAnswer(offer);
      answerBlob = await encodePayload(answer);
      qrDataUrl = await QRCode.toDataURL(answerBlob, {
        width: 400,
        margin: 1,
        errorCorrectionLevel: "M",
        color: { dark: "#1a1a2e", light: "#ffffff" },
      });
      phase = "answer-qr";
      rerender();
      // The sender will scan this QR, then connect. We wait for the channel
      // to open (the sender calls setRemote + waitOpen on their side).
      await peer.waitOpen();
      const path = await peer.connectionPath();
      phase = "transferring";
      progressText = `connected: ${path}`;
      rerender();

      const results = await transferReceive(peer.channel!, (ev: TransferEvent) => {
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
      errorText = formatError(e, policy);
      rerender();
    }
  }

  function stopScan() {
    if (scanController) {
      scanController.abort();
      scanController = null;
    }
    const video = container.querySelector("#scan-video") as HTMLVideoElement | null;
    if (video?.srcObject) {
      (video.srcObject as MediaStream).getTracks().forEach((t) => t.stop());
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

function formatError(e: unknown, policy: Policy): string {
  const msg = e instanceof Error ? e.message : String(e);
  if (!policy.fetchICE && (msg.includes("ICE") || msg.includes("connection") || msg.includes("failed"))) {
    return `${msg}\n\nNo servers were contacted (No Servers mode). If both devices are on a network that blocks direct connections (e.g., Wi-Fi with AP isolation), try again with Best Effort mode — it uses a TURN relay as a fallback without storing your data.`;
  }
  return msg;
}
