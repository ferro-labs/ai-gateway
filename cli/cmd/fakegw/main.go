// Command fakegw serves deterministic gateway responses for local CLI use.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ferro-labs/ai-gateway/cli/internal/fixture"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	degraded := flag.Bool("degraded", false, "healthy gateway in trouble: 200 with a half-open circuit and one unroutable target")
	noProviders := flag.Bool("no-providers", false, "no credential configured: /health 503 no_providers, /readyz 503 not_ready")
	noAuth := flag.Bool("no-auth", false, "accept unauthenticated admin requests")
	noLogs := flag.Bool("no-log-store", false, "501 the log endpoints, as a gateway without a store does")
	flag.Parse()

	s := fixture.Default()
	// Two independent axes, as on the real gateway: a circuit does not make
	// /health non-200, and only an empty provider set does.
	s.Degraded, s.NoProviders = *degraded, *noProviders
	s.NoLogStore, s.ChatDelay = *noLogs, 60*time.Millisecond
	s.RequireAuth = !*noAuth

	fmt.Fprintf(os.Stderr, "fakegw on %s — key: %s\n", *addr, s.AcceptKey)
	// A bare ":8080" needs a host prepended; "127.0.0.1:8101" already has one.
	host := *addr
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	}
	fmt.Fprintf(os.Stderr, "  FERRO_URL=http://%s FERRO_API_KEY=%s go run ./cmd/ferro\n", host, s.AcceptKey)
	srv := &http.Server{
		Addr:    *addr,
		Handler: fixture.Handler(s),
		// ReadHeaderTimeout is the Slowloris bound — a client that opens a
		// connection and dribbles headers forever is cut off here — and
		// ReadTimeout bounds the body after it. Neither can cut a stream
		// short: net/http clears the read deadline the moment the request
		// body is consumed (connReader.startBackgroundRead), which is before
		// the first SSE frame is written.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// WriteTimeout is deliberately zero. It bounds the WHOLE response, and
		// this server's chat surface is SSE: a finite value would cut a stream
		// off mid-flight and, worse, do it silently — the client sees a
		// truncated stream with no error frame and no [DONE], which is exactly
		// the failure the playground dev loop exists to make visible. Nothing
		// is given up by leaving it off, because the Slowloris vector
		// ReadHeaderTimeout covers is the one G114 is about.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
