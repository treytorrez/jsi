#!/bin/sh
# scripts/e2e-local.sh — M2.8 e2e: full SP/1 handshake + TP/1 transfer
# through a REAL signaling worker. Boots `wrangler dev` on 127.0.0.1:8787,
# waits for /health, then runs the TestE2EWorker Go test (sender and
# receiver meet through the worker; run inside `nix develop` — needs node,
# go, curl).
set -eu

PORT=8787
HOST=127.0.0.1
BASE="http://$HOST:$PORT"
HEALTH_BUDGET=60 # seconds; first boot compiles workerd/the worker

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
LOG=$(mktemp "${TMPDIR:-/tmp}/jsi-worker-log.XXXXXX")

WPID=
cleanup() {
	trap - EXIT INT TERM
	if [ -n "$WPID" ] && kill -0 "$WPID" 2>/dev/null; then
		kill "$WPID" 2>/dev/null || true
		wait "$WPID" 2>/dev/null || true
	fi
}
trap cleanup EXIT
trap 'exit 1' INT TERM

echo "==> starting worker (npx wrangler dev --port $PORT --ip $HOST), log: $LOG"
cd "$ROOT/worker"
npx wrangler dev --port "$PORT" --ip "$HOST" >"$LOG" 2>&1 &
WPID=$!

echo "==> waiting for $BASE/health (budget ${HEALTH_BUDGET}s)..."
i=0
until curl -fsS -o /dev/null "$BASE/health" 2>/dev/null; do
	if ! kill -0 "$WPID" 2>/dev/null; then
		echo "FAIL: wrangler dev exited early — worker log:" >&2
		cat "$LOG" >&2
		exit 1
	fi
	i=$((i + 1))
	if [ "$i" -ge "$HEALTH_BUDGET" ]; then
		echo "FAIL: worker did not answer /health within ${HEALTH_BUDGET}s — last log lines:" >&2
		tail -n 40 "$LOG" >&2 || true
		exit 1
	fi
	sleep 1
done
echo "==> worker healthy at $BASE"

cd "$ROOT"
set +e
JSI_E2E_WORKER="$BASE" go test -race -count=1 -run TestE2EWorker ./internal/e2e/...
STATUS=$?
set -e

if [ "$STATUS" -eq 0 ]; then
	echo "PASS: e2e transfer through local worker succeeded"
else
	echo "FAIL: e2e test exited $STATUS (worker log: $LOG)" >&2
fi
exit "$STATUS"
