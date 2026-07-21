import { defineConfig } from "vitest/config";
import { cloudflareTest } from "@cloudflare/vitest-pool-workers";

// M1.7: tests run inside workerd via @cloudflare/vitest-pool-workers; each
// test gets isolated storage (incl. the SESSION_DO namespace, M1.9).
// Bindings/vars come from wrangler.jsonc.
export default defineConfig({
  plugins: [cloudflareTest({ wrangler: { configPath: "./wrangler.jsonc" } })],
});
