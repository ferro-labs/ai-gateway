#!/usr/bin/env node
// Turns a directory of GoReleaser release archives into the npm packages that
// `npx ferrogw` resolves: one platform package per archive, plus the `ferrogw`
// package whose optionalDependencies name them.
//
//   node packaging/npm/build.mjs --artifacts <dir> --out <dir> [--tag vX.Y.Z]
//
// The version and the platform set are READ OFF THE ARCHIVES. There is no
// version constant here and no platform list that a release could disagree
// with, because the two things that have gone wrong for this project before are
// a hand-maintained table nobody updated and a surface nobody published. A tag
// that starts shipping windows/arm64 gains its package with no edit to this
// file; a tag that stops shipping a platform drops it from
// optionalDependencies rather than pointing them at a version that was never
// published.
//
// Input is the *published release assets*, not `dist/`. This runs on release
// publication, long after the GoReleaser job's working directory is gone, and
// that is the better anchor anyway: it packages the exact bytes users download
// and verify, and the workflow_dispatch recovery path takes the identical code
// path as the automatic one.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";

const SCOPE = "@ferro-labs-ai";
const REPO_URL = "https://github.com/ferro-labs/ai-gateway";

// The one mapping that genuinely has to be written down: npm's platform
// vocabulary is not Go's. `os`/`cpu` are matched against process.platform and
// process.arch, so they must read win32/x64 even though the archive that fed
// them reads windows/amd64.
const TARGETS = [
  { npm: "linux-x64", goos: "linux", goarch: "amd64", os: "linux", cpu: "x64", ext: "tar.gz", exe: "ferrogw" },
  { npm: "linux-arm64", goos: "linux", goarch: "arm64", os: "linux", cpu: "arm64", ext: "tar.gz", exe: "ferrogw" },
  { npm: "darwin-x64", goos: "darwin", goarch: "amd64", os: "darwin", cpu: "x64", ext: "tar.gz", exe: "ferrogw" },
  { npm: "darwin-arm64", goos: "darwin", goarch: "arm64", os: "darwin", cpu: "arm64", ext: "tar.gz", exe: "ferrogw" },
  { npm: "win32-x64", goos: "windows", goarch: "amd64", os: "win32", cpu: "x64", ext: "zip", exe: "ferrogw.exe" },
  { npm: "win32-arm64", goos: "windows", goarch: "arm64", os: "win32", cpu: "arm64", ext: "zip", exe: "ferrogw.exe" },
];

// ferrogw_1.4.2_linux_amd64.tar.gz — note the version carries no `v` prefix,
// while the tag it came from does.
const ARCHIVE = /^ferrogw_([^_]+)_([a-z0-9]+)_([a-z0-9]+)\.(tar\.gz|zip)$/;

// fileURLToPath, not new URL(...).pathname: the latter yields "/C:/..." on
// Windows, and this script is also run by hand off a release.
const here = path.dirname(fileURLToPath(import.meta.url));

function fail(message) {
  console.error(`build.mjs: ${message}`);
  process.exit(1);
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value, null, 2)}\n`);
}

// Extraction shells out because Node ships no archive reader. Both tools are on
// every GitHub-hosted runner; a missing one is an environment error worth
// naming rather than a stack trace.
function extract(archive, ext, members, dest) {
  const [tool, args] =
    ext === "zip"
      ? ["unzip", ["-o", "-j", archive, ...members, "-d", dest]]
      : ["tar", ["-xzf", archive, "-C", dest, ...members]];
  try {
    execFileSync(tool, args, { stdio: "pipe" });
  } catch (err) {
    fail(`could not extract ${members.join(", ")} from ${path.basename(archive)} with ${tool}: ${err.message}`);
  }
}

const { values: opts } = parseArgs({
  options: {
    artifacts: { type: "string" },
    out: { type: "string" },
    tag: { type: "string" },
  },
});

if (!opts.artifacts || !opts.out) {
  fail("usage: build.mjs --artifacts <release-assets-dir> --out <dir> [--tag vX.Y.Z]");
}

// ─── Read the release ────────────────────────────────────────────────────────

const found = new Map(); // "<goos>_<goarch>" -> absolute archive path
const versions = new Set();

for (const name of fs.readdirSync(opts.artifacts)) {
  const m = ARCHIVE.exec(name);
  if (!m) continue;
  const [, version, goos, goarch] = m;
  versions.add(version);
  found.set(`${goos}_${goarch}`, path.join(opts.artifacts, name));
}

if (found.size === 0) fail(`no ferrogw_*.tar.gz / .zip archives in ${opts.artifacts}`);
if (versions.size > 1) fail(`archives disagree about the version: ${[...versions].sort().join(", ")}`);

// The loop below walks TARGETS and tolerates a missing archive, which covers
// "the release did not build this platform". It cannot see the other
// direction: an archive the release DID build that TARGETS has no entry for is
// simply never read, so a newly added goos/goarch would go missing from npm
// with a green publish and no warning. Fail on it instead — the same rule
// build_wheels.py applies on the PyPI side, so the two package surfaces cannot
// disagree about which platforms a release covers.
const mapped = new Set(TARGETS.map((t) => `${t.goos}_${t.goarch}`));
const unmapped = [...found.keys()].filter((k) => !mapped.has(k)).sort();
if (unmapped.length > 0) {
  fail(
    `release archives for ${unmapped.join(", ")} have no TARGETS entry — ` +
      `add one (npm/os/cpu/ext/exe) so the platform is published, or the release ` +
      `is shipping a binary npm users cannot install`,
  );
}

const version = [...versions][0];

// The tag is a cross-check, never the source. If the two disagree the release
// assets are not the ones this tag produced, and publishing either number would
// be wrong.
if (opts.tag && opts.tag !== `v${version}`) {
  fail(`tag ${opts.tag} does not match the archives' version ${version} (expected v${version})`);
}

// ─── Platform packages ───────────────────────────────────────────────────────

fs.rmSync(opts.out, { recursive: true, force: true });
fs.mkdirSync(opts.out, { recursive: true });

const built = [];

for (const t of TARGETS) {
  const archive = found.get(`${t.goos}_${t.goarch}`);
  if (!archive) {
    // Not fatal. The release is the authority on what exists, and a platform it
    // did not build must not appear in optionalDependencies pointing at a
    // version that was never published. Loud, because the usual cause is an
    // `ignore:` entry in .goreleaser.yaml rather than a deliberate drop.
    console.warn(`WARNING: no ${t.goos}/${t.goarch} archive in this release — skipping ${SCOPE}/gateway-${t.npm}`);
    continue;
  }

  const dir = path.join(opts.out, `gateway-${t.npm}`);
  fs.mkdirSync(dir, { recursive: true });
  extract(archive, t.ext, [t.exe, "LICENSE"], dir);
  // npm records file modes in the tarball and restores the executable bit on
  // install, so this chmod is what makes the binary runnable on the far side.
  fs.chmodSync(path.join(dir, t.exe), 0o755);

  writeJSON(path.join(dir, "package.json"), {
    name: `${SCOPE}/gateway-${t.npm}`,
    version,
    description: `ferrogw binary for ${t.os} ${t.cpu}. Installed automatically as an optional dependency of \`ferrogw\` — do not depend on it directly.`,
    license: "Apache-2.0",
    homepage: REPO_URL,
    repository: { type: "git", url: `git+${REPO_URL}.git` },
    bugs: { url: `${REPO_URL}/issues` },
    // The pair npm matches against process.platform / process.arch to decide
    // whether to install this package at all. Exactly one of the six matches.
    os: [t.os],
    cpu: [t.cpu],
    // Deliberately no `bin`: six packages each claiming the `ferrogw` command
    // would collide, and only the shim knows which one this host resolved.
    // Which also means Yarn PnP has no reason to unplug this package, and you
    // cannot exec a file inside a PnP zip — so ask for it explicitly.
    preferUnplugged: true,
    // LICENSE and README ship regardless of this list; npm always includes them.
    files: [t.exe],
  });

  built.push({ ...t, archive });
}

// ─── Main package ────────────────────────────────────────────────────────────

const mainDir = path.join(opts.out, "ferrogw");
fs.mkdirSync(path.join(mainDir, "bin"), { recursive: true });
fs.copyFileSync(path.join(here, "bin", "ferrogw.js"), path.join(mainDir, "bin", "ferrogw.js"));

// LICENSE and README come out of the release archive rather than the working
// tree, so the npm landing page describes the version being published and
// nothing else.
extract(built[0].archive, built[0].ext, ["LICENSE", "README.md"], mainDir);

writeJSON(path.join(mainDir, "package.json"), {
  name: "ferrogw",
  version,
  description:
    "Ferro Labs AI Gateway — one OpenAI-compatible API in front of 30+ LLM providers, with routing, guardrails and key management.",
  license: "Apache-2.0",
  author: "Ferro Labs <hello@ferrolabs.ai>",
  homepage: REPO_URL,
  repository: { type: "git", url: `git+${REPO_URL}.git` },
  bugs: { url: `${REPO_URL}/issues` },
  keywords: ["ai", "llm", "gateway", "openai", "anthropic", "proxy", "routing", "ferro", "ferrolabs", "ai-gateway"],
  bin: { ferrogw: "bin/ferrogw.js" },
  files: ["bin"],
  // The shim uses only node:path and node:child_process, but 18 is the floor
  // every current package manager still resolves against.
  engines: { node: ">=18" },
  // Exact versions, not ranges: the shim and the binary are cut from one tag,
  // and a range would let a lockfile pair a new shim with an old binary.
  optionalDependencies: Object.fromEntries(built.map((t) => [`${SCOPE}/gateway-${t.npm}`, version])),
});

// ─── Self-check ──────────────────────────────────────────────────────────────
// This script is the whole distance between a git tag and seven published
// packages, and its failure mode is quiet: a wrong `os` value does not fail a
// publish, it fails every install on that platform, forever, at that version.

const checkedShim = path.join(mainDir, "bin", "ferrogw.js");
execFileSync(process.execPath, ["--check", checkedShim], { stdio: "pipe" });

const mainManifest = JSON.parse(fs.readFileSync(path.join(mainDir, "package.json"), "utf8"));
if (mainManifest.version !== version) fail("main package version does not round-trip");

const expected = built.map((t) => `${SCOPE}/gateway-${t.npm}`).sort();
const declared = Object.keys(mainManifest.optionalDependencies).sort();
if (expected.join(",") !== declared.join(",")) {
  fail(`optionalDependencies (${declared.join(", ")}) do not match the packages built (${expected.join(", ")})`);
}

for (const t of built) {
  const dir = path.join(opts.out, `gateway-${t.npm}`);
  const m = JSON.parse(fs.readFileSync(path.join(dir, "package.json"), "utf8"));
  if (m.name !== `${SCOPE}/gateway-${t.npm}`) fail(`${t.npm}: name is ${m.name}`);
  if (m.version !== version) fail(`${t.npm}: version is ${m.version}, expected ${version}`);
  if (m.os?.length !== 1 || m.os[0] !== t.os) fail(`${t.npm}: os is ${JSON.stringify(m.os)}, expected ["${t.os}"]`);
  if (m.cpu?.length !== 1 || m.cpu[0] !== t.cpu) fail(`${t.npm}: cpu is ${JSON.stringify(m.cpu)}, expected ["${t.cpu}"]`);
  if (mainManifest.optionalDependencies[m.name] !== version) fail(`${t.npm}: not pinned to ${version} by ferrogw`);

  const bin = path.join(dir, t.exe);
  if (!fs.existsSync(bin)) fail(`${t.npm}: ${t.exe} missing`);
  if (!(fs.statSync(bin).mode & 0o111)) fail(`${t.npm}: ${t.exe} is not executable`);
}

console.log(`built ferrogw@${version} + ${built.length} platform packages in ${opts.out}`);
for (const t of built) console.log(`  ${SCOPE}/gateway-${t.npm}`);
if (built.length !== TARGETS.length) {
  console.log(`  (${TARGETS.length - built.length} platform(s) absent from this release — see warnings above)`);
}
