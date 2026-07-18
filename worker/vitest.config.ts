import { defineConfig } from "vitest/config";
import { cloudflareTest } from "@cloudflare/vitest-pool-workers";

// M1.7: tests run inside workerd via @cloudflare/vitest-pool-workers; each
// test file gets an isolated KV namespace. Bindings/vars come from wrangler.jsonc.
export default defineConfig({
  plugins: [cloudflareTest({ wrangler: { configPath: "./wrangler.jsonc" } })],
});
