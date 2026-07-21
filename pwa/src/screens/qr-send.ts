// QR send screen: display offer as a large QR → scan the receiver's answer QR → connect.
// PWA↔PWA only (encodes paste blob, not QP/1 binary frames).

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import QRCode from "qrcode";

import type { Policy } from "../policy";
import { summarize, builtinSTUN } from "../policy";
import { Peer } from "../rtc/peer";
import { send as transferSend, type SendFile, type TransferEvent } from "../rtc/transfer";
import { encodePayload, decodePayload } from "../signal/paste";
import { scanQR } from "../rtc/scanner";

declare global {
  interface Window { __jsiAdvanced?: { server: string; mdns: boolean; stun: boolean } }
}

type Phase = "pick" | "offer-qr" | "scanning-answer" | "connecting" | "transferring" | "done" | "error";

export function renderQRSend(
  container: HTMLElement,
  policy: Policy,
  serverURL: string,
  onBack: () => void,
): void {
  let phase: Phase = "pick";
  let selectedFiles: File[] = [];
  let peer: Peer | null = null;
  let offerBlob = "";
  let qrDataUrl = "";
  let progressText = "";
  let errorText = "";
  let scanController: AbortController | null = null;

  const rerender = () => {
    container.innerHTML = `
      <style>
        .screen { max-width: 600px; margin: 0 auto; padding: 1rem; }
        h2 { color: #64b5f6; font-size: 1.2rem; margin-bottom: 1rem; }
        .mode { color: #81c784; font-size: 0.85rem; margin-bottom: 1rem; }
        .file-pick { border: 2px dashed #455a64; border-radius: 8px; padding: 2rem; text-align: center; cursor: pointer; margin-bottom: 1rem; }
        .file-pick:hover { border-color: #64b5f6; }
        .file-list { color: #b0bec5; font-size: 0.9rem; margin-bottom: 1rem; }
        .qr-display { text-align: center; margin: 1rem 0; }
        .qr-display img { background: #fff; padding: 12px; border-radius: 12px; max-width: 400px; width: 100%; }
        .scanner-hint { color: #90a4ae; text-align: center; margin: 1rem 0; }
        .scanner-video { width: 100%; max-width: 400px; border-radius: 12px; display: block; margin: 0 auto; }
        .progress-text { color: #81c784; margin: 0.5rem 0; }
        .error { color: #ef5350; margin: 0.5rem 0; }
        .actions { display: flex; gap: 0.5rem; margin-top: 1rem; justify-content: center; }
        input[type=file] { display: none; }
      </style>
      <div class="screen">
        <h2>Send via QR</h2>
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
          <label class="file-pick" for="file-input"><div>Tap to choose a file</div></label>
          <input type="file" id="file-input" multiple />
          ${selectedFiles.length > 0 ? `<div class="file-list">${selectedFiles.map((f) => `${escapeHtml(f.name)} (${formatBytes(f.size)})`).join("<br>")}</div>` : ""}
          ${selectedFiles.length > 0 ? `<md-filled-button id="btn-offer">Show QR</md-filled-button>` : ""}
        `;
      case "offer-qr":
        return `
          <p style="text-align:center;color:#b0bec5">Show this code to the receiver:</p>
          <div class="qr-display">${qrDataUrl ? `<img src="${qrDataUrl}" alt="QR code" />` : ""}</div>
          <p class="scanner-hint">The receiver scans it, then shows you their answer code.</p>
          <div style="text-align:center">
            <md-filled-button id="btn-scan-answer">Scan answer code</md-filled-button>
          </div>
        `;
      case "scanning-answer":
        return `
          <p style="text-align:center;color:#b0bec5">Point your camera at the receiver's answer code:</p>
          <video class="scanner-video" id="scan-video" autoplay muted playsinline></video>
          <p class="scanner-hint">Scanning…</p>
        `;
      case "connecting":
        return `<p class="progress-text">Connecting…</p>`;
      case "transferring":
        return `<p class="progress-text">${escapeHtml(progressText)}</p>`;
      case "done":
        return `<p style="color:#81c784;font-size:1.1rem">Transfer complete!</p>`;
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
    container.querySelector("#btn-scan-answer")?.addEventListener("click", startScanAnswer);
  };

  async function startOffer() {
    phase = "connecting";
    progressText = "Creating offer…";
    rerender();
    try {
      const iceServers = builtinSTUN();
      peer = new Peer({ iceServers });
      const offer = await peer.createOffer();
      offerBlob = await encodePayload(offer);
      // Generate QR as a data URL (large, static — no animation needed).
      qrDataUrl = await QRCode.toDataURL(offerBlob, {
        width: 400,
        margin: 1,
        errorCorrectionLevel: "M",
        color: { dark: "#1a1a2e", light: "#ffffff" },
      });
      phase = "offer-qr";
      rerender();
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
    }
  }

  async function startScanAnswer() {
    phase = "scanning-answer";
    rerender();
    scanController = new AbortController();
    try {
      const video = container.querySelector("#scan-video") as HTMLVideoElement;
      const result = await scanQR(video, scanController.signal);
      const answer = await decodePayload(result);
      await connect(answer);
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") {
        phase = "offer-qr";
      } else {
        phase = "error";
        errorText = e instanceof Error ? e.message : String(e);
      }
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

  async function connect(answer: RTCSessionDescriptionInit) {
    phase = "connecting";
    progressText = "Connecting…";
    stopScan();
    rerender();
    try {
      await peer!.setRemote(answer);
      await peer!.waitOpen();
      const path = await peer!.connectionPath();
      phase = "transferring";
      progressText = `connected: ${path}`;
      rerender();

      const files: SendFile[] = selectedFiles.map((f) => ({
        path: "", name: f.name, size: f.size, data: f, mime: f.type,
      }));

      await transferSend(peer!.channel!, files, (ev: TransferEvent) => {
        if (ev.kind === "progress") {
          progressText = `file ${ev.fileId}: ${formatBytes(ev.bytesDone)} / ${formatBytes(ev.bytesTotal)}`;
          rerender();
        }
      }, undefined, () => peer!.drainBufferedMessages());

      phase = "done";
      rerender();
    } catch (e) {
      phase = "error";
      errorText = e instanceof Error ? e.message : String(e);
      rerender();
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
