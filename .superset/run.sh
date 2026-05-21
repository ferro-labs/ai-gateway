#!/usr/bin/env bash
set -euo pipefail

cd "${SUPERSET_WORKSPACE_PATH:-$PWD}"

if [ -f ".superset/runtime/ports.env" ]; then
  set -a
  . ./.superset/runtime/ports.env
  set +a
fi

if [ -f "package.json" ]; then
  if [ -f "pnpm-lock.yaml" ] && command -v pnpm >/dev/null 2>&1; then
    pnpm dev
  elif { [ -f "bun.lockb" ] || [ -f "bun.lock" ]; } && command -v bun >/dev/null 2>&1; then
    bun dev
  elif command -v npm >/dev/null 2>&1; then
    npm run dev
  else
    echo "No supported JS package manager found."
    exit 1
  fi
else
  echo "No default run command configured. Edit .superset/run.sh for this repo."
fi
