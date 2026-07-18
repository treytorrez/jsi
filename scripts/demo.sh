#!/bin/sh
# spike/demo: fully automated two-process demo — real WebRTC (DTLS) transfer
# over paste signaling, zero infrastructure. Run inside `nix develop`:
#   scripts/demo.sh
set -eu
cd "$(dirname "$0")/.."

go build -o bin/jsi-demo ./cmd/jsi-demo

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "==> creating 8 MiB random file"
head -c 8388608 /dev/urandom >"$work/sample.bin"
mkdir "$work/out"

# Named pipes wire sender stdout -> receiver stdin (offer) and back (answer).
mkfifo "$work/offer" "$work/answer"

echo "==> starting sender (background) and receiver (foreground)"
./bin/jsi-demo send "$work/sample.bin" >"$work/offer" <"$work/answer" 2>"$work/send.log" &
sender=$!
./bin/jsi-demo receive "$work/out" <"$work/offer" >"$work/answer" 2>"$work/recv.log"
wait "$sender"

echo "--- sender log ---"; cat "$work/send.log"
echo "--- receiver log ---"; cat "$work/recv.log"

a=$(sha256sum "$work/sample.bin" | cut -d' ' -f1)
b=$(sha256sum "$work/out/sample.bin" | cut -d' ' -f1)
if [ "$a" = "$b" ]; then
  echo "DEMO OK: 8 MiB transferred P2P, sha256 match: $a"
else
  echo "DEMO FAIL: hash mismatch"; exit 1
fi
