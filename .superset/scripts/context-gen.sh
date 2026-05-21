#!/usr/bin/env bash
set -euo pipefail

mkdir -p .superset/context

{
  echo "# Workspace Context"
  echo
  echo "- Workspace: ${SUPERSET_WORKSPACE_NAME:-unknown}"
  echo "- Path: ${SUPERSET_WORKSPACE_PATH:-$PWD}"
  echo "- Branch: $(git branch --show-current 2>/dev/null || echo unknown)"
  echo "- Commit: $(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  echo
  echo "## Status"
  git status --short 2>/dev/null || true
} > .superset/context/current-status.txt
