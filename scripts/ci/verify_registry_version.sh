#!/usr/bin/env bash
# Runs a just-published `ferrogw` through a package runner (npx, uvx) and
# asserts the version it reports. Shared by the packages-npm and packages-pypi
# jobs in .github/workflows/install-matrix.yml so the retry semantics cannot
# drift between them.
#
#   verify_registry_version.sh <label> -- <command...>
#
# Environment:
#   EXPECTED_VERSION     the version the binary must report (required)
#   MAX_ATTEMPTS         attempts before failing        (default 10)
#   RETRY_DELAY          seconds between attempts        (default 30)
#   PER_ATTEMPT_TIMEOUT  seconds one attempt may run     (default 120)
#   RUNNER_OS            named in the error annotations  (default: uname)
#
# A fresh publish takes a few minutes to reach every registry edge — every
# release since v1.5.2 failed on attempt 1 and passed on rerun — so a failing
# command is retried, bounded, and its output is printed when the budget runs
# out. A version mismatch is not retried: a wrong version is a bug, not lag.
#
# Each attempt is bounded too: a stalled fetch would otherwise turn "10 x 30 s"
# into an unbounded wait. GNU coreutils `timeout` is on the Linux and Windows
# (Git Bash) runners; the macOS image ships it as `gtimeout` via Homebrew.
# Without either, attempts run unbounded and say so.
#
# Deliberately not `set -e`: GitHub's `shell: bash` wrapper already runs this
# under `bash -e`, which is what used to abort the step before its own
# diagnostic printed. Errors are handled explicitly.
set -uo pipefail
set +e

label="${1:?usage: verify_registry_version.sh <label> -- <command...>}"
shift
[ "${1:-}" = "--" ] && shift
[ "$#" -gt 0 ] || { echo "verify_registry_version.sh: no command given" >&2; exit 2; }
: "${EXPECTED_VERSION:?EXPECTED_VERSION is required}"

max_attempts="${MAX_ATTEMPTS:-10}"
retry_delay="${RETRY_DELAY:-30}"
per_attempt="${PER_ATTEMPT_TIMEOUT:-120}"
os="${RUNNER_OS:-$(uname -s)}"

if command -v timeout >/dev/null 2>&1; then
  bound=(timeout "$per_attempt")
elif command -v gtimeout >/dev/null 2>&1; then
  bound=(gtimeout "$per_attempt")
else
  echo "::notice title=no timeout binary on ${os}::each ${label} attempt runs unbounded"
  bound=()
fi

attempt=1
while :; do
  raw="$("${bound[@]}" "$@" 2>&1)"
  rc=$?
  [ "$rc" -eq 0 ] && break
  if [ "$rc" -eq 124 ]; then
    reason="timed out after ${per_attempt}s"
  else
    reason="exited ${rc}"
  fi
  if [ "$attempt" -ge "$max_attempts" ]; then
    echo "::error title=${label} ferrogw failed on ${os}::${label} ${reason} for ferrogw ${EXPECTED_VERSION} after ${attempt} attempts"
    printf '%s\n' "$raw"
    exit 1
  fi
  echo "attempt ${attempt}/${max_attempts}: ${label} ${reason}; retrying in ${retry_delay}s (registry propagation)"
  attempt=$((attempt + 1))
  sleep "$retry_delay"
done

got="$(printf '%s\n' "$raw" | awk '$1 == "Version" { print $2; exit }')"
if [ "$got" != "$EXPECTED_VERSION" ]; then
  echo "::error title=${label} version mismatch on ${os}::expected ferrogw version '${EXPECTED_VERSION}', got '${got:-<no Version line in output>}'"
  printf 'full output:\n%s\n' "$raw"
  exit 1
fi
echo "OK ${label} on ${os}: ferrogw ${got}"
