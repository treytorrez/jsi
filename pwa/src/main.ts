// JSI PWA entry point (S4.1 scaffold). Minimal app shell — full screens land
// in S4.7–S4.10. This just verifies the build pipeline + MWC renders.
import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";
import "@material/web/iconbutton/icon-button.js";

import { resolvePolicy, summarize, DefaultServerURL, ExternalsNone } from "./policy";

const app = document.getElementById("app")!;
const pol = resolvePolicy(ExternalsNone, "", "unset");

app.innerHTML = `
  <style>
    body { margin: 0; background: #1a1a2e; color: #e0e0e0; font-family: system-ui, sans-serif; }
    #app { max-width: 600px; margin: 0 auto; padding: 2rem 1rem; }
    h1 { color: #64b5f6; font-size: 1.5rem; }
    .tagline { color: #90a4ae; margin-bottom: 2rem; }
    .actions { display: flex; gap: 1rem; flex-wrap: wrap; }
    .mode { margin-top: 2rem; color: #81c784; font-size: 0.9rem; }
  </style>
  <h1>JSI — Just Send It</h1>
  <p class="tagline">Peer-to-peer file transfer. No accounts, no servers touch your files.</p>
  <div class="actions">
    <md-filled-button id="btn-send">Send</md-filled-button>
    <md-outlined-button id="btn-recv">Receive</md-outlined-button>
  </div>
  <p class="mode">${summarize(pol, DefaultServerURL)}</p>
`;

document.getElementById("btn-send")?.addEventListener("click", () => {
  alert("Send flow — coming in S4.8");
});
document.getElementById("btn-recv")?.addEventListener("click", () => {
  alert("Receive flow — coming in S4.9");
});

// Register the service worker for offline paste mode.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}
