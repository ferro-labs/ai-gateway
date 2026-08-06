// Package webui embeds the built operations dashboard and serves it as a
// single-page application.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// placeholderHTML stands in for the dashboard when no bundle was embedded.
//
// It is a constant rather than a committed dist/index.html because the build
// writes the real index.html to exactly that path: a tracked file there would
// be modified by every `make build`, and the first time someone committed the
// result the marker identifying it as a stand-in would be gone and the gateway
// would report a dashboard it does not have. Nothing in dist/ is tracked, so
// there is nothing a build can dirty.
//
// No inline <script>: the Content-Security-Policy this is served under forbids
// it, and a placeholder that trips a console error teaches the wrong lesson.
const placeholderHTML = `<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Dashboard not built</title>
  </head>
  <body>
    <h1>Dashboard bundle not built</h1>
    <p>
      This binary was compiled without the operations dashboard. Run
      <code>make build</code>, which builds the web application and copies its
      output into <code>internal/webui/dist</code> before compiling the binary.
    </p>
  </body>
</html>
`

const indexFile = "index.html"

// assetsPrefix is the directory Vite writes content-hashed bundles to. A
// changed file always gets a changed name there, which is what makes the
// year-long immutable lifetime below safe to hand out.
const assetsPrefix = "assets/"

const (
	immutableCacheControl = "public, max-age=31536000, immutable"
	noStoreCacheControl   = "no-store"
)

// The build pipeline copies web/dist here before `go build`, because go:embed
// cannot reference paths outside the package directory. Only .gitkeep is
// committed, which is enough to keep the pattern resolvable in a tree where the
// dashboard was never built — `all:` is what makes a dotfile count.
//
//go:embed all:dist
var embedded embed.FS

var dist = func() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// Unreachable: "dist" is a valid path and is embedded directly above.
		panic("webui: embedded dist unreadable: " + err.Error())
	}
	return sub
}()

var available = func() bool {
	_, err := fs.Stat(dist, indexFile)
	return err == nil
}()

// Available reports whether a dashboard build is embedded. It is false in a
// binary compiled straight from a clean checkout, where the handler answers
// with placeholderHTML and a 503 instead of an app shell.
func Available() bool { return available }

// Handler serves the embedded dashboard. A path matching an embedded file is
// served directly; anything else falls back to index.html so client-side
// routes survive a hard refresh or a deep link.
//
// It resolves against r.URL.Path, so mount it at the site root or wrap it in
// http.StripPrefix. It sets no security headers — the global middleware owns
// those.
func Handler() http.Handler { return handlerFor(dist, available) }

// handlerFor is Handler's body over an explicit filesystem, so tests can drive
// both states — bundle embedded and not — without depending on whether someone
// has run `make build` in this tree. Handler() alone would make the suite pass
// or fail on the contents of a generated directory.
func handlerFor(fsys fs.FS, hasBundle bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Without this the SPA fallback would answer a mistyped or wrong-method
		// API call with a 200 and an HTML body, hiding the mistake from the
		// client that made it.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = indexFile
		}

		// A directory takes the fallback too: the embedded tree has no index
		// files below the root, so a directory request is a client-side route
		// as far as this handler is concerned.
		if info, err := fs.Stat(fsys, name); err != nil || info.IsDir() {
			serveIndex(w, r, fsys, hasBundle)
			return
		}

		w.Header().Set("Cache-Control", cacheControl(name))
		http.ServeFileFS(w, r, fsys, name)
	})
}

// serveIndex answers a path with no embedded file. Client-side routes
// (/overview, /keys, /login, …) exist only inside the bundle, so a browser
// navigating straight to one must still receive the app shell. An API client
// must not: it asked for a path that does not exist, and an HTML 200 would
// turn that into a parse error somewhere further downstream.
func serveIndex(w http.ResponseWriter, r *http.Request, fsys fs.FS, hasBundle bool) {
	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", noStoreCacheControl)
	if !hasBundle {
		// Written by hand rather than through http.Error, which forces
		// Content-Type to text/plain and would ship the markup as visible text.
		// 503 rather than 200: this build genuinely cannot serve a dashboard,
		// and a monitor pointed at the page should say so.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, placeholderHTML)
		}
		return
	}
	http.ServeFileFS(w, r, fsys, indexFile)
}

// cacheControl picks a caching policy by path. Only the content-hashed bundles
// under assets/ can be cached hard, because a new build gives them new names.
// Every other file keeps its name across deploys — index.html above all, whose
// whole job is to name the current bundles — so caching one would pin a client
// to asset names that no longer exist.
func cacheControl(name string) string {
	if strings.HasPrefix(name, assetsPrefix) {
		return immutableCacheControl
	}
	return noStoreCacheControl
}
