// Privacy screen (S4 redesign): three large Material cards for the D15
// externals presets. User picks one every visit (no localStorage — deliberate).

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import {
  ExternalsNone,
  ExternalsFallback,
  ExternalsFull,
} from "../policy";

export function renderPrivacy(
  container: HTMLElement,
  onChoose: (preset: string) => void,
): void {
  container.innerHTML = `
    <style>
      .screen { max-width: 600px; margin: 0 auto; padding: 1.5rem 1rem; }
      h2 { color: #64b5f6; font-size: 1.3rem; margin-bottom: 0.5rem; }
      .subtitle { color: #90a4ae; font-size: 0.9rem; margin-bottom: 1.5rem; }
      .cards { display: flex; flex-direction: column; gap: 1rem; }
      .card {
        background: #16213e; border: 1px solid #304050; border-radius: 16px;
        padding: 1.25rem; cursor: pointer; transition: border-color 0.2s, background 0.2s;
        display: flex; align-items: center; gap: 1rem;
      }
      .card:hover { border-color: #64b5f6; background: #1a2744; }
      .card:active { transform: scale(0.99); }
      .card-icon { font-size: 2rem; flex-shrink: 0; }
      .card-body { flex: 1; }
      .card-title { font-size: 1.1rem; font-weight: 600; margin-bottom: 0.25rem; }
      .card-desc { font-size: 0.85rem; color: #90a4ae; line-height: 1.3; }
      .card-none .card-title { color: #81c784; }
      .card-fallback .card-title { color: #ffb74d; }
      .card-full .card-title { color: #64b5f6; }
      .help-link { color: #78909c; font-size: 0.8rem; text-decoration: underline; cursor: pointer;
                   margin-top: 1.5rem; text-align: center; display: block; }
      .help-text { color: #78909c; font-size: 0.8rem; margin-top: 1rem; display: none;
                   line-height: 1.4; }
      .help-text.show { display: block; }
    </style>
    <div class="screen">
      <h2>How private should this transfer be?</h2>
      <p class="subtitle">Your choice controls which servers, if any, help connect you to the other device.</p>
      <div class="cards">
        <div class="card card-none" id="card-none">
          <div class="card-icon">🔒</div>
          <div class="card-body">
            <div class="card-title">No Servers</div>
            <div class="card-desc">Paste blobs manually. Zero external contact — no servers, no STUN, no TURN. Best for same-network or offline transfers.</div>
          </div>
        </div>
        <div class="card card-fallback" id="card-fallback">
          <div class="card-icon">⚡</div>
          <div class="card-body">
            <div class="card-title">Best Effort</div>
            <div class="card-desc">Try direct first, then fall back to Cloudflare signaling + TURN relay if needed. Good balance of privacy and reliability.</div>
          </div>
        </div>
        <div class="card card-full" id="card-full">
          <div class="card-icon">🌐</div>
          <div class="card-body">
            <div class="card-title">Always Online</div>
            <div class="card-desc">Use the Cloudflare signaling worker. TURN relay available if direct fails. Most reliable for cross-internet transfers.</div>
          </div>
        </div>
      </div>
      <span class="help-link" id="help-link">What does this mean?</span>
      <div class="help-text" id="help-text">
        JSI uses WebRTC for peer-to-peer transfer — your file bytes never touch a server. But
        two devices need to find each other first. "No Servers" means you manually copy-paste
        connection codes. "Always Online" uses a Cloudflare Worker to broker the handshake (it
        forgets everything in 90 seconds). "Best Effort" tries manual first, then falls back to
        the worker. TURN relay (if needed) carries only encrypted traffic — Cloudflare cannot
        read your files.
      </div>
    </div>
  `;

  container.querySelector("#card-none")?.addEventListener("click", () => onChoose(ExternalsNone));
  container.querySelector("#card-fallback")?.addEventListener("click", () => onChoose(ExternalsFallback));
  container.querySelector("#card-full")?.addEventListener("click", () => onChoose(ExternalsFull));

  const helpLink = container.querySelector("#help-link");
  const helpText = container.querySelector("#help-text");
  helpLink?.addEventListener("click", () => helpText?.classList.toggle("show"));
}
