package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	aigateway "github.com/ferro-labs/ai-gateway"
)

// readyzPingTimeout bounds how long Readyz waits for all backing-store pings
// combined before reporting the gateway not ready.
const readyzPingTimeout = 2 * time.Second

// Pinger is the minimal reachability probe Readyz applies to each backing store.
// The API key store and the config manager both satisfy it.
type Pinger interface {
	Ping(context.Context) error
}

// providerCircuit is the per-provider circuit state reported by Readyz.
type providerCircuit struct {
	Name    string `json:"name"`
	Circuit string `json:"circuit"`
}

// Livez handles GET /livez. It reports process liveness only, performing no
// dependency checks, so it always returns 200. Orchestrators use it to decide
// whether to restart the process.
func Livez() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// Readyz handles GET /readyz. It reports whether the gateway can serve traffic:
// config must be loaded, every pinger must be reachable within
// readyzPingTimeout, and at least one provider must have a non-open circuit. It
// returns 200 with the readiness snapshot when ready, otherwise 503 with a
// reason naming the failed check.
func Readyz(gw *aigateway.Gateway, pingers ...Pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gw == nil {
			writeNotReady(w, "gateway not configured")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), readyzPingTimeout)
		defer cancel()
		for _, p := range pingers {
			if p == nil {
				continue
			}
			if err := p.Ping(ctx); err != nil {
				writeNotReady(w, "store unreachable: "+err.Error())
				return
			}
		}

		readiness := gw.Readiness()
		if !readiness.Ready {
			writeNotReady(w, "no ready providers")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status":    "ready",
			"providers": circuitsFromReadiness(readiness),
		})
	}
}

// writeNotReady emits a 503 with a JSON body naming the failed readiness check.
// Reasons never include secrets — store pings surface connection errors only.
func writeNotReady(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status": "not_ready",
		"reason": reason,
	})
}

// circuitsFromReadiness projects the gateway readiness snapshot onto the
// JSON-tagged response shape.
func circuitsFromReadiness(r aigateway.Readiness) []providerCircuit {
	out := make([]providerCircuit, 0, len(r.Providers))
	for _, p := range r.Providers {
		out = append(out, providerCircuit{Name: p.Name, Circuit: p.Circuit})
	}
	return out
}
