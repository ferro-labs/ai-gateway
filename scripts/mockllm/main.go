// Command mockllm is a tiny OpenAI-compatible upstream. Out of the box it
// answers chat-completion and embedding requests with randomized latency,
// token counts, and a small fraction of errors, so a gateway's metrics and
// traces carry realistic, varied data without any call to a real provider.
//
// It is also a scriptable test double: a scenario set through /_mock/scenario
// switches the instance to deterministic behaviour — fail the next N calls
// with a status, carry a Retry-After, add latency, hang until the client
// leaves, or fail a stream after N chunks — and /_mock/calls reports what the
// instance was asked. That is what scripts/strategy_e2e.sh drives.
//
// Process configuration comes from the environment:
//
//	PORT                  listen port                     (default 9090)
//	MOCK_NAME             name echoed in every completion (default mockllm)
//	MOCK_LATENCY_MIN_MS   demo-mode latency floor         (default 40)
//	MOCK_LATENCY_MAX_MS   demo-mode latency ceiling       (default 200)
//	MOCK_ERROR_PCT        demo-mode requests failing 5xx  (default 3)
//	MOCK_RATE_LIMIT_PCT   demo-mode requests failing 429  (default 2)
//
// Control endpoints:
//
//	POST   /_mock/scenario  {"status":503,"fail_count":2,"retry_after_s":1,
//	                         "delay_ms":100,"hang":false,"stream_fail_after":2}
//	                        Sets the scenario and turns demo randomness off.
//	                        An empty object {} is a healthy, deterministic
//	                        instance. status fails every call until cleared;
//	                        with fail_count it fails only the next N calls and
//	                        then heals. Order per call: delay, hang, status.
//	DELETE /_mock/scenario  Back to demo mode.
//	GET    /_mock/calls     {"calls","streams","last_model","models"}
//	POST   /_mock/reset     Clear the scenario and the counters.
//
// Run it standalone with `go run .`, or build the image from this directory.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// settings is the process configuration, read once from the environment.
type settings struct {
	name         string
	latencyMinMS int
	latencyMaxMS int
	errorPct     int
	rateLimitPct int
}

func settingsFromEnv() settings {
	return settings{
		name:         env("MOCK_NAME", "mockllm"),
		latencyMinMS: envInt("MOCK_LATENCY_MIN_MS", 40),
		latencyMaxMS: envInt("MOCK_LATENCY_MAX_MS", 200),
		errorPct:     envInt("MOCK_ERROR_PCT", 3),
		rateLimitPct: envInt("MOCK_RATE_LIMIT_PCT", 2),
	}
}

// scenario is the deterministic behaviour a test asks for.
type scenario struct {
	// Status fails calls with this HTTP status; 0 answers normally.
	Status int `json:"status"`
	// FailCount limits Status to the next N calls, after which the instance
	// heals. 0 means every call while Status is set.
	FailCount int `json:"fail_count"`
	// RetryAfterS is sent as a Retry-After header on failed calls.
	RetryAfterS int `json:"retry_after_s"`
	// DelayMS is added before every answer.
	DelayMS int `json:"delay_ms"`
	// Hang holds every call open until the client goes away.
	Hang bool `json:"hang"`
	// StreamFailAfter ends a stream with an error frame after N chunks.
	StreamFailAfter int `json:"stream_fail_after"`
}

// decision is what one call does, resolved under the lock so counters and
// the fail budget stay consistent under concurrent requests.
type decision struct {
	demo            bool
	delay           time.Duration
	hang            bool
	status          int
	retryAfterS     int
	streamFailAfter int
}

type mock struct {
	cfg settings

	mu        sync.Mutex
	active    bool // a scenario is set; demo randomness is off
	sc        scenario
	remaining int // failures left while sc.FailCount > 0
	calls     int
	streams   int
	lastModel string
	models    map[string]int
}

func main() {
	cfg := settingsFromEnv()
	addr := ":" + env("PORT", "9090")
	// ReadTimeout bounds the body read as well as the headers; a hung
	// scenario holds the response, never the request.
	srv := &http.Server{Addr: addr, Handler: newHandler(cfg), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second}
	log.Printf("mockllm %q listening on %s", cfg.name, addr)
	log.Fatal(srv.ListenAndServe())
}

// newHandler builds the mock's HTTP surface.
func newHandler(cfg settings) http.Handler {
	m := &mock{cfg: cfg, models: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/_mock/scenario", m.handleScenario)
	mux.HandleFunc("/_mock/calls", m.handleCalls)
	mux.HandleFunc("/_mock/reset", m.handleReset)
	// Dispatch by path suffix, not exact path, so any provider's base URL works:
	// each provider appends its own version prefix (/v1, /openai/v1, …), and all
	// of them end in /chat/completions or /embeddings.
	mux.HandleFunc("/", m.dispatch)
	return mux
}

func (m *mock) handleScenario(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var sc scenario
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&sc); err != nil {
			http.Error(w, "scenario: "+err.Error(), http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		m.active, m.sc, m.remaining = true, sc, sc.FailCount
		m.mu.Unlock()
	case http.MethodDelete:
		m.mu.Lock()
		m.active, m.sc, m.remaining = false, scenario{}, 0
		m.mu.Unlock()
	default:
		http.Error(w, "use POST or DELETE", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *mock) handleCalls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "use GET", http.StatusMethodNotAllowed)
		return
	}
	m.mu.Lock()
	models := maps.Clone(m.models)
	body := map[string]any{"calls": m.calls, "streams": m.streams, "last_model": m.lastModel, "models": models}
	m.mu.Unlock()
	writeJSON(w, body)
}

func (m *mock) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	m.mu.Lock()
	m.active, m.sc, m.remaining = false, scenario{}, 0
	m.calls, m.streams, m.lastModel, m.models = 0, 0, "", map[string]int{}
	m.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (m *mock) dispatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Mock-Name", m.cfg.name)
	switch p := r.URL.Path; {
	case strings.HasSuffix(p, "/chat/completions"), strings.HasSuffix(p, "/completions"):
		m.chatCompletion(w, r)
	case strings.HasSuffix(p, "/embeddings"):
		m.embedding(w, r)
	default:
		http.NotFound(w, r)
	}
}

// decide records the call and resolves what the scenario says to do with it.
func (m *mock) decide(model string, stream bool) decision {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if stream {
		m.streams++
	}
	m.lastModel = model
	m.models[model]++
	if !m.active {
		return decision{demo: true}
	}
	d := decision{
		delay:           time.Duration(m.sc.DelayMS) * time.Millisecond,
		hang:            m.sc.Hang,
		retryAfterS:     m.sc.RetryAfterS,
		streamFailAfter: m.sc.StreamFailAfter,
	}
	switch {
	case m.sc.Status == 0:
	case m.sc.FailCount == 0:
		d.status = m.sc.Status
	case m.remaining > 0:
		m.remaining--
		d.status = m.sc.Status
	}
	return d
}

// admit applies the parts of a decision that happen before any answer and
// reports whether the call may go on to answer.
func (m *mock) admit(w http.ResponseWriter, r *http.Request, d decision) bool {
	if d.demo {
		m.cfg.sleepRandom()
		if code, ok := m.cfg.maybeError(); ok {
			writeError(w, code, 0)
			return false
		}
		return true
	}
	if d.delay > 0 {
		select {
		case <-time.After(d.delay):
		case <-r.Context().Done():
			return false
		}
	}
	if d.hang {
		<-r.Context().Done()
		return false
	}
	if d.status != 0 {
		writeError(w, d.status, d.retryAfterS)
		return false
	}
	return true
}

type chatRequest struct {
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type embeddingRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

func (m *mock) chatCompletion(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	model := orDefault(req.Model, "gpt-4o-mini")

	d := m.decide(model, req.Stream)
	if !m.admit(w, r, d) {
		return
	}
	promptTokens := m.promptTokens(req, d.demo)
	if req.Stream {
		m.streamChat(w, r, model, promptTokens, d.streamFailAfter)
		return
	}
	writeJSON(w, map[string]any{
		"id":      "chatcmpl-mock-" + randHex(),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": m.content()},
			"finish_reason": "stop",
		}},
		"usage": usage(promptTokens, len(m.words())),
	})
}

// streamChat answers as OpenAI-compatible SSE: one chunk per word, a final
// chunk carrying finish_reason and usage, then [DONE]. failAfter > 0 ends the
// stream with an error frame after that many chunks instead, which is how an
// upstream reports a failure once its 200 headers are already out.
func (m *mock) streamChat(w http.ResponseWriter, r *http.Request, model string, promptTokens, failAfter int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	id := "chatcmpl-mock-" + randHex()
	created := time.Now().Unix()
	frame := func(v any) bool {
		data, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	chunk := func(delta map[string]any, finish any, extra map[string]any) map[string]any {
		out := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		maps.Copy(out, extra)
		return out
	}
	errorFrame := map[string]any{"error": map[string]any{"type": "mock_error", "message": "mock upstream failed mid-stream"}}

	words := m.words()
	for i, word := range words {
		if failAfter > 0 && i == failAfter {
			frame(errorFrame)
			return
		}
		delta := map[string]any{"content": word}
		if i == 0 {
			delta["role"] = "assistant"
		}
		if !frame(chunk(delta, nil, nil)) || r.Context().Err() != nil {
			return
		}
	}
	if failAfter > 0 {
		frame(errorFrame)
		return
	}
	if !frame(chunk(map[string]any{}, "stop", map[string]any{"usage": usage(promptTokens, len(words))})) {
		return
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (m *mock) embedding(w http.ResponseWriter, r *http.Request) {
	var req embeddingRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	model := orDefault(req.Model, "text-embedding-3-small")

	d := m.decide(model, false)
	if !m.admit(w, r, d) {
		return
	}
	vec := make([]float64, 8)
	promptTokens := 8
	if d.demo {
		for i := range vec {
			vec[i] = rand.Float64()*2 - 1 //nolint:gosec // demo data, not security-sensitive
		}
		promptTokens += rand.Intn(60) //nolint:gosec // demo data, not security-sensitive
	} else {
		for i := range vec {
			vec[i] = float64(i) / 8
		}
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"model":  model,
		"data":   []any{map[string]any{"object": "embedding", "index": 0, "embedding": vec}},
		"usage":  map[string]any{"prompt_tokens": promptTokens, "total_tokens": promptTokens},
	})
}

// words is the completion every instance gives, split the way it is streamed.
func (m *mock) words() []string { return []string{"ok", " from", " " + m.cfg.name} }

func (m *mock) content() string { return strings.Join(m.words(), "") }

// promptTokens is varied in demo mode so token panels move, and a function of
// the prompt otherwise so two identical requests meter identically.
func (m *mock) promptTokens(req chatRequest, demo bool) int {
	if demo {
		return 8 + rand.Intn(60) //nolint:gosec // demo data, not security-sensitive
	}
	n := 0
	for _, msg := range req.Messages {
		n += len(msg.Content)
	}
	return 1 + n/4
}

func usage(prompt, completion int) map[string]any {
	return map[string]any{
		"prompt_tokens":     prompt,
		"completion_tokens": completion,
		"total_tokens":      prompt + completion,
	}
}

// sleepRandom models upstream latency in demo mode: mostly fast with a long
// tail, so the latency-percentile panels have a spread to show.
func (s settings) sleepRandom() {
	span := max(s.latencyMaxMS-s.latencyMinMS, 1)
	base := s.latencyMinMS + rand.Intn(span) //nolint:gosec // demo data, not security-sensitive
	if rand.Intn(100) < 12 {                 //nolint:gosec // demo data, not security-sensitive
		base += 200 + rand.Intn(600) //nolint:gosec // occasional slow request for the p99 tail; demo data
	}
	time.Sleep(time.Duration(base) * time.Millisecond)
}

// maybeError returns a status to fail with, on a small fraction of demo-mode
// requests: a 5xx (which counts toward the circuit breaker) more often than a
// 429 (which does not), so the reliability panels distinguish the two.
func (s settings) maybeError() (int, bool) {
	switch n := rand.Intn(100); { //nolint:gosec // demo data, not security-sensitive
	case n < s.errorPct:
		return http.StatusInternalServerError, true
	case n < s.errorPct+s.rateLimitPct:
		return http.StatusTooManyRequests, true
	default:
		return 0, false
	}
}

func writeError(w http.ResponseWriter, code, retryAfterS int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfterS > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterS))
	}
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
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}
