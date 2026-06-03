#!/usr/bin/env bash
set -euo pipefail

catalog_url="${FERRO_MODEL_CATALOG_URL:-https://github.com/ferro-labs/model-catalog/releases/latest/download/catalog.json}"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="${repo_root}/models/catalog_backup.json"
tmp="$(mktemp)"
trap 'rm -f "${tmp}"' EXIT

curl -fsSL "${catalog_url}" -o "${tmp}"
python3 -m json.tool "${tmp}" >/dev/null
mv "${tmp}" "${target}"

go test ./models -run TestCatalogBackupParseable -count=1

echo "refreshed ${target} from ${catalog_url}"
