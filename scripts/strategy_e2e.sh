#!/usr/bin/env bash
#
# strategy_e2e.sh — deterministic end-to-end check of every routing strategy,
# with no provider keys.
#
# Three scriptable mock upstreams (scripts/mockllm) stand in for groq, together
# and openai. Each scenario tells one of them to fail, slow down, or recover
# before the request is sent; the response's "provider" field and the mocks'
# own call counters then show what the strategy did, over the real binary and
# the real HTTP surfaces (unary, SSE, embeddings, /v1/models, /metrics).
#
#   scripts/strategy_e2e.sh              # about a minute, no credentials
#   E2E_SLOW=1 scripts/strategy_e2e.sh   # adds the hung-target cell (~20s)
#   PORT=18096 MOCK_PORT_BASE=19100 scripts/strategy_e2e.sh
#
# scripts/strategy_smoke.sh is the live counterpart against real providers.
# Exit code is non-zero if any check fails.
set -euo pipefail

cd "$(dirname "$0")/.."   # repo root

PORT="${PORT:-18096}"
BASE="http://127.0.0.1:${PORT}"
MOCK_PORT_BASE="${MOCK_PORT_BASE:-19100}"
MASTER_KEY="sk-strategy-e2e-master-0123456789"

# Priced on both groq (0.075/M) and together (0.05/M) in the embedded catalog,
# so cost-optimized has a real, deterministic answer.
SHARED="openai/gpt-oss-20b"
# A visible key each target maps to a different upstream id (targets[].model_map).
SMART="smart"
# Priced differently per target once mapped: groq 0.05/M, together 0.18/M.
FAST="fast"
EMBED="mock-embed"

TMP="$(mktemp -d)"; GW="$TMP/ferrogw"; MOCK="$TMP/mockllm"; GW_PID=""
declare -A MOCK_PID MOCK_PORT
cleanup() {
  [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true
  for p in "${MOCK_PID[@]-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done
  rm -rf "$TMP"
}
trap cleanup EXIT

pass=0; fail=0
check() { # <name> <ok:0|1> <detail>
  if [ "$2" -eq 0 ]; then printf '  \033[32m✅ %s\033[0m\n' "$1"; pass=$((pass+1))
  else printf '  \033[31m❌ %s\033[0m — %s\n' "$1" "$3"; fail=$((fail+1)); fi
}
section() { echo; echo "$1"; }

# ── mocks ───────────────────────────────────────────────────────────────────────
mock_url() { printf 'http://127.0.0.1:%s' "${MOCK_PORT[$1]}"; }
scenario() { # <mock> <json>  — set the mock's behaviour; {} is healthy and deterministic
  local code; code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d "$2" "$(mock_url "$1")/_mock/scenario")
  [ "$code" = 204 ] || { echo "scenario $1 $2 -> HTTP $code" >&2; exit 1; }
}
heal() { scenario "$1" '{}'; }
reset_mock() { curl -s -o /dev/null -X POST "$(mock_url "$1")/_mock/reset"; heal "$1"; }
reset_mocks() { local m; for m in "${!MOCK_PORT[@]}"; do reset_mock "$m"; done; }
calls() { curl -s "$(mock_url "$1")/_mock/calls" | sed -n 's/.*"calls":\([0-9]*\).*/\1/p'; }
last_model() { curl -s "$(mock_url "$1")/_mock/calls" | sed -n 's/.*"last_model":"\([^"]*\)".*/\1/p'; }
start_mock() { # <name> <port>
  MOCK_PORT[$1]=$2
  MOCK_NAME="$1" PORT="$2" MOCK_ERROR_PCT=0 MOCK_RATE_LIMIT_PCT=0 MOCK_LATENCY_MIN_MS=0 MOCK_LATENCY_MAX_MS=1 \
    "$MOCK" >"$TMP/mock-$1.log" 2>&1 &
  MOCK_PID[$1]=$!
  local i; for i in $(seq 1 50); do curl -sf -o /dev/null "http://127.0.0.1:$2/healthz" && break; sleep 0.1; done
  heal "$1"
}

# ── gateway ─────────────────────────────────────────────────────────────────────
wait_ready() { local i; for i in $(seq 1 60); do curl -sf -o /dev/null -m 2 "$BASE/readyz" && return 0; sleep 0.25; done
  echo "gateway not ready; log:" >&2; tail -30 "$TMP/gw.log" >&2; exit 1; }
start_gw() { # <config-file>  — every provider points at its mock; no key is real
  env MASTER_KEY="$MASTER_KEY" PORT="$PORT" ALLOW_UNAUTHENTICATED_PROXY=true RATE_LIMIT_RPS=0 \
      CONFIG_STORE_BACKEND=memory API_KEY_STORE_BACKEND=memory FERRO_MODEL_CATALOG_TIMEOUT=0 \
      GROQ_API_KEY=mock     GROQ_BASE_URL="$(mock_url groq)" \
      TOGETHER_API_KEY=mock TOGETHER_BASE_URL="$(mock_url together)" \
      OPENAI_API_KEY=mock   OPENAI_BASE_URL="$(mock_url openai)" \
      GATEWAY_CONFIG="$1" "$GW" serve >"$TMP/gw.log" 2>&1 &
  GW_PID=$!; wait_ready; reset_mocks
}
stop_gw() { [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true; wait "$GW_PID" 2>/dev/null || true; GW_PID=""; }
config() { # <name>  — writes stdin to a config file and prints its path
  cat >"$TMP/$1.yaml"; printf '%s' "$TMP/$1.yaml"
}

# ── requests ────────────────────────────────────────────────────────────────────
field() { grep -o "\"$1\":\"[^\"]*\"" | head -1 | sed 's/^[^:]*:"//; s/"$//' || true; }
route() { # <model> <prompt> -> CODE PROV RMODEL ECODE
  local body; body=$(printf '{"model":"%s","messages":[{"role":"user","content":"%s"}],"max_tokens":5}' "$1" "$2")
  local out; out=$(curl -s -m 30 -w $'\n%{http_code}' -H 'Content-Type: application/json' -d "$body" "$BASE/v1/chat/completions")
  CODE="${out##*$'\n'}"; local resp="${out%$'\n'*}"
  PROV=$(field provider <<<"$resp"); RMODEL=$(field model <<<"$resp"); ECODE=$(field code <<<"$resp")
}
stream() { # <model> <prompt> -> CODE SCONTENT SMODEL SDONE SERR
  local body; body=$(printf '{"model":"%s","stream":true,"messages":[{"role":"user","content":"%s"}]}' "$1" "$2")
  local out; out=$(curl -s -N -m 30 -w $'\n%{http_code}' -H 'Content-Type: application/json' -d "$body" "$BASE/v1/chat/completions")
  CODE="${out##*$'\n'}"; local frames="${out%$'\n'*}"
  SCONTENT=$(grep -o '"content":"[^"]*"' <<<"$frames" | sed 's/^[^:]*:"//; s/"$//' | tr -d '\n' || true)
  SMODEL=$(field model <<<"$frames")
  SDONE=$(grep -c '^data: \[DONE\]' <<<"$frames" || true)
  SERR=$(grep -c '"error"' <<<"$frames" || true)
}
embed() { # <model> -> CODE
  CODE=$(curl -s -o /dev/null -m 30 -w '%{http_code}' -H 'Content-Type: application/json' \
    -d "{\"model\":\"$1\",\"input\":\"hi\"}" "$BASE/v1/embeddings")
}
tally() { # <n> <model> -> T_groq T_together T_openai T_err
  T_groq=0; T_together=0; T_openai=0; T_err=0
  local i; for i in $(seq 1 "$1"); do route "$2" "hi"
    case "$PROV" in groq) T_groq=$((T_groq+1));; together) T_together=$((T_together+1));; openai) T_openai=$((T_openai+1));; *) T_err=$((T_err+1));; esac
  done
}
within() { # <count> <total> <pct> <tolerance> -> 0|1
  local pct=$(( 100 * $1 / $2 ))
  [ "$pct" -ge $(( $3 - $4 )) ] && [ "$pct" -le $(( $3 + $4 )) ] && echo 0 || echo 1
}
metric() { curl -s -H "Authorization: Bearer $MASTER_KEY" "$BASE/metrics" | grep -F "$1" | head -1 | awk '{print $NF}'; }
breaker() { metric "gateway_circuit_breaker_state{provider=\"$1\"}"; }
models_listing() { curl -s "$BASE/v1/models" | grep -o "\"id\":\"$1\"" | wc -l | tr -d ' '; }

# ── build and boot the mocks ─────────────────────────────────────────────────────
echo "Building ferrogw and mockllm…"
go build -o "$GW" ./cmd/ferrogw
(cd scripts/mockllm && go build -o "$MOCK" .)
start_mock groq     "$MOCK_PORT_BASE"
start_mock together "$((MOCK_PORT_BASE+1))"
start_mock openai   "$((MOCK_PORT_BASE+2))"

echo; echo "== Strategy end-to-end (mock upstreams: groq, together, openai) =="

# ── 1. single ────────────────────────────────────────────────────────────────────
section "1. single — exactly one target, never a sibling"
start_gw "$(config single <<YAML
strategy: { mode: single }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
)"
route "$SHARED" hi
check "single serves from the first target" "$([ "$CODE" = 200 ] && [ "$PROV" = groq ] && echo 0 || echo 1)" "code=$CODE prov=$PROV"
scenario groq '{"status":503}'
route "$SHARED" hi
check "single reports the first target's failure instead of moving on" \
  "$([ "$CODE" = 502 ] && [ "$(calls together)" = 0 ] && echo 0 || echo 1)" "code=$CODE together_calls=$(calls together)"
stop_gw

# ── 2. fallback: failure classification ──────────────────────────────────────────
# No breaker here on purpose: each scenario must see a fresh primary, and a
# breaker would carry the previous scenario's failures into the next one.
section "2. fallback — declared order, failover on transient failures only"
start_gw "$(config fallback <<YAML
strategy: { mode: fallback }
targets:
  - virtual_key: groq
    retry: { attempts: 2, initial_backoff_ms: 1 }
  - virtual_key: together
YAML
)"
scenario groq '{"status":503}'
route "$SHARED" hi
check "503 on the primary: retried once there, then served by the sibling" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = together ] && [ "$(calls groq)" = 2 ] && [ "$(calls together)" = 1 ] && echo 0 || echo 1)" \
  "code=$CODE prov=$PROV groq_calls=$(calls groq) together_calls=$(calls together)"
check "the masked primary failure still counts on gateway_provider_errors_total" \
  "$([ "$(metric 'gateway_provider_errors_total{error_type="provider_error",provider="groq"}' )" != "" ] && echo 0 || echo 1)" "no series"

reset_mocks; scenario groq '{"status":401}'
route "$SHARED" hi
check "401 on the primary is deterministic: reported as upstream_auth_error, sibling never asked" \
  "$([ "$CODE" = 502 ] && [ "$ECODE" = upstream_auth_error ] && [ "$(calls together)" = 0 ] && echo 0 || echo 1)" \
  "code=$CODE ecode=$ECODE together_calls=$(calls together)"

reset_mocks; scenario groq '{"status":429,"retry_after_s":1}'
t0=$(date +%s%N); route "$SHARED" hi; elapsed_ms=$(( ($(date +%s%N) - t0) / 1000000 ))
check "429 with Retry-After: the wait is honoured before the retry, then the sibling serves" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = together ] && [ "$(calls groq)" = 2 ] && [ "$elapsed_ms" -ge 900 ] && echo 0 || echo 1)" \
  "code=$CODE prov=$PROV groq_calls=$(calls groq) elapsed=${elapsed_ms}ms"

reset_mocks; scenario groq '{"status":503}'
stream "$SHARED" hi
check "stream start fails over: the sibling's stream reaches the client and ends with [DONE]" \
  "$([ "$CODE" = 200 ] && [ "$SCONTENT" = "ok from together" ] && [ "$SDONE" = 1 ] && echo 0 || echo 1)" \
  "code=$CODE content=$SCONTENT done=$SDONE"
check "streamed chunks carry the routed model, not the upstream's" "$([ "$SMODEL" = "$SHARED" ] && echo 0 || echo 1)" "model=$SMODEL"

# The 429 above parked groq for its Retry-After (1s); this cell needs groq to
# serve, so wait the park out.
sleep 1.2
reset_mocks; scenario groq '{"stream_fail_after":1}'
stream "$SHARED" hi
check "a stream that fails after its first byte ends with an error frame and is not failed over" \
  "$([ "$SERR" -ge 1 ] && [ "$SDONE" = 0 ] && [ "$(calls together)" = 0 ] && echo 0 || echo 1)" \
  "err_frames=$SERR done=$SDONE together_calls=$(calls together)"

if [ "${E2E_SLOW:-0}" = 1 ]; then
  reset_mocks; scenario groq '{"hang":true}'
  t0=$(date +%s); route "$SHARED" hi; took=$(( $(date +%s) - t0 ))
  check "a hung primary times out at the transport and the sibling serves (E2E_SLOW)" \
    "$([ "$CODE" = 200 ] && [ "$PROV" = together ] && echo 0 || echo 1)" "code=$CODE prov=$PROV took=${took}s"
fi
stop_gw

# ── 2b. fallback: circuit breaker ────────────────────────────────────────────────
section "2b. fallback + circuit breaker — open, skipped, half-open, closed"
start_gw "$(config fallback-breaker <<YAML
strategy: { mode: fallback }
targets:
  - virtual_key: groq
    retry: { attempts: 2, initial_backoff_ms: 1 }
    circuit_breaker: { failure_threshold: 3, success_threshold: 1, timeout: "1s" }
  - virtual_key: together
YAML
)"
scenario groq '{"status":503,"fail_count":100}'
route "$SHARED" hi; route "$SHARED" hi   # 2 requests × 2 attempts = 4 failures ≥ threshold 3
groq_before=$(calls groq)
route "$SHARED" hi
check "circuit opens after the failure threshold: the sibling serves without the primary being called" \
  "$([ "$(breaker groq)" = 1 ] && [ "$(calls groq)" = "$groq_before" ] && [ "$PROV" = together ] && echo 0 || echo 1)" \
  "breaker=$(breaker groq) groq_calls_before=$groq_before after=$(calls groq) prov=$PROV"
check "/v1/models still lists the model exactly once while the circuit is open" \
  "$([ "$(models_listing "$SHARED")" = 1 ] && echo 0 || echo 1)" "listed=$(models_listing "$SHARED")"
heal groq; sleep 1.3
route "$SHARED" hi
check "half-open probe after the breaker timeout: a healed primary takes traffic back" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = groq ] && [ "$(breaker groq)" = 0 ] && echo 0 || echo 1)" \
  "code=$CODE prov=$PROV breaker=$(breaker groq)"
stop_gw

# ── 3. loadbalance ───────────────────────────────────────────────────────────────
section "3. loadbalance — weights are probabilities, 0 is drained, an open circuit redistributes"
start_gw "$(config lb <<YAML
strategy: { mode: loadbalance }
targets:
  - virtual_key: groq
    weight: 3
    circuit_breaker: { failure_threshold: 2, timeout: "30s" }
  - { virtual_key: together, weight: 1 }
YAML
)"
tally 400 "$SHARED"
check "3:1 weights give the primary about 75% of 400 requests (±8)" "$(within "$T_groq" 400 75 8)" "groq=$T_groq together=$T_together errors=$T_err"
reset_mocks; scenario groq '{"status":503,"fail_count":100}'
tally 6 "$SHARED"; reset_mocks   # trip the breaker (threshold 2), then count clean
tally 40 "$SHARED"
check "with the primary's circuit open, every request lands on the sibling" \
  "$([ "$T_together" = 40 ] && [ "$(calls groq)" = 0 ] && echo 0 || echo 1)" "groq=$T_groq together=$T_together groq_calls=$(calls groq)"
stop_gw
start_gw "$(config lb-drained <<YAML
strategy: { mode: loadbalance }
targets: [ { virtual_key: groq, weight: 1 }, { virtual_key: together, weight: 0 } ]
YAML
)"
tally 40 "$SHARED"
check "a weight-0 target is drained: 40/40 on the other" "$([ "$T_groq" = 40 ] && [ "$(calls together)" = 0 ] && echo 0 || echo 1)" "groq=$T_groq together_calls=$(calls together)"
stop_gw

# ── 4. least-latency ─────────────────────────────────────────────────────────────
section "4. least-latency — the faster target wins after both are profiled"
start_gw "$(config ll <<YAML
strategy: { mode: least-latency }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
)"
scenario together '{"delay_ms":120}'
tally 100 "$SHARED"
# One request in ten explores a sampled non-leader (see least-latency in
# internal/strategies/README.md), so the fast target serves about 90%; 100
# draws keep the 80% bar clear of that draw's own variance.
check "a slower sibling receives its profiling request and the exploration share; the fast target serves ≥ 80%" \
  "$([ "$T_groq" -ge 80 ] && [ "$T_err" = 0 ] && echo 0 || echo 1)" "groq=$T_groq together=$T_together errors=$T_err"
heal together; scenario groq '{"delay_ms":120}'
tally 60 "$SHARED"
# The leader's window is 100 samples, so 60 slow ones cannot turn it over here;
# what this cell can prove is that the slowed leader no longer takes everything
# — exploration keeps measuring the healed sibling, which is what lets the
# flip happen (TestLeastLatency_SlowedLeaderLosesLeadership proves the flip).
check "a leader that slows down is still explored past: the healed sibling keeps receiving samples" \
  "$([ "$T_together" -ge 2 ] && echo 0 || echo 1)" "groq=$T_groq together=$T_together"
stop_gw

# ── 5. cost-optimized ────────────────────────────────────────────────────────────
section "5. cost-optimized — the cheapest catalog price wins, mapped models are priced per target"
start_gw "$(config cost <<YAML
strategy: { mode: cost-optimized }
targets:
  - virtual_key: groq
    model_map: { ${FAST}: llama-3.1-8b-instant }
  - virtual_key: together
    model_map: { ${FAST}: meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo }
YAML
)"
tally 5 "$SHARED"
check "${SHARED}: together (0.05/M) is chosen over groq (0.075/M), deterministically" \
  "$([ "$T_together" = 5 ] && echo 0 || echo 1)" "groq=$T_groq together=$T_together errors=$T_err"
scenario together '{"status":503}'
route "$SHARED" hi
check "with the cheapest target failing, the next-cheapest serves" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = groq ] && echo 0 || echo 1)" "code=$CODE prov=$PROV"
reset_mocks
route "$FAST" hi
check "${FAST} via model_map: groq's mapped id (0.05/M) beats together's (0.18/M) and the mapped id is what reaches groq" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = groq ] && [ "$(last_model groq)" = "llama-3.1-8b-instant" ] && echo 0 || echo 1)" \
  "code=$CODE prov=$PROV groq_last_model=$(last_model groq)"
check "the client sees the visible key as the response model" "$([ "$RMODEL" = "$FAST" ] && echo 0 || echo 1)" "model=$RMODEL"
stop_gw

# ── 6. conditional ───────────────────────────────────────────────────────────────
section "6. conditional — a rule names one target; no match falls back to the first"
start_gw "$(config cond <<YAML
strategy:
  mode: conditional
  conditions:
    - { key: model, value: llama-3.1-8b-instant, target_key: groq }
    - { key: model_prefix, value: "meta-llama/", target_key: together }
targets:
  - { virtual_key: groq,     models: [ llama-3.1-8b-instant ] }
  - { virtual_key: together, models: [ meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo ] }
YAML
)"
route "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo" hi; a=$PROV; ac=$CODE
route "llama-3.1-8b-instant" hi; b=$PROV
route "$SHARED" hi; c=$PROV
check "model rule and model_prefix rule each reach their named target; no match → first target" \
  "$([ "$ac" = 200 ] && [ "$a" = together ] && [ "$b" = groq ] && [ "$c" = groq ] && echo 0 || echo 1)" "prefix→$a model→$b nomatch→$c"
reset_mocks; scenario together '{"status":503}'
route "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo" hi
check "a failing named target is reported, never rerouted to a sibling" \
  "$([ "$CODE" = 502 ] && [ "$(calls groq)" = 0 ] && echo 0 || echo 1)" "code=$CODE groq_calls=$(calls groq)"
stop_gw

# ── 7. content-based ─────────────────────────────────────────────────────────────
section "7. content-based — the prompt picks the target; streams follow the same rule"
start_gw "$(config content <<YAML
strategy:
  mode: content-based
  content_conditions:
    - { type: prompt_contains, value: "code", target_key: together }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
)"
route "$SHARED" "please write code"; m=$PROV
route "$SHARED" "please say hello"; nm=$PROV
stream "$SHARED" "write code please"
check "matching prompt → rule target; other prompt → first target; a matching stream follows the rule" \
  "$([ "$m" = together ] && [ "$nm" = groq ] && [ "$SCONTENT" = "ok from together" ] && echo 0 || echo 1)" "match→$m nomatch→$nm stream→$SCONTENT"
reset_mocks; scenario together '{"status":503}'
route "$SHARED" "write code"
check "a failing rule target is reported, never rerouted" "$([ "$CODE" = 502 ] && [ "$(calls groq)" = 0 ] && echo 0 || echo 1)" "code=$CODE groq_calls=$(calls groq)"
stop_gw

# ── 8. ab-test ───────────────────────────────────────────────────────────────────
section "8. ab-test — weighted variants, drained variant, failover keeps the request in the pool"
start_gw "$(config ab <<YAML
strategy:
  mode: ab-test
  ab_variants:
    - { target_key: groq,     weight: 50, label: control }
    - { target_key: together, weight: 50, label: challenger }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
)"
tally 400 "$SHARED"
check "50/50 variants split 400 requests evenly (±8)" "$(within "$T_groq" 400 50 8)" "groq=$T_groq together=$T_together errors=$T_err"
stop_gw
start_gw "$(config ab-drained <<YAML
strategy:
  mode: ab-test
  ab_variants:
    - { target_key: groq,     weight: 100, label: control }
    - { target_key: together, weight: 0,   label: challenger }
targets: [ { virtual_key: groq }, { virtual_key: together } ]
YAML
)"
tally 40 "$SHARED"
check "a weight-0 variant is drained: 40/40 on the other" "$([ "$T_groq" = 40 ] && echo 0 || echo 1)" "groq=$T_groq together=$T_together"
scenario groq '{"status":503}'
route "$SHARED" hi
check "a failing variant fails over inside the pool (intent-to-treat: the drawn label stays on the request)" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = together ] && echo 0 || echo 1)" "code=$CODE prov=$PROV"
stop_gw

# ── 9. model_map across providers ────────────────────────────────────────────────
section "9. model_map — one visible key, a different upstream id per target, on unary and stream"
start_gw "$(config map <<YAML
strategy: { mode: fallback }
targets:
  - virtual_key: openai
    model_map: { ${SMART}: gpt-4.1-nano }
  - virtual_key: groq
    model_map: { ${SMART}: ${SHARED} }
YAML
)"
route "$SMART" hi
check "the primary receives its mapped id; the client gets the visible key back" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = openai ] && [ "$(last_model openai)" = gpt-4.1-nano ] && [ "$RMODEL" = "$SMART" ] && echo 0 || echo 1)" \
  "code=$CODE prov=$PROV openai_last_model=$(last_model openai) model=$RMODEL"
stream "$SMART" hi
check "streamed chunks name the visible key too" "$([ "$CODE" = 200 ] && [ "$SMODEL" = "$SMART" ] && [ "$SDONE" = 1 ] && echo 0 || echo 1)" "model=$SMODEL done=$SDONE"
scenario openai '{"status":503}'
route "$SMART" hi
check "failover applies the sibling's own mapping" \
  "$([ "$CODE" = 200 ] && [ "$PROV" = groq ] && [ "$(last_model groq)" = "$SHARED" ] && [ "$RMODEL" = "$SMART" ] && echo 0 || echo 1)" \
  "code=$CODE prov=$PROV groq_last_model=$(last_model groq) model=$RMODEL"
check "/v1/models advertises the visible key exactly once" "$([ "$(models_listing "$SMART")" = 1 ] && echo 0 || echo 1)" "listed=$(models_listing "$SMART")"
stop_gw

# ── 10. embeddings share the walk ────────────────────────────────────────────────
section "10. embeddings — the same walk, the same failover"
start_gw "$(config embed <<YAML
strategy: { mode: fallback }
targets:
  - { virtual_key: groq,     models: [ ${EMBED} ] }
  - { virtual_key: together, models: [ ${EMBED} ] }
YAML
)"
scenario groq '{"status":503}'
embed "$EMBED"
check "an embeddings request fails over past a 503 primary" \
  "$([ "$CODE" = 200 ] && [ "$(calls together)" = 1 ] && echo 0 || echo 1)" "code=$CODE together_calls=$(calls together)"
stop_gw

echo; echo "== Summary: ${pass} passed, ${fail} failed =="
[ "$fail" -eq 0 ]
