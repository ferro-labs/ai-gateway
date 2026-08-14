#!/usr/bin/env bash
# Clean-machine sanity for a built ferro binary. Also the manual release gate.
#
#   ./scripts/smoke.sh [path-to-binary]
#   FERRO_SMOKE_URL=http://localhost:8080 ./scripts/smoke.sh   # adds live checks
set -euo pipefail

bin="${1:-./ferro}"
[ -x "$bin" ] || { echo "FAIL: $bin is not executable (run: make build)" >&2; exit 1; }

"$bin" version
"$bin" --help >/dev/null

# Exit codes are the API: an unknown verb must be a non-zero exit, or every
# `ferro ... || alert` in a pipeline silently passes.
if "$bin" definitely-not-a-verb >/dev/null 2>&1; then
  echo "FAIL: unknown verb must exit non-zero" >&2
  exit 1
fi

# The full-screen console must never appear on a non-TTY stdout. This runs with
# stdout piped, so a bare invocation has to refuse rather than emit escape codes.
command -v timeout >/dev/null || { echo "FAIL: timeout is required for the non-TTY smoke check" >&2; exit 1; }
set +e
bare_output="$(timeout --signal=TERM --kill-after=1s 5s "$bin" </dev/null 2>/dev/null)"
bare_status=$?
set -e
if [ "$bare_status" -eq 124 ] || [ "$bare_status" -eq 137 ]; then
  echo "FAIL: bare ferro blocked on a non-TTY invocation" >&2
  exit 1
fi
if [ "$bare_status" -eq 0 ]; then
  echo "FAIL: bare ferro must refuse a non-TTY invocation" >&2
  exit 1
fi
if [[ "$bare_output" == *$'\e['* ]]; then
  echo "FAIL: bare ferro emitted ANSI on a non-TTY stdout" >&2
  exit 1
fi

if [ -n "${FERRO_SMOKE_URL:-}" ]; then
  FERRO_URL="$FERRO_SMOKE_URL" "$bin" status
  # stdout must be exactly one JSON document — nothing else may leak into it.
  FERRO_URL="$FERRO_SMOKE_URL" "$bin" status --format json | python3 -m json.tool >/dev/null
  echo "[OK] live smoke against $FERRO_SMOKE_URL"
fi

echo "[OK] smoke passed"
