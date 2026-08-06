#!/usr/bin/env bash
#
# plugin_smoke.sh — quick live check that every built-in plugin works.
#
# Boots the gateway against one real (cheap) provider and exercises each plugin
# end-to-end over HTTP, then prints a PASS/FAIL summary. Phase 1 uses the
# committed scripts/config.plugin-test.yaml (retargeted to $PROVIDER); the
# budget and rate-limit reject-demos run from throwaway tight configs. Everything
# runs in a temp dir with in-memory stores.
#
#   scripts/plugin_smoke.sh                 # defaults: deepseek / deepseek-chat
#   PROVIDER=gemini MODEL=gemini-2.5-flash-lite scripts/plugin_smoke.sh
#   PORT=18099 scripts/plugin_smoke.sh
#
# Credential: read from $<PROVIDER>_API_KEY in the environment, else from .env.
# (e.g. DEEPSEEK_API_KEY). Free options: gemini, groq. deepseek is cheap and has
# no tight free-tier RPM, which makes the multi-request tests reliable.
#
# Plugins covered: word-filter, max-token, response-cache, request-logger,
# budget, rate-limit. Exit code is non-zero if any check fails.
set -euo pipefail

cd "$(dirname "$0")/.."   # repo root

PROVIDER="${PROVIDER:-deepseek}"
MODEL="${MODEL:-deepseek-chat}"
PORT="${PORT:-18080}"
BASE="http://localhost:${PORT}"
MASTER_KEY="sk-plugin-smoke-master-0123456789"

# Credential env var name: DEEPSEEK_API_KEY, NVIDIA_NIM_API_KEY, … Override the
# whole name with PROVIDER_KEY_VAR for providers that break the convention.
KEYVAR="${PROVIDER_KEY_VAR:-$(printf '%s' "$PROVIDER" | tr '[:lower:]-' '[:upper:]_')_API_KEY}"
PROVIDER_KEY="${!KEYVAR:-}"
if [ -z "$PROVIDER_KEY" ] && [ -f .env ]; then
  PROVIDER_KEY="$(grep -E "^${KEYVAR}=" .env | head -1 | sed -E "s/^${KEYVAR}=//; s/^\"//; s/\"$//")"
fi
if [ -z "$PROVIDER_KEY" ]; then
  echo "ERROR: no ${KEYVAR} found (checked the environment and .env)." >&2
  echo "       export ${KEYVAR}=... (or pass PROVIDER=<a provider you have a key for>)." >&2
  exit 1
fi

TMP="$(mktemp -d)"
BIN="$TMP/ferrogw"
GW_PID=""
cleanup() { [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT

echo "Building ferrogw…"
go build -o "$BIN" ./cmd/ferrogw   # plain build; the API needs no embedded dashboard

pass=0; fail=0
check() { # <name> <ok:0|1> <detail-on-fail>
  if [ "$2" -eq 0 ]; then printf '  \033[32m✅ %s\033[0m\n' "$1"; pass=$((pass+1))
  else printf '  \033[31m❌ %s\033[0m — %s\n' "$1" "$3"; fail=$((fail+1)); fi
}

wait_ready() {
  local i
  for i in $(seq 1 40); do
    curl -sf -o /dev/null -m 2 "$BASE/readyz" && return 0
    sleep 0.5
  done
  echo "gateway did not become ready; last log lines:"; tail -20 "$TMP/gw.log" >&2
  exit 1
}

start_gw() { # <config-file> <unauth|auth>
  local unauth=""; [ "$2" = "unauth" ] && unauth="true"
  rm -f "$TMP/reqlog.db"
  env MASTER_KEY="$MASTER_KEY" PORT="$PORT" \
      ALLOW_UNAUTHENTICATED_PROXY="$unauth" \
      CONFIG_STORE_BACKEND=memory API_KEY_STORE_BACKEND=memory \
      REQUEST_LOG_STORE_BACKEND=sqlite REQUEST_LOG_STORE_DSN="$TMP/reqlog.db" \
      "$KEYVAR=$PROVIDER_KEY" GATEWAY_CONFIG="$1" \
      "$BIN" serve >"$TMP/gw.log" 2>&1 &
  GW_PID=$!
  wait_ready
}
stop_gw() { [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true; wait "$GW_PID" 2>/dev/null || true; GW_PID=""; sleep 0.5; }

# call <auth-token-or-empty> <json-body> -> sets CODE and BODY
call() {
  local out
  out=$(curl -s -m 40 -w $'\n%{http_code}' \
        ${1:+-H "Authorization: Bearer $1"} \
        -H 'Content-Type: application/json' -d "$2" "$BASE/v1/chat/completions")
  CODE="${out##*$'\n'}"; BODY="${out%$'\n'*}"
}
req() { printf '{"model":"%s","messages":[{"role":"user","content":"%s"}],"max_tokens":%s}' "$MODEL" "$1" "${2:-20}"; }

# --- plugin configs -------------------------------------------------------------
# Phase 1 uses the committed all-plugins config (retargeted to $PROVIDER); its
# budget/rate-limit are permissive, so they don't interfere with the guardrail,
# cache and logger checks. Phases 2 and 3 need TIGHT budget/rate-limit to make
# those two plugins actually reject, which cannot coexist with the phase-1 checks
# in one gateway — so they get their own throwaway configs below.
CFG_BASE="scripts/config.plugin-test.yaml"
[ -f "$CFG_BASE" ] || { echo "ERROR: missing $CFG_BASE" >&2; exit 1; }
sed "s/^\( *virtual_key: \)deepseek/\1${PROVIDER}/" "$CFG_BASE" > "$TMP/p1.yaml"

cat > "$TMP/p2.yaml" <<YAML
strategy: { mode: single }
targets: [ { virtual_key: ${PROVIDER} } ]
plugins:
  - { name: budget, type: guardrail, stage: before_request, enabled: true, config: { store_id: default, spend_limit_usd: 0.0001, input_per_m_tokens: 100.0, output_per_m_tokens: 100.0, max_keys: 10000 } }
  - { name: budget, type: guardrail, stage: after_request,  enabled: true, config: { store_id: default, spend_limit_usd: 0.0001, input_per_m_tokens: 100.0, output_per_m_tokens: 100.0, max_keys: 10000 } }
  - { name: request-logger, type: logging, stage: before_request, enabled: true, config: { level: info, persist: false } }
  - { name: request-logger, type: logging, stage: after_request,  enabled: true, config: { level: info, persist: false } }
  - { name: request-logger, type: logging, stage: on_error,       enabled: true, config: { level: info, persist: false } }
YAML

cat > "$TMP/p3.yaml" <<YAML
strategy: { mode: single }
targets: [ { virtual_key: ${PROVIDER} } ]
plugins:
  - { name: rate-limit, type: guardrail, stage: before_request, enabled: true, config: { requests_per_second: 2, burst: 2 } }
  - { name: request-logger, type: logging, stage: before_request, enabled: true, config: { level: info, persist: false } }
  - { name: request-logger, type: logging, stage: on_error,       enabled: true, config: { level: info, persist: false } }
  - { name: request-logger, type: logging, stage: after_request,  enabled: true, config: { level: info, persist: false } }
YAML

echo
echo "== Plugin smoke test =="
echo "provider=$PROVIDER  model=$MODEL  port=$PORT"
echo

# ── Phase 1: guardrails + cache + logger (unauthenticated) ──────────────────────
echo "Phase 1 — word-filter, max-token, response-cache, request-logger"
start_gw "$TMP/p1.yaml" unauth

call "" "$(req "Reply with exactly: pong")"
check "baseline chat 200 (provider reachable)" "$([ "$CODE" = 200 ] && echo 0 || echo 1)" "got $CODE: $BODY"

call "" "$(req "my secret is hunter2")"
check "word-filter blocks (400)" "$([ "$CODE" = 400 ] && grep -q 'word-filter' <<<"$BODY" && echo 0 || echo 1)" "got $CODE: $BODY"

call "" "$(req "hi" 99999)"
check "max-token blocks over-cap max_tokens (400)" "$([ "$CODE" = 400 ] && grep -q 'max_tokens' <<<"$BODY" && echo 0 || echo 1)" "got $CODE: $BODY"

CACHE_REQ="$(req "cache-probe unique-phrase-42 reply one word" 20)"
call "" "$CACHE_REQ"; id1="$(grep -o '"id":"[^"]*"' <<<"$BODY" | head -1)"
call "" "$CACHE_REQ"; id2="$(grep -o '"id":"[^"]*"' <<<"$BODY" | head -1)"
check "response-cache hit (identical id on repeat)" "$([ -n "$id1" ] && [ "$id1" = "$id2" ] && echo 0 || echo 1)" "id1=$id1 id2=$id2"

logs="$(curl -s -m 5 -H "Authorization: Bearer $MASTER_KEY" "$BASE/admin/logs?limit=20")"
check "request-logger wrote rows to /admin/logs" "$(grep -q '"model"' <<<"$logs" && echo 0 || echo 1)" "no rows: $logs"

stop_gw

# ── Phase 2: budget (authenticated — budget keys on the API credential) ─────────
echo "Phase 2 — budget (authenticated)"
start_gw "$TMP/p2.yaml" auth
call "$MASTER_KEY" "$(req "budget probe one")"
c1="$CODE"
call "$MASTER_KEY" "$(req "budget probe two")"
check "budget allows first, then blocks over-limit (200 → 402)" \
  "$([ "$c1" = 200 ] && [ "$CODE" = 402 ] && grep -q 'budget' <<<"$BODY" && echo 0 || echo 1)" \
  "req1=$c1 req2=$CODE: $BODY"
stop_gw

# ── Phase 3: rate-limit (global rps; unauthenticated is fine) ───────────────────
echo "Phase 3 — rate-limit (rps=2, burst=2)"
start_gw "$TMP/p3.yaml" unauth
RL_REQ="$(req "rl" 5)"
codes=$(for _ in $(seq 1 8); do
          curl -s -o /dev/null -m 40 -w '%{http_code}\n' -H 'Content-Type: application/json' -d "$RL_REQ" "$BASE/v1/chat/completions" &
        done; wait)
n200=$(grep -c '^200$' <<<"$codes" || true); n429=$(grep -c '^429$' <<<"$codes" || true)
check "rate-limit sheds a concurrent burst (some 429, some 200)" \
  "$([ "${n429:-0}" -ge 1 ] && [ "${n200:-0}" -ge 1 ] && echo 0 || echo 1)" \
  "200=$n200 429=$n429 (all: $(tr '\n' ' ' <<<"$codes"))"
stop_gw

echo
echo "== Summary: ${pass} passed, ${fail} failed =="
[ "$fail" -eq 0 ]
