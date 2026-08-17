# npm distribution

`npx ferrogw` runs the gateway with nothing pre-installed. This directory holds
the two committed pieces that make that true; everything else is generated from
a release.

```
packaging/npm/
├── bin/ferrogw.js   the shim that becomes ferrogw's `bin`
├── build.mjs        release archives -> the packages that get published
└── README.md        this file
```

Built and published by
[`.github/workflows/publish-npm.yml`](../../.github/workflows/publish-npm.yml)
after the release assets exist. The release workflow must dispatch it
explicitly; see [Publishing](#publishing).

## Layout

One tag produces up to seven packages:

| Package | Contents |
|---|---|
| `ferrogw` | `bin/ferrogw.js` and nothing else. Names the six below as `optionalDependencies`, pinned to the exact version. |
| `@ferro-labs-ai/gateway-linux-x64` | the `ferrogw` binary, plus `os: ["linux"]` / `cpu: ["x64"]` |
| `@ferro-labs-ai/gateway-linux-arm64` | " |
| `@ferro-labs-ai/gateway-darwin-x64` | " |
| `@ferro-labs-ai/gateway-darwin-arm64` | " |
| `@ferro-labs-ai/gateway-win32-x64` | `ferrogw.exe` |
| `@ferro-labs-ai/gateway-win32-arm64` | `ferrogw.exe` — first produced by the release that drops the windows/arm64 `ignore:`, see [Platform set](#platform-set) |

npm reads `os`/`cpu` and installs exactly the one package that matches the host,
skipping the other five without downloading them. The shim then
`require.resolve`s whichever one landed and execs the binary inside it.

**The platform packages deliberately declare no `bin`.** Six packages each
claiming the `ferrogw` command would collide, and only the shim knows which one
this host resolved.

### Why not a postinstall downloader

The obvious alternative — one package that fetches the right binary in a
`postinstall` — fails in three ways this layout does not:

- it does nothing under `--ignore-scripts`, which is increasingly the corporate
  default and which CI images set as a matter of policy;
- it needs the network at install time, so it cannot install offline or from a
  private mirror;
- it records nothing in the lockfile, so the bytes it fetched are outside the
  integrity model entirely.

Optional dependencies are ordinary dependencies: integrity-checked in the
lockfile, resolvable from a mirror, and installed with **zero** script
execution. This is the esbuild / biome / swc pattern.

### Why `spawn` and not `spawnSync`

`ferrogw serve` is a long-running server, so the shim is a parent process for
the life of that server and has to behave like one.

On POSIX the right answer would be to replace the shim process with the binary
outright, but Node exposes no `execve`. `spawnSync` blocks the event loop, so no
handler can run: `Ctrl-C` would still reach the child, because a tty signals the
whole foreground process group — but `kill`, `docker stop` and a systemd unit
stop all target the parent alone, and that `SIGTERM` has to be relayed or the
gateway never gets its graceful shutdown.

So the shim uses async `spawn`, forwards `SIGINT`/`SIGTERM`/`SIGHUP`/`SIGBREAK`,
and re-raises on itself when the child dies from a signal, so a supervisor reads
"killed by SIGTERM" rather than "exited 143" — different facts, only one of them
true.

## Building

```sh
gh release download v1.4.2 --dir /tmp/artifacts \
  --pattern 'ferrogw_*.tar.gz' --pattern 'ferrogw_*.zip' --pattern 'checksums.txt'
( cd /tmp/artifacts && sha256sum --ignore-missing -c checksums.txt )

node packaging/npm/build.mjs --artifacts /tmp/artifacts --out /tmp/npm --tag v1.4.2
```

**The input is the published release assets, not `dist/`.** By the time this
workflow runs, the GoReleaser job's working directory is gone — and the release
is the better anchor anyway because it packages the exact bytes users download.

**Nothing here carries a version.** `build.mjs` reads both the version and the
platform set off the archive filenames, and `--tag` is only a cross-check that
these assets are the ones that tag produced. There is no manifest to bump and no
platform table to forget, which are the two ways this project has previously
shipped a stale surface.

Two naming details the script exists to absorb:

- release assets carry **no `v` prefix** — tag `v1.4.2` produces
  `ferrogw_1.4.2_linux_amd64.tar.gz`;
- **npm's platform vocabulary is not Go's** — npm says `x64`/`win32` where Go
  says `amd64`/`windows`, and `os`/`cpu` are matched against `process.platform`
  and `process.arch`, so they must read npm's spelling.

Windows binaries come out of the `.zip`, everything else out of the `.tar.gz`.
`tar` and `unzip` must be on `PATH`; Node ships no archive reader.

The script re-reads every manifest it wrote and checks the name, version,
`os`/`cpu`, the binary's presence and its executable bit, and that
`ferrogw`'s `optionalDependencies` match the packages actually built — because
its failure mode is quiet. A wrong `os` value does not fail a publish; it fails
every install on that platform, at that version, forever.

## Publishing

The workflow publishes over **npm Trusted Publishing (OIDC)** with
`npm publish --provenance`. There is no npm token in secrets — this is the same
keyless posture as the cosign signatures on the release itself.

Requirements, all encoded in the workflow:

- `permissions: id-token: write`;
- npm CLI ≥ 11.5.1 and Node ≥ 22.14 (Node 22 still bundles npm 10, so the
  workflow upgrades npm explicitly);
- `--access public` — scoped packages default to restricted, and provenance
  requires a public package;
- a `repository` field matching where the publish runs from.

The trusted-publisher configuration on npm must name repository
`ferro-labs/ai-gateway`, workflow `publish-npm.yml`, and allowed action
`npm publish`.

The release workflow dispatches this workflow after GoReleaser succeeds and
waits for it to finish. GitHub suppresses most follow-on events created with the
repository `GITHUB_TOKEN`, but explicitly allows `workflow_dispatch`. Both the
automatic and manual paths check out the requested tag before running versioned
build code.

**Order is load-bearing: platform packages first, `ferrogw` last.** The main
package names each platform package at an exact version, so publishing it first
opens a window where an install resolves the shim and no binary at all — and
optional dependencies fail *quietly*, so those users get a broken `npx ferrogw`
rather than a failed install.

Re-running skips versions already on the registry, so a publish that dies
halfway can be resumed without hitting a conflict.

Stable versions publish under npm's `latest` dist-tag. Versions containing a
prerelease component publish under `next`, so an `-rc` release cannot replace
the default installed by `npm install ferrogw`.

### One-time bootstrap (required before the first release)

npm cannot configure a trusted publisher for a package that does not exist yet —
unlike PyPI, there is no pre-registration, and `npm trust` has the same
restriction. **Each of the seven packages therefore needs one manual,
token-authenticated publish before OIDC works**, after which the token should be
revoked:

1. build the packages locally from the first release that contains all six
   platform archives;
2. `npm publish --access public` each one with a granular token;
3. on npmjs.com, add a trusted publisher for each package — repository
   `ferro-labs/ai-gateway`, workflow file `publish-npm.yml`, allowed action
   `npm publish`;
4. revoke the token.

Because npm pins trust to the **workflow filename**, renaming
`publish-npm.yml` stops publishing until all seven trusted publishers are
updated.

## Platform set

**The release decides which packages exist, not this directory.** A platform
whose archive is absent is warned about and skipped, and is left out of
`ferrogw`'s `optionalDependencies` — naming a version that was never published
would be worse than not naming it at all. A platform that appears needs no edit
here.

That is not hypothetical. `.goreleaser.yaml` carried
`ignore: {goos: windows, goarch: arm64}` until recently, so **v1.4.3 and earlier
ship no `ferrogw_<version>_windows_arm64.zip`** and building against one of
those tags produces five platform packages with a warning. The first release cut
after that `ignore:` was dropped produces all six, automatically.

## Scope

These packages use `@ferro-labs-ai`, matching the org's existing
[`@ferro-labs-ai/sdk`](https://www.npmjs.com/package/@ferro-labs-ai/sdk). The
design spec wrote `@ferro-labs`; that scope is unclaimed, and adopting it would
mean a second npm org to own, secure and explain, splitting the org's identity
across two scopes for no gain. Claim `@ferro-labs` defensively if you like, but
publish under the one users already have installed.

## Verifying a change by hand

```sh
node --check packaging/npm/bin/ferrogw.js
node packaging/npm/build.mjs --artifacts /tmp/artifacts --out /tmp/npm --tag v1.4.2

# pack and install exactly what would ship
mkdir -p /tmp/tgz /tmp/smoke
for d in /tmp/npm/ferrogw /tmp/npm/gateway-linux-x64; do ( cd "$d" && npm pack --pack-destination /tmp/tgz ); done
cd /tmp/smoke && npm init -y >/dev/null && npm install --ignore-scripts /tmp/tgz/*.tgz
npx ferrogw version
```
