// Command mockllm is a tiny OpenAI-compatible upstream: it answers
// chat-completion and embedding requests with randomized latency, token counts,
// and a small fraction of errors, so a gateway's metrics and traces carry
// realistic, varied data without any call to a real provider.
//
// It is a reusable test tool, driven entirely by env vars:
//
//	PORT                  listen port                     (default 9090)
//	MOCK_LATENCY_MIN_MS   fast-path latency floor         (default 40)
//	MOCK_LATENCY_MAX_MS   fast-path latency ceiling       (default 200)
//	MOCK_ERROR_PCT        percent of requests failing 5xx (default 3)
//	MOCK_RATE_LIMIT_PCT   percent of requests failing 429 (default 2)
//
// Run it standalone with `go run .`, or build the image from this directory.
package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Tunables, read once at startup from the environment.
var (
	latencyMinMS = envInt("MOCK_LATENCY_MIN_MS", 40)
	latencyMaxMS = envInt("MOCK_LATENCY_MAX_MS", 200)
	errorPct     = envInt("MOCK_ERROR_PCT", 3)
	rateLimitPct = envInt("MOCK_RATE_LIMIT_PCT", 2)
)

type chatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type embeddingRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

func main() {
	addr := ":" + env("PORT", "9090")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Dispatch by path suffix, not exact path, so any provider's base URL works:
	// each provider appends its own version prefix (/v1, /openai/v1, …), and all
	// of them end in /chat/completions or /embeddings.
	mux.HandleFunc("/", dispatch)
	log.Printf("mockllm listening on %s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// dispatch routes by the operation at the tail of the path, so it does not
// matter which version prefix a provider prepends to its base URL.
func dispatch(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case strings.HasSuffix(p, "/chat/completions"), strings.HasSuffix(p, "/completions"):
		chat(w, r)
	case strings.HasSuffix(p, "/embeddings"):
		embeddings(w, r)
	default:
		http.NotFound(w, r)
	}
}

// chat answers a chat-completion request. Latency, token counts, and the occasional
// error are randomized so the dashboard's rate, latency, error, token, and cost
// panels all show movement.
func chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	model := orDefault(req.Model, "gpt-4o-mini")

	sleepRandom()
	if code, ok := maybeError(); ok {
		writeError(w, code)
		return
	}

	prompt := 0
	for _, m := range req.Messages {
		prompt += len(m.Content) / 4
	}
	prompt += 8 + rand.Intn(40)       //nolint:gosec // demo data, not security-sensitive
	completion := 12 + rand.Intn(220) //nolint:gosec // demo data, not security-sensitive

	writeJSON(w, map[string]any{
		"id":      "chatcmpl-" + randHex(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": "This is a mock completion for the observability demo."},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     prompt,
			"completion_tokens": completion,
			"total_tokens":      prompt + completion,
		},
	})
}

// embeddings answers /v1/embeddings with a short random vector and usage, so the
// token panels include the embedding surface too.
func embeddings(w http.ResponseWriter, r *http.Request) {
	var req embeddingRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	model := orDefault(req.Model, "text-embedding-3-small")

	sleepRandom()
	if code, ok := maybeError(); ok {
		writeError(w, code)
		return
	}

	vec := make([]float64, 8)
	for i := range vec {
		vec[i] = rand.Float64()*2 - 1 //nolint:gosec // demo data, not security-sensitive
	}
	prompt := 8 + rand.Intn(60) //nolint:gosec // demo data, not security-sensitive

	writeJSON(w, map[string]any{
		"object": "list",
		"model":  model,
		"data":   []any{map[string]any{"object": "embedding", "index": 0, "embedding": vec}},
		"usage":  map[string]any{"prompt_tokens": prompt, "total_tokens": prompt},
	})
}

// sleepRandom models upstream latency: mostly fast with a long tail, so the
// latency-percentile panels have a spread to show.
func sleepRandom() {
	span := max(latencyMaxMS-latencyMinMS, 1)
	base := latencyMinMS + rand.Intn(span) //nolint:gosec // demo data, not security-sensitive
	if rand.Intn(100) < 12 {               //nolint:gosec // demo data, not security-sensitive
		base += 200 + rand.Intn(600) // occasional slow request for the p99 tail
	}
	time.Sleep(time.Duration(base) * time.Millisecond)
}

// maybeError returns a status to fail with, on a small fraction of requests: a
// 5xx (which counts toward the circuit breaker) more often than a 429 (which
// does not), so the reliability panels distinguish the two.
func maybeError() (int, bool) {
	switch n := rand.Intn(100); { //nolint:gosec // demo data, not security-sensitive
	case n < errorPct:
		return http.StatusInternalServerError, true
	case n < errorPct+rateLimitPct:
		return http.StatusTooManyRequests, true
	default:
		return 0, false
	}
}

func writeError(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": "mock upstream error", "type": "mock_error", "code": code},
	})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func randHex() string {
	const hex = "0123456789abcdef"
	var b strings.Builder
	for range 16 {
		b.WriteByte(hex[rand.Intn(len(hex))]) //nolint:gosec // demo id, not security-sensitive
	}
	return b.String()
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
