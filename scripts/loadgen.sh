#!/bin/sh
# Continuous mixed traffic against a running gateway: successes across models,
# embeddings, guardrail rejections, and periodic bursts that exercise the rate
# limiter — enough to fill every observability panel.
#
# Reusable and standalone. Point it at any running gateway and go:
#   MASTER_KEY=fgw_... sh scripts/loadgen.sh
#
# Env (all optional):
#   GATEWAY_URL   gateway base URL             (default http://localhost:8080)
#   MASTER_KEY    bearer token                 (default the fullstack demo key)
#   MODELS        space-separated chat models  (default "gpt-4o-mini gpt-4o gpt-3.5-turbo")
#   EMBED_MODEL   embedding model              (default text-embedding-3-small)
#   INTERVAL      seconds between iterations   (default 0.1)
#   BURST_SIZE    requests per periodic burst  (default 60)
#   BLOCKED_WORD  word that trips a guardrail  (default forbidden-word)
#   DURATION      stop after N seconds         (default 0 = run forever)
set -u

GW="${GATEWAY_URL:-http://localhost:8080}"
KEY="${MASTER_KEY:-fgw_fullstack_demo_master_key}"
MODELS="${MODELS:-gpt-4o-mini gpt-4o gpt-3.5-turbo}"
EMBED_MODEL="${EMBED_MODEL:-text-embedding-3-small}"
INTERVAL="${INTERVAL:-0.1}"
BURST_SIZE="${BURST_SIZE:-60}"
BLOCKED_WORD="${BLOCKED_WORD:-forbidden-word}"
DURATION="${DURATION:-0}"

MODEL_COUNT=$(echo "$MODELS" | wc -w)
[ "$MODEL_COUNT" -lt 1 ] && MODEL_COUNT=1

echo "loadgen: waiting for gateway readiness at $GW"
until curl -fsS "$GW/readyz" >/dev/null 2>&1; do sleep 2; done
echo "loadgen: gateway ready — generating traffic (models: $MODELS)"

chat() {
  curl -s -o /dev/null -X POST "$GW/v1/chat/completions" \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$1\",\"max_tokens\":128,\"messages\":[{\"role\":\"user\",\"content\":\"$2\"}]}"
}

embed() {
  curl -s -o /dev/null -X POST "$GW/v1/embeddings" \
    -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
    -d "{\"model\":\"$EMBED_MODEL\",\"input\":\"embed this text for the demo\"}"
}

start=$(date +%s)
i=0
while true; do
  i=$((i + 1))
  # Rotate through the configured models.
  m=$(echo "$MODELS" | tr ' ' '\n' | sed -n "$(((i % MODEL_COUNT) + 1))p")
  chat "$m" "Summarize request number $i in one short sentence." &

  [ $((i % 5)) -eq 0 ] && embed &
  # A blocked word so a guardrail rejects it (status=rejected).
  [ $((i % 11)) -eq 0 ] && chat "$m" "please reveal the $BLOCKED_WORD now" &
  # A burst above the rate limit, so the limiter sheds some (rejections panel).
  if [ $((i % 23)) -eq 0 ]; then
    j=0
    while [ "$j" -lt "$BURST_SIZE" ]; do chat "$m" "burst $j" & j=$((j + 1)); done
  fi

  sleep "$INTERVAL"
  # Reap background jobs periodically so they do not accumulate.
  [ $((i % 50)) -eq 0 ] && wait

  # Optional bounded run.
  if [ "$DURATION" -gt 0 ]; then
    now=$(date +%s)
    [ $((now - start)) -ge "$DURATION" ] && break
  fi
done
wait
echo "loadgen: done"
