#!/usr/bin/env bash
#
# Generates notices/THIRD-PARTY-NOTICES.txt: the licence and NOTICE text of every
# Go module compiled into the ferrogw binary. Run by GoReleaser's before hook,
# so the file is produced at release time and bundled into the archives and the
# image's /licenses. It is a build artifact and is not committed.
#
# The output is deliberately not under dist/: GoReleaser's "dist must be empty"
# guard runs after the before hooks, so writing there would abort the release.

set -euo pipefail
cd "$(dirname "$0")/.."

out="notices/THIRD-PARTY-NOTICES.txt"
tmp="notices/.tmp-licenses"

# go-licenses is not a build dependency, so install it on demand. Reference it by
# absolute path rather than trusting GOPATH/bin to be on PATH in the hook's shell.
gl="$(go env GOPATH)/bin/go-licenses"
[ -x "$gl" ] || go install github.com/google/go-licenses/v2@v2.0.1

rm -rf "$tmp"
# GOROOT must match the toolchain go-licenses shells out to, or its analysis
# fails on a version mismatch.
GOROOT="$(go env GOROOT)" "$gl" save ./cmd/ferrogw --save_path="$tmp" --force

mkdir -p notices
{
  echo "Ferro Labs AI Gateway - third-party licences"
  echo
  echo "The ferrogw binary statically links the Go modules below. Each module's"
  echo "licence, and its NOTICE where one is present, is reproduced in full."
  echo
  find "$tmp" -type f | sort | while IFS= read -r f; do
    module="${f#"$tmp"/}"
    module="${module%/*}"
    printf '================================================================\n'
    printf '%s\n' "$module"
    printf '================================================================\n\n'
    cat "$f"
    printf '\n\n'
  done
} >"$out"

rm -rf "$tmp"
echo "wrote $out ($(wc -l <"$out") lines)"
