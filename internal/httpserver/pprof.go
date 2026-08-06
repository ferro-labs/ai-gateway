package httpserver

import (
	httppprof "net/http/pprof"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"
)

// mountPprofRoutes registers /debug/pprof/* when ENABLE_PPROF is set.
//
// It registers no protection of its own and must only ever be called from
// inside the admin-scoped /debug group in mountObservabilityRoutes — see the
// tier split documented there. ENABLE_PPROF decides whether these routes exist;
// it says nothing about who may call them, and mounting them anywhere else
// publishes a heap dump to whoever can authenticate.
func mountPprofRoutes(r chi.Router) {
	if !PprofEnabled() {
		return
	}

	r.Route("/debug/pprof", func(r chi.Router) {
		r.Get("/", httppprof.Index)
		r.Get("/cmdline", httppprof.Cmdline)
		r.Get("/profile", httppprof.Profile)
		r.Post("/symbol", httppprof.Symbol)
		r.Get("/symbol", httppprof.Symbol)
		r.Get("/trace", httppprof.Trace)
		r.Get("/allocs", httppprof.Handler("allocs").ServeHTTP)
		r.Get("/block", httppprof.Handler("block").ServeHTTP)
		r.Get("/goroutine", httppprof.Handler("goroutine").ServeHTTP)
		r.Get("/heap", httppprof.Handler("heap").ServeHTTP)
		r.Get("/mutex", httppprof.Handler("mutex").ServeHTTP)
		r.Get("/threadcreate", httppprof.Handler("threadcreate").ServeHTTP)
	})
}

// PprofEnabled reports whether ENABLE_PPROF mounts the profiling routes. It is
// exported so startup can report the same answer the router acts on, rather
// than a second reading of the variable that could drift from it.
func PprofEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("ENABLE_PPROF")))
	return value == "1" || value == "true" || value == "yes"
}
