#!/usr/bin/env bash
#
# mcp_smoke.sh — quick live check that MCP tool-calling works end to end.
#
# Wires real MCP servers to a real (cheap) LLM and asks questions that can only
# be answered by calling a tool, so a correct answer proves the whole loop:
# tools injected → the model calls one → the gateway runs it over MCP → the
# result is fed back → the model answers. Makes REAL network calls (npx fetches
# the MCP servers; the LLM is a live provider). Prints a PASS/FAIL summary.
#
#   scripts/mcp_smoke.sh                    # needs OPENAI_API_KEY + node/npx
#   PROVIDER=groq MODEL=llama-3.3-70b-versatile scripts/mcp_smoke.sh
#
# Covers three MCP server kinds: stdio "everything" (compute/echo), stdio
# "filesystem" (reads a file), and an HTTP (Streamable HTTP) transport — plus the
# allowed_tools filter and required-server readiness gating.
#
# The default is openai gpt-4.1-nano: a tool-calling loop injects every server's
# tool schema on every turn, which is token-heavy — a free tier (e.g. groq) hits
# its per-minute token limit and 429s before it can call a tool. gpt-4.1-nano is
# a few hundredths of a cent for the whole run and does not rate-limit.
set -euo pipefail

cd "$(dirname "$0")/.."

PROVIDER="${PROVIDER:-openai}"
MODEL="${MODEL:-gpt-4.1-nano}"
PORT="${PORT:-18102}"
BASE="http://localhost:${PORT}"
MASTER_KEY="sk-mcp-smoke-master-0123456789"
MAGIC="zephyr-7731"   # opaque word: the model can only produce it by reading the file

command -v npx >/dev/null || { echo "ERROR: npx (node) is required for the MCP servers." >&2; exit 1; }

keyfor() { local v="${!1:-}"; if [ -z "$v" ] && [ -f .env ]; then
    v="$(grep -E "^${1}=" .env | head -1 | sed -E "s/^${1}=//; s/^\"//; s/\"$//" || true)"; fi; printf '%s' "$v"; }
KEYVAR="$(printf '%s' "$PROVIDER" | tr '[:lower:]-' '[:upper:]_')_API_KEY"
PKEY="$(keyfor "$KEYVAR")"
[ -z "$PKEY" ] && { echo "ERROR: need $KEYVAR (in the environment or .env)." >&2; exit 1; }

TMP="$(mktemp -d)"; BIN="$TMP/ferrogw"; GW_PID=""; HTTP_PID=""
cleanup() {
  [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true
  [ -n "$HTTP_PID" ] && kill "$HTTP_PID" 2>/dev/null || true
  # npx spawns node as a child; killing npx does not always reap it, so free the
  # HTTP server's port directly too.
  command -v lsof >/dev/null && { lsof -ti tcp:3001 2>/dev/null | xargs -r kill 2>/dev/null || true; }
  rm -rf "$TMP"
}
trap cleanup EXIT

echo "Building ferrogw…"; go build -o "$BIN" ./cmd/ferrogw
mkdir -p "$TMP/fs"; printf 'the magic word is %s\n' "$MAGIC" > "$TMP/fs/secret.txt"

pass=0; fail=0
check() { if [ "$2" -eq 0 ]; then printf '  \033[32m✅ %s\033[0m\n' "$1"; pass=$((pass+1))
  else printf '  \033[31m❌ %s\033[0m — %s\n' "$1" "$3"; fail=$((fail+1)); fi; }
contains() { case "$1" in *"$2"*) return 0;; *) return 1;; esac; }

wait_up() { local i; for i in $(seq 1 60); do curl -s -o /dev/null -m 2 "$BASE/readyz" && return 0; sleep 0.5; done
  echo "gateway did not come up; log:"; tail -20 "$TMP/gw.log" >&2; exit 1; }
mcp_ready() { curl -s -m 3 "$BASE/readyz" \
  | python3 -c "import sys,json;d=json.load(sys.stdin);print(any(s.get('name')=='$1' and s.get('ready') for s in d.get('mcp_servers',[])))" 2>/dev/null | grep -q True; }
wait_mcp() { local i; for i in $(seq 1 60); do mcp_ready "$1" && return 0; sleep 1; done
  echo "MCP server '$1' never became ready; log:"; tail -30 "$TMP/gw.log" >&2; return 1; }

start_gw() { # <config>
  env MASTER_KEY="$MASTER_KEY" PORT="$PORT" ALLOW_UNAUTHENTICATED_PROXY=true \
      FERRO_MODEL_CATALOG_TIMEOUT=0 \
      CONFIG_STORE_BACKEND=memory API_KEY_STORE_BACKEND=memory \
      "$KEYVAR=$PKEY" GATEWAY_CONFIG="$1" "$BIN" serve >"$TMP/gw.log" 2>&1 &
  GW_PID=$!; wait_up
}
stop_gw() { [ -n "$GW_PID" ] && kill "$GW_PID" 2>/dev/null || true; wait "$GW_PID" 2>/dev/null || true; GW_PID=""; sleep 0.4; }

# ask <prompt> -> sets CODE, CONTENT (the assistant's final text)
ask() {
  local body; body=$(printf '{"model":"%s","messages":[{"role":"user","content":"%s"}],"max_tokens":300}' "$MODEL" "$1")
  local out; out=$(curl -s -m 150 -w $'\n%{http_code}' -H 'Content-Type: application/json' -d "$body" "$BASE/v1/chat/completions")
  CODE="${out##*$'\n'}"
  CONTENT=$(python3 -c 'import sys,json
try: print(json.load(sys.stdin)["choices"][0]["message"]["content"])
except Exception: pass' <<<"${out%$'\n'*}" 2>/dev/null || true)
}

echo; echo "== MCP smoke test =="; echo "provider=$PROVIDER model=$MODEL port=$PORT"; echo

# ── 1. stdio servers: agentic loop (everything + filesystem) ─────────────────────
echo "1. stdio MCP — agentic tool-calling loop"
sed -e "s#MCPFS_DIR#$TMP/fs#" -e "s#virtual_key: openai#virtual_key: ${PROVIDER}#" scripts/config.mcp-test.yaml > "$TMP/p1.yaml"
start_gw "$TMP/p1.yaml"; wait_mcp everything; wait_mcp filesystem

ask "Use a tool to add 17 and 25, then state only the resulting number."
check "everything: model calls the compute tool (answer contains 42)" \
  "$([ "$CODE" = 200 ] && contains "$CONTENT" 42 && echo 0 || echo 1)" "code=$CODE content=$(printf %.120s "$CONTENT")"

ask "Read the file secret.txt and reply with only the exact magic word it contains."
check "filesystem: model reads the file via MCP (answer contains the opaque word)" \
  "$([ "$CODE" = 200 ] && contains "$CONTENT" "$MAGIC" && echo 0 || echo 1)" "code=$CODE content=$(printf %.120s "$CONTENT")"
stop_gw

# ── 2. allowed_tools filter — read tool withheld, word unreachable ───────────────
echo "2. allowed_tools — the read tool is withheld, so the file cannot be read"
cat > "$TMP/p2.yaml" <<YAML
strategy: { mode: single }
targets: [ { virtual_key: ${PROVIDER} } ]
mcp_servers:
  - name: filesystem
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "$TMP/fs"]
    allowed_tools: ["list_directory", "list_allowed_directories"]
    timeout_seconds: 60
YAML
start_gw "$TMP/p2.yaml"; wait_mcp filesystem
ask "Read the file secret.txt and reply with only the exact magic word it contains."
check "allowed_tools withholds read: the opaque word is NOT produced" \
  "$([ "$CODE" = 200 ] && ! contains "$CONTENT" "$MAGIC" && echo 0 || echo 1)" "leaked=$(contains "$CONTENT" "$MAGIC" && echo yes || echo no) content=$(printf %.120s "$CONTENT")"
stop_gw

# ── 3. HTTP (Streamable HTTP) transport ──────────────────────────────────────────
echo "3. HTTP transport — the gateway connects to an MCP server over Streamable HTTP"
# server-everything's streamableHttp reads PORT; pin it to 3001 so it never
# collides with the gateway's own PORT (which we inherit in the environment).
PORT=3001 npx -y @modelcontextprotocol/server-everything streamableHttp >"$TMP/http-mcp.log" 2>&1 &
HTTP_PID=$!
httpok=1
# Probe by opening a TCP connection, not an HTTP request: a POST to /mcp opens an
# SSE stream that would make curl block until timeout.
for i in $(seq 1 80); do
  (exec 3<>/dev/tcp/127.0.0.1/3001) 2>/dev/null && { exec 3>&-; httpok=0; break; }
  sleep 0.5
done
if [ "$httpok" -ne 0 ]; then
  check "HTTP MCP server started on :3001" 1 "server-everything streamableHttp did not come up"
else
  cat > "$TMP/p3.yaml" <<YAML
strategy: { mode: single }
targets: [ { virtual_key: ${PROVIDER} } ]
mcp_servers:
  - name: remote
    url: http://localhost:3001/mcp
    timeout_seconds: 30
YAML
  start_gw "$TMP/p3.yaml"; wait_mcp remote
  ask "Use a tool to add 100 and 23, then state only the resulting number."
  check "HTTP transport: model calls a tool over Streamable HTTP (answer contains 123)" \
    "$([ "$CODE" = 200 ] && contains "$CONTENT" 123 && echo 0 || echo 1)" "code=$CODE content=$(printf %.120s "$CONTENT")"
  stop_gw
fi
kill "$HTTP_PID" 2>/dev/null || true; HTTP_PID=""
command -v lsof >/dev/null && { lsof -ti tcp:3001 2>/dev/null | xargs -r kill 2>/dev/null || true; }
sleep 1

# ── 4. required-server readiness gating ──────────────────────────────────────────
echo "4. required MCP server that cannot start → /readyz is 503"
cat > "$TMP/p4.yaml" <<YAML
strategy: { mode: single }
targets: [ { virtual_key: ${PROVIDER} } ]
mcp_servers:
  - name: broken
    command: /nonexistent-mcp-command-xyz
    required: true
    timeout_seconds: 10
YAML
start_gw "$TMP/p4.yaml"
# The required server's failed handshake is reflected a moment after bind; poll.
rc=000
for i in $(seq 1 30); do
  rc=$(curl -s -o /dev/null -m 3 -w '%{http_code}' "$BASE/readyz")
  [ "$rc" = 503 ] && break
  sleep 1
done
check "required broken server takes the instance out of rotation (/readyz 503)" \
  "$([ "$rc" = 503 ] && echo 0 || echo 1)" "/readyz=$rc"
stop_gw

echo; echo "== Summary: ${pass} passed, ${fail} failed =="
[ "$fail" -eq 0 ]
