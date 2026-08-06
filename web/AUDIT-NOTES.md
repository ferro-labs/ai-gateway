# npm audit exceptions

`npm audit` is run on demand here, not as a CI gate, so this file is a note for
whoever runs it — not a scanner config. There is no `osv-scanner.toml`,
`.nsprc`, or `audit-ci` allowlist because no tool that reads one runs against
`web/`.

There are currently no exceptions. The last one — GHSA-qwww-vcr4-c8h2, a
react-router 7.x false positive whose advisory range (`>=7.12.0 <8.3.0`) was
never corrected for the upstream 7.18.2 backport — was retired when the
dashboard moved to `react-router@8.3.0`, which sits outside the range and
carries the fix natively. Its full analysis is in this file's git history.
