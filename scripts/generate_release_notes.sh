#!/usr/bin/env bash

set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
  echo "usage: $0 <tag> [output-file]" >&2
  exit 1
fi

tag="$1"
# Tag-push events restrict who can push, but tag names are still
# user-controlled — reject anything outside a semver-shaped allowlist
# before interpolating into curl URLs, git arguments, or printf strings.
if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.+-]+)?$ ]]; then
  echo "refusing unexpected tag format: $tag" >&2
  exit 1
fi
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

# Append a Contributors section sourced from the GitHub compare API.
# Skipped silently on any failure (no prev tag, missing curl/jq, API error,
# rate limit) so the release always falls back to the CHANGELOG-only body.
# Filters out chore:/docs:/ci: commits to keep the focus on feature/fix work.
prev_tag=$(git tag --list 'v[0-9]*' --sort=-v:refname 2>/dev/null | grep -vFx "$tag" | head -n1 || true)

if [[ -n "$prev_tag" ]] && command -v curl >/dev/null 2>&1 && command -v jq >/dev/null 2>&1; then
  api_url="https://api.github.com/repos/ferro-labs/ai-gateway/compare/${prev_tag}...${tag}"
  auth_header=()
  if [[ -n "${GITHUB_TOKEN:-}" ]]; then
    auth_header=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
  fi

  contrib_file="$(mktemp)"
  if curl -fsS "${auth_header[@]}" \
       -H "Accept: application/vnd.github+json" \
       "$api_url" 2>/dev/null \
     | jq -r '
         [
           .commits[]
           | select(.commit.message | test("^(chore|docs|ci)\\("; "i") | not)
           | {
               subject: (.commit.message | split("\n")[0]),
               login:   (.author.login // .commit.author.name),
             }
         ]
         | unique_by(.subject)
         | .[]
         | "* \(.subject) — @\(.login)"
       ' 2>/dev/null > "$contrib_file"
  then
    if [[ -s "$contrib_file" ]]; then
      {
        printf '\n\n## Contributors\n\nThanks to everyone who shipped this release:\n\n'
        cat "$contrib_file"
      } >> "$tmp_file"
    fi
  fi
  rm -f "$contrib_file"
fi

{
  cat "$tmp_file"
  printf '\n\n---\n\n**Full changelog:** %s/blob/%s/CHANGELOG.md\n' "$repo_url" "$tag"
} >"${output_file:-/dev/stdout}"
