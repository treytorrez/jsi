// JSI PWA entry point. Redesigned flow: privacy → action → send/receive.
// Deep link: #t=TOKEN skips straight to receive (worker mode, token pre-filled).

import "@material/web/button/filled-button.js";
import "@material/web/button/outlined-button.js";

import {
  resolvePolicy,
  DefaultServerURL,
  ExternalsNone,
  ExternalsFull,
  ExternalsFallback,
  SignalWorker,
  SignalPaste,
  SignalQR,
  type Policy,
} from "./policy";
import { renderPrivacy } from "./screens/privacy";
import { renderAction, type AdvancedSettings } from "./screens/action";
import { renderSend } from "./screens/send";
import { renderReceive } from "./screens/receive";
import { renderQRSend } from "./screens/qr-send";
import { renderQRReceive } from "./screens/qr-receive";

const VERSION = "0.1.0";

type Screen = "privacy" | "action" | "send" | "receive" | "qr-send" | "qr-receive";

let currentScreen: Screen = "privacy";
let preset = ExternalsNone;
let serverURL = DefaultServerURL;
let advanced: AdvancedSettings = { server: DefaultServerURL, mdns: true, stun: true };
let deepLinkToken: string | null = null;

const app = document.getElementById("app")!;

function getPolicy(): Policy {
  const relay = advanced.stun ? "unset" : "off";
  // Signal is determined by preset: none→paste, fallback→paste, full→worker.
  // Override signal to "worker" if full, "paste" otherwise (default behavior).
  const p = resolvePolicy(preset, "", relay as "unset" | "on" | "off");
  return p;
}

function render(): void {
  // Inject the version badge once (persists across screen changes).
  let badge = document.getElementById("version-badge");
  if (!badge) {
    badge = document.createElement("div");
    badge.id = "version-badge";
    badge.style.cssText =
      "position:fixed;bottom:0.5rem;right:0.75rem;color:#455a64;" +
      "font-size:0.7rem;font-family:monospace;pointer-events:none;z-index:9999";
    badge.textContent = `jsi v${VERSION}`;
    document.body.appendChild(badge);
  }

  switch (currentScreen) {
    case "privacy":
      renderPrivacy(app, (choice: string) => {
        preset = choice;
        // If full, default signal to worker; else paste (resolvePolicy handles this).
        currentScreen = "action";
        render();
      });
      break;

    case "action":
      renderAction(
        app,
        getPolicy(),
        advanced.server,
        () => { currentScreen = "send"; render(); },
        () => { currentScreen = "receive"; render(); },
        () => { currentScreen = "privacy"; render(); },
        () => { currentScreen = "qr-send"; render(); },
        () => { currentScreen = "qr-receive"; render(); },
        (s: AdvancedSettings) => { advanced = s; serverURL = s.server; },
      );
      break;

    case "send": {
      const pol = getPolicy();
      window.__jsiAdvanced = advanced;
      renderSend(app, pol, advanced.server, () => {
        currentScreen = "action";
        render();
      });
      break;
    }

    case "receive": {
      let pol = getPolicy();
      let prefillToken: string | null = null;

      if (deepLinkToken) {
        preset = ExternalsFull;
        pol = resolvePolicy(ExternalsFull, SignalWorker, "unset");
        prefillToken = deepLinkToken;
        deepLinkToken = null;
      }

      window.__jsiAdvanced = advanced;
      renderReceive(app, pol, advanced.server, () => {
        currentScreen = "action";
        render();
      }, prefillToken);
      break;
    }

    case "qr-send": {
      const pol = resolvePolicy(preset, SignalQR, preset === ExternalsFallback ? "unset" : "off");
      window.__jsiAdvanced = advanced;
      renderQRSend(app, pol, advanced.server, () => {
        currentScreen = "action";
        render();
      });
      break;
    }

    case "qr-receive": {
      const pol = resolvePolicy(preset, SignalQR, preset === ExternalsFallback ? "unset" : "off");
      window.__jsiAdvanced = advanced;
      renderQRReceive(app, pol, advanced.server, () => {
        currentScreen = "action";
        render();
      });
      break;
    }
  }
}

// Check for deep-link hash on load: #t=TOKEN
function checkDeepLink(): void {
  const hash = window.location.hash;
  const match = hash.match(/^#t=([A-Z0-9]{6})$/i);
  if (match) {
    deepLinkToken = match[1].toUpperCase();
    currentScreen = "receive";
  }
}

// Register the service worker for offline paste mode.
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").catch(() => {});
}

checkDeepLink();

// Wait for MWC custom elements to be defined before rendering.
Promise.all([
  customElements.whenDefined("md-filled-button"),
  customElements.whenDefined("md-outlined-button"),
])
  .then(() => render())
  .catch(() => render());
