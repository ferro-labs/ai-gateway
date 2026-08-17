# `get.ferrolabs.ai`

The Cloudflare Worker that serves the `ferrogw` installers.

| Request | Response |
|---|---|
| `/install.sh`, or `/` with a curl/wget User-Agent | `install.sh`, `text/x-shellscript` |
| `/install.ps1`, or `/` with a PowerShell User-Agent | `install.ps1`, `text/plain` |
| `/` with a browser, unknown, or absent User-Agent | `302` to the docs install page |
| anything else | `404` with a plain-text pointer |

An explicit path always beats User-Agent sniffing.

**The Worker serves the script bytes; it does not redirect to
`raw.githubusercontent.com`.** A redirect would move the trust anchor of
`curl … | sh` onto a third party, and it breaks behind proxies that refuse
cross-origin redirects.

`scripts/install/install.sh` and `scripts/install/install.ps1` are the single
source of truth.
The `[build]` command in `wrangler.toml` inlines them into the bundle on every
`wrangler dev` and `wrangler deploy`, emitting `scripts.generated.js` — a build
artifact. **Do not commit it** — committing it would give the installers a
second home that can silently disagree with the first, and it is the copy the
edge would actually serve. `.gitignore` covers both it and `.wrangler/`.

## Deploy

Automatic: `.github/workflows/deploy-get-worker.yml` deploys on a push to `main`
touching `scripts/install/**` or `deploy/get-worker/**`, and on
`workflow_dispatch`. It is deliberately *not* triggered by a release — the
installers resolve the version at runtime, so a tag changes nothing here.

By hand, from this directory:

```sh
npx wrangler@4.123.0 dev      # local, on 127.0.0.1
npx wrangler@4.123.0 deploy   # requires the credentials below
```

## Credentials

Two repository secrets:

| Secret | Value |
|---|---|
| `CLOUDFLARE_API_TOKEN` | API token, permissions below |
| `CLOUDFLARE_ACCOUNT_ID` | The account ID from the Cloudflare dashboard sidebar |

Create the token under **My Profile → API Tokens → Create Token**. The built-in
**Edit Cloudflare Workers** template covers it; scope the zone half to
`ferrolabs.ai` and the account half to the account that owns the zone. The
permissions that are actually required:

| Scope | Permission | Why |
|---|---|---|
| Account | Workers Scripts — Edit | Upload the Worker |
| Account | Account Settings — Read | Resolve the account |
| Zone (`ferrolabs.ai`) | Workers Routes — Edit | Attach the custom domain |
| Zone (`ferrolabs.ai`) | Zone — Read | Resolve the zone by name |
| Zone (`ferrolabs.ai`) | DNS — Edit | Create the `get` record on first deploy |

Nothing else. No KV, no R2, no D1 — this Worker has no bindings and no secrets
of its own.

## DNS

`routes` in `wrangler.toml` declares `get.ferrolabs.ai` as a **Custom Domain**,
so **wrangler creates the DNS record itself** on the first deploy: a proxied
(orange-cloud) `CNAME` for `get` managed by Workers. Do not hand-create it
first — a pre-existing record on that name makes the deploy fail with a
conflict.

Prerequisite: the `ferrolabs.ai` zone is on the same Cloudflare account as the
Worker. It already is — the zone runs on `diana`/`conrad.ns.cloudflare.com`.

To verify after a deploy (the workflow does this itself):

```sh
curl -fsSL https://get.ferrolabs.ai/install.sh | head -1
curl -sS -o /dev/null -w '%{http_code}\n' -A 'Mozilla/5.0' https://get.ferrolabs.ai/
```
