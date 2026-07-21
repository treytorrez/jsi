// JSI PWA entry point. Home screen routes to send/receive/settings.
// Default policy: none (paste signaling, zero server contact — D15).

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import {
  resolvePolicy,
  summarize,
  DefaultServerURL,
  ExternalsNone,
  type Policy,
} from "./policy";
import { renderSend } from "./screens/send";
import { renderReceive } from "./screens/receive";

type Screen = "home" | "send" | "receive" | "settings";

let currentScreen: Screen = "home";
let policy: Policy = resolvePolicy(ExternalsNone, "", "unset");
const serverURL = DefaultServerURL;

const app = document.getElementById("app")!;

function render(): void {
  if (currentScreen === "home") {
    renderHome();
  } else if (currentScreen === "send") {
    renderSend(app, policy, serverURL, () => {
      currentScreen = "home";
      render();
    });
  } else if (currentScreen === "receive") {
    renderReceive(app, policy, serverURL, () => {
      currentScreen = "home";
      render();
    });
  } else if (currentScreen === "settings") {
    renderSettings();
  }
}

function renderHome(): void {
  app.innerHTML = `
    <style>
      body { margin: 0; background: #1a1a2e; color: #e0e0e0; font-family: system-ui, sans-serif; }
      #app { max-width: 600px; margin: 0 auto; padding: 2rem 1rem; }
      h1 { color: #64b5f6; font-size: 1.5rem; }
      .tagline { color: #90a4ae; margin-bottom: 2rem; }
      .actions { display: flex; gap: 1rem; flex-wrap: wrap; justify-content: center; }
      .mode { margin-top: 2rem; color: #81c784; font-size: 0.9rem; }
      /* Fallback: show button text even before MWC upgrades */
      md-filled-button, md-outlined-button { display: inline-block; cursor: pointer; padding: 0.5rem 1rem; }
      md-filled-button { background: #64b5f6; color: #1a1a2e; border-radius: 20px; }
      md-outlined-button { border: 1px solid #64b5f6; color: #64b5f6; border-radius: 20px; }
    </style>
    <h1>JSI — Just Send It</h1>
    <p class="tagline">Peer-to-peer file transfer. No accounts, no servers touch your files.</p>
    <div class="actions">
      <md-filled-button id="btn-send">Send</md-filled-button>
      <md-outlined-button id="btn-recv">Receive</md-outlined-button>
    </div>
    <p class="mode">${summarize(policy, serverURL)}</p>
  `;

  document.getElementById("btn-send")?.addEventListener("click", () => {
    currentScreen = "send";
    render();
  });
  document.getElementById("btn-recv")?.addEventListener("click", () => {
    currentScreen = "receive";
    render();
  });
}

function renderSettings(): void {
  // Placeholder — full settings screen lands in S4.10.
  app.innerHTML = `<p>Settings — coming soon</p><md-outlined-button id="back">Back</md-outlined-button>`;
  document.getElementById("back")?.addEventListener("click", () => {
    currentScreen = "home";
    render();
  });
}

// Register the service worker for offline paste mode.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}

// Wait for MWC custom elements to be defined before rendering, so the
// browser doesn't render empty/unknown elements on first paint.
const components = [
  "md-filled-button",
  "md-outlined-button",
];

Promise.all(components.map((c) => customElements.whenDefined(c)))
  .then(() => render())
  .catch(() => render()); // render anyway if a component fails to load
