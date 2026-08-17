// The Worker behind https://get.ferrolabs.ai — it serves the two installer
// scripts, and nothing else.
//
// It serves the bytes itself rather than redirecting to raw.githubusercontent.com.
// A redirect would move the trust anchor of `curl … | sh` onto a third party we
// do not control, and it breaks the one-liner outright behind the corporate
// proxies that refuse cross-origin redirects — which is precisely the audience
// a one-line installer exists for.
//
// The scripts are inlined into this bundle at deploy time from their single
// source of truth, scripts/install/. See the [build] command in wrangler.toml;
// scripts.generated.js is a build artifact and is not committed.
import { installSh, installPs1 } from "./scripts.generated.js";

// Where `/` sends a human. The only value in this file that moves when the docs
// information architecture does, so it is named once.
//
// It points at the install page rather than the quickstart on purpose: someone
// who typed this hostname into a browser is choosing between brew, npm, pip,
// scoop and the one-liner, and the quickstart answers a different question
// (first request in two minutes, via one preselected method).
const DOCS_INSTALL_URL = "https://docs.ferrolabs.ai/getting-started/install/";

// Deliberately conservative. An unknown or absent User-Agent falls through to
// the browser redirect: a person reading the URL is the ambiguous case, and any
// scripted client can always name the file it wants.
const CURL_UA = /^(curl|wget)\//i;

// Windows PowerShell 5.1 and pwsh 7 both send a `Mozilla/5.0 (…)` prefix and
// differ only in the trailing product token, so this must be tested before
// anything browser-shaped — which is why the browser case is the fallthrough
// rather than a pattern of its own.
const POWERSHELL_UA = /(?:Windows)?PowerShell\//i;

// text/x-shellscript is what the spec asks for; PowerShell gets text/plain.
// The charset is load-bearing on the PowerShell path: `Invoke-RestMethod` on
// PS 5.1 falls back to ISO-8859-1 for a text/* response that declares none, so
// an unlabelled script mojibakes the moment it grows a non-ASCII byte.
const SH_TYPE = "text/x-shellscript; charset=utf-8";
const PS1_TYPE = "text/plain; charset=utf-8";

// Five minutes. There is no version in these URLs, so the resource is mutable
// by construction and cannot be immutable-cached; the window is the trade
// between the two failures that matter. Long enough that a launch-day link
// being passed around is answered by intermediate caches instead of re-entering
// the Worker for every reader; short enough that a broken installer is gone
// from every proxy and browser within one coffee, which is the number that
// decides how bad a bad release feels.
const SCRIPT_CACHE = "public, max-age=300";

const serveScript = (body, contentType, method) =>
  new Response(method === "HEAD" ? null : body, {
    headers: {
      "content-type": contentType,
      "cache-control": SCRIPT_CACHE,
      // These are read by shells rather than rendered, but a sniffing
      // intermediary deciding otherwise is a variable with no upside.
      "x-content-type-options": "nosniff",
    },
  });

// Plain text, because everything that reaches it is a terminal or a bare fetch.
const NOT_FOUND = `Not found. This host serves the ferrogw installers only.

  curl -fsSL https://get.ferrolabs.ai/install.sh | sh   # Linux, macOS
  irm https://get.ferrolabs.ai/install.ps1 | iex        # Windows

${DOCS_INSTALL_URL}
`;

export default {
  fetch(request) {
    const { pathname } = new URL(request.url);
    const ua = request.headers.get("user-agent") ?? "";

    // An explicit path always wins over sniffing: a client that named the file
    // gets that file, whatever its User-Agent claims to be.
    if (pathname === "/install.sh") {
      return serveScript(installSh, SH_TYPE, request.method);
    }
    if (pathname === "/install.ps1") {
      return serveScript(installPs1, PS1_TYPE, request.method);
    }

    if (pathname === "/") {
      if (CURL_UA.test(ua)) return serveScript(installSh, SH_TYPE, request.method);
      if (POWERSHELL_UA.test(ua)) return serveScript(installPs1, PS1_TYPE, request.method);
      return new Response(null, {
        status: 302,
        headers: {
          location: DOCS_INSTALL_URL,
          // Not cached: the target moves with the docs, and a redirect pinned
          // in a browser cache outlives every fix we could ship here.
          "cache-control": "no-store",
        },
      });
    }

    return new Response(NOT_FOUND, {
      status: 404,
      headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" },
    });
  },
};
