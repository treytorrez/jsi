// Action screen (S4 redesign): Send/Receive buttons + collapsible Advanced
// section (server URL, mDNS toggle, STUN toggle).

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import { summarize, type Policy } from "../policy";

export interface AdvancedSettings {
  server: string;
  mdns: boolean;
  stun: boolean;
}

export function renderAction(
  container: HTMLElement,
  policy: Policy,
  server: string,
  onSend: () => void,
  onReceive: () => void,
  onBack: () => void,
  onQRSend?: () => void,
  onQRReceive?: () => void,
  onAdvancedChange?: (s: AdvancedSettings) => void,
): void {
  let advanced: AdvancedSettings = { server, mdns: true, stun: true };

  const rerender = () => {
    container.innerHTML = `
      <style>
        .screen { max-width: 600px; margin: 0 auto; padding: 1.5rem 1rem; }
        h2 { color: #64b5f6; font-size: 1.3rem; margin-bottom: 1rem; }
        .mode { color: #81c784; font-size: 0.85rem; margin-bottom: 1.5rem; }
        .actions { display: flex; gap: 1rem; justify-content: center; margin-bottom: 1.5rem; }
        .actions md-filled-button, .actions md-outlined-button {
          flex: 1; max-width: 200px; height: 60px; font-size: 1.1rem;
        }
        details { margin-top: 1rem; }
        summary { color: #78909c; cursor: pointer; font-size: 0.9rem; padding: 0.5rem 0; }
        .adv-field { margin: 0.75rem 0; display: flex; align-items: center; gap: 0.75rem; }
        .adv-field label { color: #b0bec5; font-size: 0.9rem; min-width: 100px; }
        .adv-field input[type=url] {
          flex: 1; background: #0d1117; border: 1px solid #304050; border-radius: 8px;
          color: #e0e0e0; padding: 0.5rem; font-size: 0.85rem;
        }
        .adv-field input[type=checkbox] { width: 20px; height: 20px; accent-color: #64b5f6; }
        .back { color: #78909c; cursor: pointer; font-size: 0.85rem; margin-top: 1.5rem; display: block; }
      </style>
      <div class="screen">
        <h2>Send or Receive?</h2>
        <p class="mode">${summarize(policy, advanced.server)}</p>
        <div class="actions">
          <md-filled-button id="btn-send">↑ Send</md-filled-button>
          <md-outlined-button id="btn-recv">↓ Receive</md-outlined-button>
        </div>
        ${policy.preset === "none" ? `
          <div style="text-align:center;margin-top:1rem">
            <md-outlined-button id="btn-qr-send" style="margin-right:0.5rem">📷 QR Send</md-outlined-button>
            <md-outlined-button id="btn-qr-recv">📷 QR Receive</md-outlined-button>
          </div>
          <p style="text-align:center;color:#78909c;font-size:0.8rem;margin-top:0.5rem">
            Scan QR codes between two devices — no typing, no paste, no servers.
          </p>
        ` : ""}
        <details>
          <summary>⚙ Advanced</summary>
          <div class="adv-field">
            <label for="adv-server">Server</label>
            <input type="url" id="adv-server" value="${escapeHtml(advanced.server)}"
              placeholder="https://jsi-signal.treytorrez.workers.dev" />
          </div>
          <div class="adv-field">
            <label for="adv-mdns">mDNS</label>
            <input type="checkbox" id="adv-mdns" ${advanced.mdns ? "checked" : ""} />
            <span style="color:#78909c;font-size:0.8rem">hide LAN IPs (privacy)</span>
          </div>
          <div class="adv-field">
            <label for="adv-stun">STUN</label>
            <input type="checkbox" id="adv-stun" ${advanced.stun ? "checked" : ""} />
            <span style="color:#78909c;font-size:0.8rem">discover public IP for direct P2P</span>
          </div>
        </details>
        <span class="back" id="btn-back">← Back to privacy</span>
      </div>
    `;
    attach();
  };

  const attach = () => {
    container.querySelector("#btn-send")?.addEventListener("click", onSend);
    container.querySelector("#btn-recv")?.addEventListener("click", onReceive);
    container.querySelector("#btn-back")?.addEventListener("click", onBack);
    container.querySelector("#btn-qr-send")?.addEventListener("click", () => onQRSend?.());
    container.querySelector("#btn-qr-recv")?.addEventListener("click", () => onQRReceive?.());

    const serverInput = container.querySelector("#adv-server") as HTMLInputElement | null;
    serverInput?.addEventListener("change", () => {
      advanced.server = serverInput.value.trim() || "https://jsi-signal.treytorrez.workers.dev";
      onAdvancedChange?.(advanced);
      rerender();
    });

    const mdnsInput = container.querySelector("#adv-mdns") as HTMLInputElement | null;
    mdnsInput?.addEventListener("change", () => {
      advanced.mdns = mdnsInput.checked;
      onAdvancedChange?.(advanced);
    });

    const stunInput = container.querySelector("#adv-stun") as HTMLInputElement | null;
    stunInput?.addEventListener("change", () => {
      advanced.stun = stunInput.checked;
      onAdvancedChange?.(advanced);
    });
  };

  rerender();
}

function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) =>
    c === "&" ? "&amp;" : c === "<" ? "&lt;" : c === ">" ? "&gt;" : c === '"' ? "&quot;" : "&#39;",
  );
}
