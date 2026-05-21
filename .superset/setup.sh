#!/usr/bin/env bash
set -euo pipefail

cd "${SUPERSET_WORKSPACE_PATH:-$PWD}"

mkdir -p .superset/runtime .superset/context .cache

copy_if_present() {
  local src="$1"
  local dst="$2"
  if [ -f "$src" ]; then
    cp -c "$src" "$dst" 2>/dev/null || cp "$src" "$dst"
  fi
}

if [ -n "${SUPERSET_ROOT_PATH:-}" ]; then
  copy_if_present "${SUPERSET_ROOT_PATH}/.env" ".env"
  copy_if_present "${SUPERSET_ROOT_PATH}/.env.local" ".env.local"
  copy_if_present "${SUPERSET_ROOT_PATH}/.env.development" ".env.development"
fi

PORT_HASH="$(printf "%s" "${SUPERSET_WORKSPACE_NAME:-workspace}" | cksum | awk '{print $1}')"
PORT_BASE="$((3000 + (PORT_HASH % 100) * 20))"

cat > .superset/runtime/ports.env <<PORTS
PORT_BASE=${PORT_BASE}
WEB_PORT=$((PORT_BASE + 0))
API_PORT=$((PORT_BASE + 1))
DOCS_PORT=$((PORT_BASE + 2))
STORYBOOK_PORT=$((PORT_BASE + 3))
PORTS

if [ "${SUPERSET_INSTALL_DEPS:-0}" = "1" ]; then
  if [ -f "pnpm-lock.yaml" ] && command -v pnpm >/dev/null 2>&1; then
    pnpm install --frozen-lockfile
  elif { [ -f "bun.lockb" ] || [ -f "bun.lock" ]; } && command -v bun >/dev/null 2>&1; then
    export BUN_INSTALL_CACHE_DIR="${SUPERSET_ROOT_PATH:-$PWD}/.cache/bun"
    mkdir -p "$BUN_INSTALL_CACHE_DIR"
    bun install --frozen-lockfile
  elif [ -f "package-lock.json" ] && command -v npm >/dev/null 2>&1; then
    npm ci
  elif [ -f "pyproject.toml" ] && command -v uv >/dev/null 2>&1; then
    uv sync
  fi
else
  echo "Skipping dependency install. Set SUPERSET_INSTALL_DEPS=1 to install during setup."
fi

if [ -x ".superset/scripts/context-gen.sh" ]; then
  ./.superset/scripts/context-gen.sh || true
fi

date -u +"%Y-%m-%dT%H:%M:%SZ" > .superset/runtime/setup-ready.txt
