#!/usr/bin/env bash
# Boot the gateway from THIS checkout, run the ferro integration suite against
# it, and always tear it down.
#
#   cd cli && ./scripts/with-gateway.sh [extra go test args]
#
# This is the one check the fixture (cmd/fakegw) cannot perform. The fixture
# proves ferro is correct given a gateway that behaves as documented; only this
# proves the real server agrees. Any divergence found here is a FIXTURE bug:
# fix internal/fixture to match the gateway, then re-run.
#
# No provider credentials are required. The gateway starts with zero targets
# and reports no_providers, which is enough to exercise status, keys, logs,
# sessions, and audit — everything except live chat.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cli="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
port="${FERRO_ITEST_PORT:-18080}"
gw_pid=""

# A hex master key of the shape the gateway expects (fgw_ + 32 hex chars).
key="fgw_$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')"

cleanup() {
  if [ -n "$gw_pid" ]; then
    kill "$gw_pid" 2>/dev/null || true
    wait "$gw_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

echo "==> building ferrogw from $root"
(cd "$root" && go build -o "$work/ferrogw" ./cmd/ferrogw)

echo "==> scaffolding a throwaway config"
# init writes a config and prints a master key once; we supply our own key via
# the environment instead, so a failure here is not fatal.
(cd "$work" && "$work/ferrogw" init -o "$work/gateway.yaml" --non-interactive >/dev/null 2>&1) \
  || echo "    (init declined; starting with an empty config)"

echo "==> starting gateway on :$port"
# A throwaway SQLite request log. Without it /admin/logs and /admin/logs/stats
# answer 501 and the suite can only prove they are absent -- and those two carry
# the most intricate types in internal/api (nullable duration_ms/ttft_ms/cost_usd,
# a nullable percentile block). One file in the temp dir turns two skipped rows
# into real decode checks.
MASTER_KEY="$key" GATEWAY_CONFIG="$work/gateway.yaml" PORT="$port" \
  REQUEST_LOG_STORE_BACKEND=sqlite REQUEST_LOG_STORE_DSN="$work/requestlog.db" \
  "$work/ferrogw" serve >"$work/gateway.log" 2>&1 &
gw_pid=$!

# /health answers 503 with {"status":"no_providers"} when no provider credential
# is present -- which is exactly how this harness runs -- so `curl -f` is the
# wrong probe: it reports a serving gateway as dead. Ask for the status code
# instead and accept either answer the endpoint can give.
healthy() {
  local code
  code="$(curl -sS --connect-timeout 1 --max-time 1 -o /dev/null -w '%{http_code}' "http://localhost:$port/health" 2>/dev/null)" || return 1
  [ "$code" = "200" ] || [ "$code" = "503" ]
}

startup_deadline=$((SECONDS + 15))
while (( SECONDS < startup_deadline )); do
  # A gateway that died is never coming up; fail fast instead of waiting out
  # the whole loop or accepting another process already bound to the port.
  if ! kill -0 "$gw_pid" 2>/dev/null; then
    echo "gateway exited during startup; log follows:" >&2
    cat "$work/gateway.log" >&2
    exit 1
  fi
  if healthy; then
    break
  fi
  sleep 0.25
done

if ! healthy; then
  echo "gateway did not become healthy within 15s; log follows:" >&2
  cat "$work/gateway.log" >&2
  exit 1
fi

echo "==> running the integration suite"
cd "$cli"
FERRO_ITEST_URL="http://localhost:$port" FERRO_ITEST_KEY="$key" \
  go test -race -tags integration ./itest/ "$@"
