# npm audit exceptions

`npm audit` is run on demand here, not as a CI gate, so this file is a note for
whoever runs it — not a scanner config. There is no `osv-scanner.toml`,
`.nsprc`, or `audit-ci` allowlist because no tool that reads one runs against
`web/`.

There are currently no exceptions.

## GHSA-qwww-vcr4-c8h2 — react-router — retired 2026-08-06

Retired when the dashboard moved to `react-router@8.3.0`, the version the
advisory lists as patched: the advisory no longer matches the lockfile and
`npm audit` reports nothing. While the dashboard was on 7.18.2 this was the
one exception, documented as a false positive for two reasons — both verified,
and both still true of the 7.x line:

1. **7.18.2 carries the backported fix.** Its published changelog entry reads
   "Harden RSC CSRF codepaths" (upstream PR #15353). The advisory's affected
   range (`>=7.12.0 <8.3.0`) was never updated to exclude it, so scanners kept
   matching a patched version. Verify in one command:
   `npm pack react-router@7.18.2` and read `package/CHANGELOG.md` — do not
   trust secondhand summaries of the advisory here; several state that no 7.x
   patch exists.
2. **The vulnerable surface is RSC-only.** The hardening lives under the
   `react-server` export condition. This is a client-only Vite SPA using
   `<BrowserRouter>` with no RSC server and no custom `resolve.conditions`,
   so that condition is never set; the client-reachable module graph of
   7.18.1 and 7.18.2 is byte-identical apart from the version string.
