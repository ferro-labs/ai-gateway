#!/usr/bin/env node
// The `bin` of the `ferrogw` npm package: find the binary that this host's
// platform package shipped, then hand the process over to it.
//
// Why a shim exists at all: one npm package cannot carry six platform binaries
// without every install paying for five it will never run. So the binaries live
// in `@ferro-labs-ai/gateway-<platform>` packages that carry `os`/`cpu` fields —
// npm installs exactly the one that matches and silently skips the rest — and
// this file is the single `bin` entry the user actually gets.
//
// Deliberately NOT a postinstall downloader. `--ignore-scripts` is increasingly
// the corporate default, an offline install has nothing to download, and a
// download records no integrity hash in the lockfile. Optional dependencies are
// integrity-checked like any other and need no script execution.

"use strict";

const path = require("node:path");
const { spawn } = require("node:child_process");

// npm's platform vocabulary, not Go's: process.arch says `x64` where the
// release archive says `amd64`, and process.platform says `win32` where it says
// `windows`. The package names follow npm, because `os`/`cpu` are matched
// against these exact values.
const pkg = `@ferro-labs-ai/gateway-${process.platform}-${process.arch}`;
const exe = process.platform === "win32" ? "ferrogw.exe" : "ferrogw";

let binary;
try {
  // Resolve the platform package's manifest and walk to the binary beside it,
  // rather than resolving the binary path directly: the POSIX binary has no
  // extension, and leaning on require.resolve's extension search for that is a
  // subtlety with no upside. The platform packages declare no `exports`, so
  // `<pkg>/package.json` is always resolvable.
  binary = path.join(require.resolve(`${pkg}/package.json`), "..", exe);
} catch {
  console.error(
    `ferrogw: no prebuilt binary for ${process.platform}-${process.arch}.\n` +
      `\n` +
      `The optional dependency ${pkg} is not installed. Either this platform is\n` +
      `not one the gateway publishes binaries for, or the install ran with\n` +
      `--omit=optional / --no-optional and skipped it.\n` +
      `\n` +
      `Other install paths: https://get.ferrolabs.ai`,
  );
  process.exit(1);
}

// spawn, not spawnSync. On POSIX the ideal is to replace this process with the
// binary outright, but Node exposes no execve, so a parent process survives for
// the whole life of the server and has to behave like one. spawnSync blocks the
// event loop, so no handler could ever run: Ctrl-C would still reach the child
// (a tty signals the entire foreground process group), but a `kill`, a
// `docker stop` or a systemd unit stop targets the parent alone, and that
// SIGTERM has to be relayed or the gateway never gets its graceful shutdown.
const child = spawn(binary, process.argv.slice(2), { stdio: "inherit" });

// SIGBREAK does not exist off Windows; Node treats an unknown signal name as an
// ordinary event that simply never fires, so the list needs no per-OS branch.
const FORWARDED = ["SIGINT", "SIGTERM", "SIGHUP", "SIGBREAK"];
for (const signal of FORWARDED) {
  process.on(signal, () => {
    // `child.killed` records only that a signal was ever SENT -- it flips true
    // on the first kill() and stays true while the child keeps running, which
    // is the normal case here, because the gateway catches SIGINT/SIGTERM to
    // shut down gracefully. Guarding on it relays the first signal and swallows
    // every one after it, so a supervisor escalating SIGINT -> SIGTERM ->
    // SIGKILL gets only the SIGINT through and then waits out its whole timeout
    // on a process nobody asked again. exitCode/signalCode are both null
    // exactly while the child is running, and one of them is set the moment it
    // is not, which is the fact this guard actually wants.
    //
    // Wrapped because this runs INSIDE a signal handler, where an uncaught
    // throw takes the shim down and orphans the gateway. On Windows only
    // SIGTERM/SIGKILL/SIGINT terminate a process, and libuv raises EINVAL for
    // the other two names in FORWARDED -- so on the one platform where SIGHUP
    // and SIGBREAK are reachable, relaying them is what would kill the relay.
    if (child.exitCode === null && child.signalCode === null) {
      try {
        child.kill(signal);
      } catch {
        // Nothing useful to do: the child is alive and this platform will not
        // deliver this signal. Staying up keeps the exit-code and re-raise
        // paths below intact, which is the more important guarantee.
      }
    }
  });
}

child.on("error", (err) => {
  console.error(`ferrogw: cannot execute ${binary}: ${err.message}`);
  process.exit(1);
});

child.on("close", (code, signal) => {
  if (signal) {
    // Re-raise so the wait status a supervisor reads says "killed by SIGTERM"
    // rather than "exited 143" — those are different facts and only one of them
    // is true. Handlers come off first or we would trap our own signal.
    for (const s of FORWARDED) process.removeAllListeners(s);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
