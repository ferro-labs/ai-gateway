#!/usr/bin/env bash
set -euo pipefail

cd "${SUPERSET_WORKSPACE_PATH:-$PWD}" 2>/dev/null || exit 0

if [ -f ".superset/runtime/ports.env" ]; then
  set -a
  . ./.superset/runtime/ports.env
  set +a
  for port in "${WEB_PORT:-}" "${API_PORT:-}" "${DOCS_PORT:-}" "${STORYBOOK_PORT:-}"; do
    if [ -n "$port" ]; then
      pids="$(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null || true)"
      if [ -n "$pids" ]; then
        kill -TERM $pids 2>/dev/null || true
      fi
    fi
  done
fi
