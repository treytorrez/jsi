// Augment the global Cloudflare.Env so `env` from cloudflare:test carries our
// bindings (the 0.18.x pool types `env` as Cloudflare.Env from workers-types).
import type { Env as JsiEnv } from "../src/env";

declare global {
  namespace Cloudflare {
    interface Env extends JsiEnv {}
  }
}

export {};
