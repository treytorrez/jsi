#!/bin/sh
# M3.5: e2e — two CLIs transfer 100 MiB through a local signaling worker
# (wrangler dev). Run inside `nix develop` (needs go, node, curl).
set -eu
cd "$(dirname "$0")/.."

go build -o bin/jsi ./cmd/jsi

work=$(mktemp -d)
head -c 104857600 /dev/urandom >"$work/f.bin"
mkdir "$work/out"

(cd worker && npx wrangler dev --port 8787 --ip 127.0.0.1 >"$work/w.log" 2>&1) &
wpid=$!
trap 'kill $wpid 2>/dev/null; rm -rf "$work"' EXIT

echo "==> waiting for local worker"
i=0
until curl -sf http://127.0.0.1:8787/health >/dev/null 2>&1; do
  i=$((i+1)); [ "$i" -gt 60 ] && { echo "FAIL: worker did not start"; cat "$work/w.log"; exit 1; }
  sleep 1
done

echo "==> sender (background, --externals full)"
./bin/jsi send "$work/f.bin" --externals full --server http://127.0.0.1:8787 >"$work/s.out" 2>"$work/s.log" &
spid=$!

i=0
until tok=$(sed -n 's/^token: //p' "$work/s.log" 2>/dev/null) && [ -n "$tok" ]; do
  i=$((i+1)); [ "$i" -gt 30 ] && { echo "FAIL: no token from sender"; cat "$work/s.log"; exit 1; }
  sleep 1
done
echo "==> token: $tok — receiver connecting"
./bin/jsi receive -y -o "$work/out" --externals full --server http://127.0.0.1:8787 "$tok" >"$work/r.out" 2>"$work/r.log"
wait "$spid"

grep -h "connected:" "$work/s.log" "$work/r.log" || true
a=$(sha256sum "$work/f.bin" | cut -d' ' -f1)
b=$(sha256sum "$work/out/f.bin" | cut -d' ' -f1)
[ "$a" = "$b" ] && echo "PASS: e2e-cli 100 MiB via local worker, sha256 match: $a" || { echo "FAIL: hash mismatch"; exit 1; }
