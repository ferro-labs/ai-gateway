#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <tag> [output-file]" >&2
  exit 1
fi

tag="$1"
version="${tag#v}"
output_file="${2:-}"
repo_url="https://github.com/ferro-labs/ai-gateway"
changelog_file="CHANGELOG.md"

if [[ ! -f "$changelog_file" ]]; then
  echo "missing $changelog_file" >&2
  exit 1
fi

tmp_file="$(mktemp)"
cleanup() {
  rm -f "$tmp_file"
}
trap cleanup EXIT

if ! awk -v version="$version" '
  $0 ~ "^## \\[" version "\\]" {
    in_section = 1
    found = 1
  }

  in_section {
    if (count > 0 && $0 ~ "^## \\[") {
      exit
    }
    lines[++count] = $0
  }

  END {
    if (!found) {
      exit 2
    }
    while (count > 0 && (lines[count] ~ /^[[:space:]]*$/ || lines[count] ~ /^---[[:space:]]*$/)) {
      count--
    }
    for (i = 1; i <= count; i++) {
      print lines[i]
    }
  }
' "$changelog_file" >"$tmp_file"; then
  echo "failed to extract release notes for $tag from $changelog_file" >&2
  exit 1
fi

{
  cat "$tmp_file"
  printf '\n\n---\n\n**Full changelog:** %s/blob/%s/CHANGELOG.md\n' "$repo_url" "$tag"
} >"${output_file:-/dev/stdout}"
