#!/bin/sh
#
# One-command installer for the Ferro Labs AI Gateway.
#
#   curl -fsSL https://get.ferrolabs.ai/install.sh | sh
#
# POSIX sh only. Alpine's /bin/sh is busybox ash and Debian's is dash, and both
# have to run this, so: no [[, no arrays, no ${!indirect}, and no
# `set -o pipefail` — that one is not POSIX and would abort the script on the
# very shells this exists to support.
#
# TRUNCATION DEFENCE (load-bearing, do not undo). A `curl | sh` whose connection
# drops mid-transfer hands the shell a *prefix* of this script, so a prefix must
# be inert rather than half-applied. Two things guarantee that:
#
#   1. Every statement lives inside a function, and `main "$@"` is the last
#      statement in the file. Hoisting even one command to the top level makes a
#      half-downloaded installer run half an install.
#   2. The whole file is one `{ … }` group. sh has to parse a compound command
#      to its closing brace before it can run any of it, so a truncated copy is
#      a syntax error at EOF and nothing executes at all — not even the partial
#      word a cut inside a function name would otherwise leave behind for the
#      shell to look up as a command.
#
# This installer writes exactly one file: the binary. It never writes a config,
# never mints a master key, never runs the gateway, and never touches the
# caller's working directory.

{ # opened here, closed on the last line — see TRUNCATION DEFENCE above

set -eu

# ─── Product ─────────────────────────────────────────────────────────────────
# The only product-specific values in the script, alongside print_next_steps().
# A sibling installer for the `ferro` CLI is meant to drop in by editing these
# two places, not by rewriting the script.

repo="ferro-labs/ai-gateway" # GitHub owner/name; also roots the cosign identity
bin="ferrogw"                # binary inside the archive, and the installed name
asset="ferrogw"              # asset prefix: <asset>_<version>_<os>_<arch>.tar.gz
env_prefix="FERROGW"         # env namespace: <env_prefix>_VERSION, _INSTALL_DIR, …

# ─── Helpers ─────────────────────────────────────────────────────────────────

say() { printf '%s\n' "$*"; }
warn() { printf '%s\n' "$*" >&2; }
die() {
  printf 'install.sh: error: %s\n' "$*" >&2
  exit 1
}
has_cmd() { command -v "$1" >/dev/null 2>&1; }

# Read <env_prefix>_<NAME>, empty when unset. Indirect expansion (${!name}) is a
# bashism, so eval is the portable spelling; the name is a literal from the
# product block above and never comes from the caller.
env_get() { eval "printf '%s' \"\${${env_prefix}_$1-}\""; }

usage() {
  cat <<EOF
Install $bin, the Ferro Labs AI Gateway.

Usage:
  curl -fsSL https://get.ferrolabs.ai/install.sh | sh
  curl -fsSL https://get.ferrolabs.ai/install.sh | sh -s -- --version v1.4.2

Options:
  --version <tag>       Release to install (default: the latest release).
  --install-dir <dir>   Where to put the binary. Default: /usr/local/bin when it
                        is writable without sudo, otherwise \$HOME/.local/bin.
  --verify-signature    Require the cosign signature over checksums.txt to be
                        checked, and fail when cosign is not installed. The
                        SHA-256 checksum is verified either way — there is no
                        flag to skip it.
  --no-modify-path      Suppress the PATH advice. This installer never edits a
                        shell profile in any case; it only prints the line to add.
  --help                Show this message.

Environment equivalents, for the piped form where flags are awkward:
  ${env_prefix}_VERSION, ${env_prefix}_INSTALL_DIR,
  ${env_prefix}_VERIFY_SIGNATURE=1, ${env_prefix}_NO_MODIFY_PATH=1
EOF
}

# ─── Steps ───────────────────────────────────────────────────────────────────

# Sets os/arch, or exits naming the supported set. It never falls through to a
# guessed value: a URL built from an unrecognised uname is a 404 several steps
# later, reported as a download failure, which sends the reader after the
# release instead of after their platform.
detect_platform() {
  os=$(uname -s)
  arch=$(uname -m)

  case "$os" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) die "unsupported operating system '$os' — this installer supports Linux and macOS (Darwin); on Windows use install.ps1" ;;
  esac

  case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *) die "unsupported architecture '$arch' — supported: x86_64/amd64, aarch64/arm64" ;;
  esac
}

# Sets version to a tag like v1.4.2.
#
# Deliberately not api.github.com/repos/.../releases/latest: that endpoint is
# rate-limited to 60 requests/hour per IP unauthenticated, so it fails outright
# on shared CI egress, and parsing tag_name out of its JSON with grep breaks on
# any reflow. The web /releases/latest URL is a 302 to /releases/tag/<tag>;
# following it costs one HEAD, needs no token, and has no rate limit.
resolve_version() {
  if [ -n "$version" ]; then
    # Accept 1.4.2 as well as v1.4.2 — the tag carries the v, the asset does not,
    # and users copy from whichever of the two they happened to be looking at.
    case "$version" in
    v*) ;;
    *) version="v$version" ;;
    esac
    return
  fi

  effective_url=$(curl -sSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest") ||
    die "could not reach github.com to resolve the latest release; pass --version to skip the lookup"
  version=${effective_url##*/}

  # A proxy or an error page can return a 200 that is not a tag page, and an
  # unchecked answer here becomes a nonsense download URL.
  case "$version" in
  v[0-9]*) ;;
  *) die "could not resolve the latest release tag (got '$version' from '$effective_url'); pass --version" ;;
  esac
}

download() { # <url> <dest>
  curl -fsSL -o "$2" "$1" || die "download failed: $1"
}

# The published checksums.txt is already in sha256sum's own format (two spaces,
# no '*' binary marker), so it can be fed to -c directly. What it cannot be fed
# is the *whole* file: it lists every release asset including the .sbom.json
# files this script never downloads, and every missing entry is a failure.
# --ignore-missing would solve that on GNU coreutils but busybox sha256sum has
# no such flag, so instead the single line naming our archive is split out and
# checked on its own — which works on every implementation.
verify_checksum() { # <filename>
  if has_cmd sha256sum; then
    sum_cmd="sha256sum"
  elif has_cmd shasum; then
    sum_cmd="shasum -a 256"
  else
    die "neither sha256sum nor shasum is available, so the download cannot be verified"
  fi

  grep "  $1\$" checksums.txt >expected.txt ||
    die "checksums.txt from $version has no entry for $1"

  # Unquoted on purpose: shasum's "-a 256" has to split into two arguments.
  # shellcheck disable=SC2086
  $sum_cmd -c expected.txt >/dev/null ||
    die "SHA-256 verification FAILED for $1 — do not use this download"
}

# The release signs checksums.txt with keyless cosign, which in turn covers
# every archive, so verifying the one file covers the lot. cosign is not a
# reasonable prerequisite for a one-liner install, so its absence is a printed
# note rather than a failure — unless the caller asked for the guarantee.
verify_signature() { # <base_url>
  if ! has_cmd cosign; then
    if [ "$require_signature" = "1" ]; then
      die "--verify-signature was requested but cosign is not on PATH (see https://docs.sigstore.dev/cosign/system_config/installation/)"
    fi
    say "note: this release's checksums.txt is cosign-signed. Install cosign and re-run with --verify-signature to check it."
    return 0
  fi

  download "$1/checksums.txt.sig" "checksums.txt.sig"
  download "$1/checksums.txt.pem" "checksums.txt.pem"

  # Identity is pinned to a release-tag run of a workflow in this repository, not
  # to one workflow filename: renaming release.yml must not break every installed
  # copy of this script, while a signature from any other repo still fails.
  # stdout is dropped; cosign's own diagnosis on stderr is kept, because a
  # signature failure is the one outcome worth reading in full.
  cosign verify-blob \
    --certificate "checksums.txt.pem" \
    --signature "checksums.txt.sig" \
    --certificate-identity-regexp "^https://github\.com/$repo/\.github/workflows/.+@refs/tags/" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    "checksums.txt" >/dev/null ||
    die "cosign signature verification FAILED for checksums.txt — do not use this download"

  say "cosign signature verified (keyless, $repo release workflow)"
}

# Sets install_dir. Runs after extraction so the sudo hint below can name a file
# that actually exists.
#
# sudo is never invoked. A pipe-to-shell that escalates on the reader's behalf is
# asking for trust it has no way to earn, and the failure mode — a password
# prompt from a script whose contents nobody read — is exactly the one that
# trains people to type their password into anything.
pick_install_dir() { # <extracted_binary_path>
  if [ -n "$install_dir" ]; then
    mkdir -p "$install_dir" 2>/dev/null || die "cannot create install directory '$install_dir'"
    if [ ! -w "$install_dir" ]; then
      # Keep the scratch directory so the command below is one the reader can
      # paste and have work. This is the only path that leaves it behind, and
      # it is the OS scratch space, not the caller's directory.
      trap - EXIT
      warn "install.sh: error: '$install_dir' is not writable by this user."
      warn ""
      warn "Install it yourself with:"
      warn ""
      warn "    sudo install -m 0755 '$1' '$install_dir/$bin'"
      warn ""
      warn "Or re-run without --install-dir to install into \$HOME/.local/bin."
      exit 1
    fi
    return
  fi

  # Preferred when it is already writable as this user (Homebrew-managed macOS,
  # most single-user Linux boxes); otherwise the user-scope directory, which
  # needs no privileges and is on PATH by default on most modern distributions.
  if [ -d "/usr/local/bin" ] && [ -w "/usr/local/bin" ]; then
    install_dir="/usr/local/bin"
    return
  fi

  # Under `set -u` an unset HOME would abort with the shell's own message, which
  # names no remedy. Service managers and minimal containers really do run
  # without it.
  [ -n "${HOME-}" ] || die "neither /usr/local/bin nor \$HOME is usable (HOME is not set) — pass --install-dir"

  install_dir="$HOME/.local/bin"
  mkdir -p "$install_dir" || die "cannot create install directory '$install_dir'"
}

# Staged through a dotfile in the target directory and renamed into place, rather
# than copied over the target: rename(2) is atomic and, unlike truncating an open
# file, does not fail with ETXTBSY when the gateway being upgraded is running.
install_binary() { # <extracted_binary_path>
  staged="$install_dir/.$bin.install.$$"
  cp "$1" "$staged" || die "could not write to '$install_dir'"
  chmod 0755 "$staged"
  mv -f "$staged" "$install_dir/$bin" || die "could not install into '$install_dir'"
}

# The three commands that actually work, in the order they have to be run. The
# middle one is here because it is the step people lose an hour to: the server
# reads GATEWAY_CONFIG, so without it `serve` ignores the file `init` just wrote
# and starts with no providers. The installer is the one output guaranteed to be
# read, so it is the right place to say so.
print_next_steps() {
  say ""
  say "Next steps:"
  say ""
  printf '    %s init                        # writes config.yaml, prints your master key\n' "$bin"
  printf '    export GATEWAY_CONFIG=./config.yaml # the server ignores the file without this\n'
  printf '    %s serve\n' "$bin"
  say ""
  say "Docs: https://docs.ferrolabs.ai"
}

# ─── Main ────────────────────────────────────────────────────────────────────

main() {
  version=$(env_get VERSION)
  install_dir=$(env_get INSTALL_DIR)
  require_signature=$(env_get VERIFY_SIGNATURE)
  no_modify_path=$(env_get NO_MODIFY_PATH)
  [ -n "$require_signature" ] || require_signature="0"
  [ -n "$no_modify_path" ] || no_modify_path="0"

  while [ $# -gt 0 ]; do
    case "$1" in
    --version)
      [ $# -ge 2 ] || die "--version needs a value, e.g. --version v1.4.2"
      version="$2"
      shift 2
      ;;
    --version=*)
      version="${1#*=}"
      shift
      ;;
    --install-dir)
      [ $# -ge 2 ] || die "--install-dir needs a value"
      install_dir="$2"
      shift 2
      ;;
    --install-dir=*)
      install_dir="${1#*=}"
      shift
      ;;
    --verify-signature)
      require_signature="1"
      shift
      ;;
    --no-modify-path)
      no_modify_path="1"
      shift
      ;;
    --help | -h)
      usage
      return 0
      ;;
    *) die "unknown option '$1' (try --help)" ;;
    esac
  done

  has_cmd curl || die "curl is required but was not found on PATH"
  has_cmd tar || die "tar is required but was not found on PATH"

  # Made absolute here, before the cd into the scratch directory below.
  # Otherwise a relative --install-dir would resolve against the scratch
  # directory and the install would be deleted by its own cleanup trap.
  case "$install_dir" in
  "" | /*) ;;
  *) install_dir="$(pwd)/$install_dir" ;;
  esac

  detect_platform
  resolve_version

  # The asset name carries the bare version; the git tag carries the v.
  archive="${asset}_${version#v}_${os}_${arch}.tar.gz"
  base_url="https://github.com/$repo/releases/download/$version"

  # Not named TMPDIR: that variable is mktemp's own input, and clobbering it
  # would relocate every later mktemp in the process.
  work_dir=$(mktemp -d 2>/dev/null) || die "could not create a temporary directory"
  trap 'rm -rf "$work_dir"' EXIT

  # Process-local, so the caller's working directory is untouched; it exists so
  # the relative filenames inside checksums.txt resolve for `sha256sum -c`.
  cd "$work_dir" || die "could not enter '$work_dir'"

  say "Installing $bin $version ($os/$arch)…"
  download "$base_url/$archive" "$archive"
  download "$base_url/checksums.txt" "checksums.txt"

  verify_checksum "$archive"
  verify_signature "$base_url"

  # The archive is flat, so the binary is extracted by name rather than by path.
  tar -xzf "$archive" "$bin" || die "could not extract '$bin' from $archive"

  pick_install_dir "$work_dir/$bin"
  install_binary "$work_dir/$bin"

  say "Installed $bin $version to $install_dir/$bin"

  # Never edits a shell profile — see --no-modify-path in the usage text. Whose
  # profile it would be is unknowable from inside a pipe (the parent is sh, not
  # the user's login shell), and silently rewriting a dotfile is a bigger
  # surprise than one line of copy-paste.
  case ":$PATH:" in
  *":$install_dir:"*) ;;
  *)
    if [ "$no_modify_path" != "1" ]; then
      say ""
      say "$install_dir is not on your PATH. Add it with:"
      say ""
      say "    export PATH=\"$install_dir:\$PATH\""
    fi
    ;;
  esac

  print_next_steps
}

main "$@"

} # end of the truncation-defence group — keep this the last line of the file
