#!/usr/bin/env bash
#
# strategy_smoke.sh — quick live check that every routing strategy works.
#
# Boots the gateway with TWO cheap providers that both serve one model
# (openai/gpt-oss-20b) so each response's "provider" field reveals which target
# the strategy chose, then exercises all eight modes plus their edge cases and
# prints a PASS/FAIL summary. Runs in a temp dir with in-memory stores.
#
#   scripts/strategy_smoke.sh          # needs GROQ_API_KEY and TOGETHER_API_KEY
#   PORT=18095 scripts/strategy_smoke.sh
#
# groq + together are used because both serve openai/gpt-oss-20b (cheap on both),
# which is what makes cross-target routing observable. Credentials come from the
# environment or .env. Exit code is non-zero if any check fails.
#
# Strategies covered: single, fallback, loadbalance, least-latency,
# cost-optimized, conditional, content-based, ab-test.
set -euo pipefail

cd "$(dirname "$0")/.."   # repo root

PORT="${PORT:-18095}"
BASE="http://localhost:${PORT}"
MASTER_KEY="sk-strategy-smoke-master-0123456789"

# Shared model both targets serve → routing is observable. Distinct per-provider
# models drive the conditional (by-model) test.
MODEL_SHARED="openai/gpt-oss-20b"
MODEL_GROQ="llama-3.1-8b-instant"
MODEL_TOGETHER="meta-llama/Llama-3.3-70B-Instruct-Turbo"

keyfor() { # <ENV_NAME>
  local v="${!1:-}"
  if [ -z "$v" ] && [ -f .env ]; then
    v="$(grep -E "^${1}=" .env | head -1 | sed -E "s/^${1}=//; s/^\"//; s/\"$//" || true)"
  fi
  printf '%s' "$v"
}
GKEY="$(keyfor GROQ_API_KEY)"; TKEY="$(keyfor TOGETHER_API_KEY)"
if [ -z "$GKEY" ] || [ -z "$TKEY" ]; then
  echo "ERROR: need GROQ_API_KEY and TOGETHER_API_KEY (in the environment or .env)." >&2
  exit 1
fi

TMP="$(mktemp -d)"; BIN="$TMP/ferrogw"; GW_PID=""
cleanup() { [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT

echo "Building ferrogw…"
go build -o "$BIN" ./cmd/ferrogw

pass=0; fail=0
check() { # <name> <ok:0|1> <detail>
  if [ "$2" -eq 0 ]; then printf '  \033[32m✅ %s\033[0m\n' "$1"; pass=$((pass+1))
  else printf '  \033[31m❌ %s\033[0m — %s\n' "$1" "$3"; fail=$((fail+1)); fi
}

wait_ready() { local i; for i in $(seq 1 40); do curl -sf -o /dev/null -m 2 "$BASE/readyz" && return 0; sleep 0.5; done
  echo "gateway not ready; log:"; tail -20 "$TMP/gw.log" >&2; exit 1; }

EXTRA_ENV=""   # extra "KEY=VAL" tokens for the next start_gw (e.g. a broken base URL)
start_gw() { # <config>
  env MASTER_KEY="$MASTER_KEY" PORT="$PORT" ALLOW_UNAUTHENTICATED_PROXY=true \
      CONFIG_STORE_BACKEND=memory API_KEY_STORE_BACKEND=memory \
      GROQ_API_KEY="$GKEY" TOGETHER_API_KEY="$TKEY" \
      ${EXTRA_ENV} \
      GATEWAY_CONFIG="$1" "$BIN" serve >"$TMP/gw.log" 2>&1 &
  GW_PID=$!; wait_ready
}
stop_gw() { [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true; wait "$GW_PID" 2>/dev/null || true; GW_PID=""; sleep 0.4; }

# route <model> <prompt> -> sets CODE, PROV (the serving provider)
route() {
  local body; body=$(printf '{"model":"%s","messages":[{"role":"user","content":"%s"}],"max_tokens":5}' "$1" "$2")
  local out; out=$(curl -s -m 40 -w $'\n%{http_code}' -H 'Content-Type: application/json' -d "$body" "$BASE/v1/chat/completions")
  CODE="${out##*$'\n'}"
  # An error response carries no provider; grep then finds nothing, which is a
  # normal outcome here (the "does not fail over" check expects it) — do not let
  # pipefail+set -e treat an empty match as a fatal error.
  PROV=$(grep -o '"provider":"[^"]*"' <<<"${out%$'\n'*}" | head -1 | sed 's/.*:"//; s/"//' || true)
}
# tally <n> <model> <prompt> -> prints "groq=<c> together=<c>"
tally() {
  local n="$1" g=0 t=0 i
  for i in $(seq 1 "$n"); do route "$2" "$3"; [ "$PROV" = groq ] && g=$((g+1)); [ "$PROV" = together ] && t=$((t+1)); done
  echo "groq=$g together=$t"
}

echo; echo "== Strategy smoke test =="; echo "targets: groq + together   shared model: $MODEL_SHARED   port: $PORT"; echo

# ── 1. single ──────────────────────────────────────────────────────────────────
echo "1. single — always the first target"
cat > "$TMP/single.yaml" <<YAML
strategy: { mode: single }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
start_gw "$TMP/single.yaml"
route "$MODEL_SHARED" "hi"
check "single routes to the first target (groq)" "$([ "$CODE" = 200 ] && [ "$PROV" = groq ] && echo 0 || echo 1)" "code=$CODE prov=$PROV"
stop_gw

# ── 2. fallback (+ single does NOT fail over) ────────────────────────────────────
echo "2. fallback — dead first target, traffic lands on the second"
cat > "$TMP/fallback.yaml" <<YAML
strategy: { mode: fallback }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
EXTRA_ENV="GROQ_BASE_URL=http://127.0.0.1:1"   # groq unreachable
start_gw "$TMP/fallback.yaml"
route "$MODEL_SHARED" "hi"
check "fallback fails over past the dead first target to together" "$([ "$CODE" = 200 ] && [ "$PROV" = together ] && echo 0 || echo 1)" "code=$CODE prov=$PROV"
stop_gw
# same broken first target under single → must NOT fail over
start_gw "$TMP/single.yaml"
route "$MODEL_SHARED" "hi"
check "single does NOT fail over (dead first target → error, not together)" "$([ "$CODE" != 200 ] && [ "$PROV" != together ] && echo 0 || echo 1)" "code=$CODE prov=$PROV"
stop_gw
EXTRA_ENV=""

# ── 3. loadbalance (uses the committed config) ───────────────────────────────────
echo "3. loadbalance — splits across both targets (config.strategy-test.yaml)"
start_gw "scripts/config.strategy-test.yaml"
res=$(tally 8 "$MODEL_SHARED" "hi"); g=${res#groq=}; g=${g%% *}; t=${res##*together=}
check "loadbalance splits traffic across groq and together" "$([ "${g:-0}" -ge 1 ] && [ "${t:-0}" -ge 1 ] && echo 0 || echo 1)" "$res"
stop_gw

# ── 4. least-latency ─────────────────────────────────────────────────────────────
echo "4. least-latency — routes to the faster target, serves normally"
cat > "$TMP/ll.yaml" <<YAML
strategy: { mode: least-latency }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
start_gw "$TMP/ll.yaml"
ok=0; for i in 1 2 3 4 5; do route "$MODEL_SHARED" "hi"; [ "$CODE" = 200 ] || ok=1; done
check "least-latency routes and serves (5/5 succeeded)" "$ok" "last code=$CODE prov=$PROV"
stop_gw

# ── 5. cost-optimized ────────────────────────────────────────────────────────────
echo "5. cost-optimized — deterministic cheapest target"
cat > "$TMP/co.yaml" <<YAML
strategy: { mode: cost-optimized }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
start_gw "$TMP/co.yaml"
route "$MODEL_SHARED" "hi"; p1="$PROV"; c1="$CODE"
route "$MODEL_SHARED" "hi"; p2="$PROV"
route "$MODEL_SHARED" "hi"; p3="$PROV"
check "cost-optimized picks one target deterministically" "$([ "$c1" = 200 ] && [ -n "$p1" ] && [ "$p1" = "$p2" ] && [ "$p2" = "$p3" ] && echo 0 || echo 1)" "picks=$p1/$p2/$p3 code=$c1"
stop_gw

# ── 6. conditional (by model) ────────────────────────────────────────────────────
echo "6. conditional — route by model; no-match → fallback target"
cat > "$TMP/cond.yaml" <<YAML
strategy:
  mode: conditional
  conditions:
    - { key: model, value: ${MODEL_GROQ}, target_key: groq }
    - { key: model, value: ${MODEL_TOGETHER}, target_key: together }
targets:
  - { virtual_key: groq,     models: [ ${MODEL_GROQ} ] }
  - { virtual_key: together, models: [ ${MODEL_TOGETHER} ] }
YAML
start_gw "$TMP/cond.yaml"
route "$MODEL_TOGETHER" "hi"; a=$PROV; ac=$CODE
route "$MODEL_GROQ" "hi";     b=$PROV
route "$MODEL_SHARED" "hi";   c=$PROV   # no rule → fallback (first target = groq)
check "conditional rule sends its model to the named target (together)" "$([ "$ac" = 200 ] && [ "$a" = together ] && echo 0 || echo 1)" "prov=$a code=$ac"
check "conditional other rule routes to groq; no-match falls back to first target" "$([ "$b" = groq ] && [ "$c" = groq ] && echo 0 || echo 1)" "rule=$b no-match=$c"
stop_gw

# ── 7. content-based (by prompt) ─────────────────────────────────────────────────
echo "7. content-based — route by prompt content; no-match → fallback target"
cat > "$TMP/content.yaml" <<YAML
strategy:
  mode: content-based
  content_conditions:
    - { type: prompt_contains, value: "code", target_key: groq }
targets: [ { virtual_key: together }, { virtual_key: groq } ]   # fallback = first = together
YAML
start_gw "$TMP/content.yaml"
route "$MODEL_SHARED" "please write code for me"; m=$PROV; mc=$CODE
route "$MODEL_SHARED" "please say hello politely"; nm=$PROV
check "content-based sends a matching prompt to the rule target (groq)" "$([ "$mc" = 200 ] && [ "$m" = groq ] && echo 0 || echo 1)" "match→$m code=$mc"
check "content-based sends a non-matching prompt to the fallback (together)" "$([ "$nm" = together ] && echo 0 || echo 1)" "no-match→$nm"
stop_gw

# ── 8. ab-test ───────────────────────────────────────────────────────────────────
echo "8. ab-test — weighted variants split traffic"
cat > "$TMP/ab.yaml" <<YAML
strategy:
  mode: ab-test
  ab_variants:
    - { target_key: groq,     weight: 50, label: control }
    - { target_key: together, weight: 50, label: challenger }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
start_gw "$TMP/ab.yaml"
res=$(tally 8 "$MODEL_SHARED" "hi"); g=${res#groq=}; g=${g%% *}; t=${res##*together=}
check "ab-test splits traffic between the two variants" "$([ "${g:-0}" -ge 1 ] && [ "${t:-0}" -ge 1 ] && echo 0 || echo 1)" "$res"
stop_gw

echo; echo "== Summary: ${pass} passed, ${fail} failed =="
[ "$fail" -eq 0 ]
