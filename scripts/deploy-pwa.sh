#!/bin/sh
# Deploy the PWA to Cloudflare Workers Static Assets.
# Run from anywhere: scripts/deploy-pwa.sh
set -eu
cd "$(dirname "$0")/../pwa"

echo "==> installing deps"
npm ci

echo "==> building"
npx vite build

echo "==> deploying"
npx wrangler deploy

echo "==> done: https://jsi-web.treytorrez.workers.dev"
