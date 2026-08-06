# npm audit exceptions

`npm audit` is run on demand here, not as a CI gate, so this file is a note for
whoever runs it — not a scanner config. There is no `osv-scanner.toml`,
`.nsprc`, or `audit-ci` allowlist because no tool that reads one runs against
`web/`.

## GHSA-qwww-vcr4-c8h2 — react-router / react-router-dom — expected, already fixed

**Re-check after 2026-10-31.** If the advisory range has been corrected by then,
delete this entry. If it has not, confirm the two facts below still hold and
extend the date — do not extend it unread.

`npm audit` reports this advisory against react-router at any 7.x version. It is
a false positive here for two independent reasons:

1. **Already patched.** The fix was backported to 7.x by upstream PR #15353 and
   released as 7.18.2, which is what the lockfile resolves. The advisory's
   affected range still reads `>=7.12.0 <8.3.0` and has not been corrected for
   the backport, so the scanner keeps matching a patched version.
2. **Not reachable.** The vulnerable code ships only under the `react-server`
   export condition. This is a client-only Vite SPA using `<BrowserRouter>`
   (`src/App.tsx`), with no RSC server and no custom `resolve.conditions`, so
   that condition is never set. Diffing the published 7.18.1 and 7.18.2
   tarballs, the client-reachable module graph is byte-identical apart from the
   version string; the CSRF hardening appears only in `index-react-server.mjs`.

Verify claim 1 in one command:

```sh
npm view react-router@7.18.2 dist.tarball   # then read its CHANGELOG.md
```
