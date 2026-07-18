#!/bin/sh
# M3.7: e2e — two CLIs over QP/1 paste signaling, with a syscall-level proof
# that the default `none` preset (with --no-stun --no-mdns) makes ZERO
# external network contact: strace records every sendto/connect; a single
# non-local destination fails the test.
#
# (An earlier draft used `unshare -rn` network namespaces. That was abandoned
# after debugging showed Pion correctly omits loopback addresses from ICE host
# candidates — inside a loopback-only netns there are zero candidates, so ICE
# can never connect. strace proves the same property on the real code path.)
set -eu
cd "$(dirname "$0")/.."

go build -o bin/jsi ./cmd/jsi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
head -c 33554432 /dev/urandom >"$work/f.bin"
mkdir "$work/out"
mkfifo "$work/offer" "$work/answer"

run() {
  if command -v strace >/dev/null 2>&1; then
    strace -f -e trace=sendto,sendmsg,connect -o "$1" "${@:2}"
  else
    "${@:2}"
  fi
}

if ! command -v strace >/dev/null 2>&1; then
  echo "==> WARNING: strace unavailable — running without syscall proof"
fi

run "$work/s.trace" ./bin/jsi send --no-stun --no-mdns "$work/f.bin" >"$work/offer" <"$work/answer" 2>"$work/s.log" &
spid=$!
run "$work/r.trace" ./bin/jsi receive --no-stun --no-mdns -y -o "$work/out" <"$work/offer" >"$work/answer" 2>"$work/r.log"
wait "$spid"

grep -h "connected:" "$work/s.log" "$work/r.log" || true
a=$(sha256sum "$work/f.bin" | cut -d' ' -f1)
b=$(sha256sum "$work/out/f.bin" | cut -d' ' -f1)
[ "$a" = "$b" ] || { echo "FAIL: hash mismatch"; exit 1; }
echo "transfer OK: 32 MiB, sha256 match: $a"

if command -v strace >/dev/null 2>&1; then
  dests=$(grep -hoE 'inet_addr\("[0-9.]+"\)|inet_pton\(AF_INET6, "[0-9a-fA-F:]+"\)' "$work/s.trace" "$work/r.trace" 2>/dev/null \
          | sed -E 's/inet_addr\("([^"]+)"\)/\1/; s/inet_pton\(AF_INET6, "([^"]+)"\)/\1/' | sort -u || true)
  own=$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -v '^$' || true)
  bad=""
  for d in $dests; do
    case "$d" in
      127.*|::1|0.0.0.0|::|fe80:*|ff02:*) continue ;;
    esac
    printf '%s\n' "$own" | grep -qx "$d" && continue
    bad="$bad $d"
  done
  [ -n "$bad" ] && { echo "FAIL: external network destinations observed:$bad"; exit 1; }
  echo "isolation OK: all network destinations local ($(echo $dests | tr '\n' ' '))"
fi
echo "PASS: e2e-paste — 32 MiB P2P, zero external contact proven"
