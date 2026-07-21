#!/bin/sh
# M1.8: self-host the SP/1 signaling worker on your own Cloudflare account.
# Needs node; wrangler auth via `npx wrangler login`. Run from anywhere:
#   scripts/deploy-worker.sh
set -eu
cd "$(dirname "$0")/../worker"

echo "==> checking wrangler auth"
npx wrangler whoami >/dev/null

echo "==> deploying worker (Durable Object migration v1 applies automatically)"
npx wrangler deploy

cat <<'EOF'

Deployed. Optional — enable TURN relay fallback (needed by ~10% of NATs):
  1. Cloudflare dashboard → Realtime → TURN → Create key (note key id + API token)
  2. npx wrangler secret put TURN_API_TOKEN        # paste the API token
  3. set vars.TURN_KEY_ID in wrangler.jsonc to the key id, re-run this script

(Without TURN the worker is STUN-only: direct P2P works for most networks.
wrangler's OAuth login cannot create TURN keys — the dashboard step is required.)

Point clients at your worker:
  jsi send <file> --externals full --server https://<your-worker>.workers.dev
EOF
