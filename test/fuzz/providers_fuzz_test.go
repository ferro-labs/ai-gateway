// Package fuzz fuzzes the providers' most exposed untrusted surface: the bytes
// an upstream LLM API returns. A provider translates that native response into a
// core.Response, and a flaky upstream, a broken proxy, or a hostile MITM can
// return anything — so the decoder must, for ANY status and ANY body, return a
// value or an error and never panic, hang, or return (nil, nil).
//
// FuzzProviderResponse drives that through every provider's real Complete path
// (built via test/providerstub, answered in memory — no network). It complements
// the wire-level stream fuzzers already in providers/core.
//
//	go test -fuzz=FuzzProviderResponse -fuzztime=30s ./test/fuzz/
//	go test ./test/fuzz/            # replays the seed corpus only (CI-safe)
package fuzz

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
	"github.com/ferro-labs/ai-gateway/test/providerstub"
)

// fuzzExclude lists providers whose Complete is not a single-shot response
// decode. Replicate submits a prediction then POLLS until a terminal status, so
// a fixed stub body that never reaches one would poll to the deadline on every
// input — its decode path is not what this target exercises. (The fuzz DID
// surface that replicate.poll is bounded only by the caller's context; with no
// request deadline set it is unbounded. That is a routing/timeout concern, not a
// decode one, so it is out of scope here.)
func fuzzExclude() map[string]string {
	return map[string]string{
		providers.NameReplicate: "Complete polls until a terminal status; a fixed stub body never reaches one",
	}
}

func FuzzProviderResponse(f *testing.F) {
	ctx := context.Background()
	excluded := fuzzExclude()

	// Build every stubbable provider once; the fuzz body only swaps the response.
	type target struct {
		id    string
		model string
		p     core.Provider
	}
	ids := make([]string, 0, len(providerstub.Fixtures()))
	for id := range providerstub.Fixtures() {
		if _, skip := excluded[id]; skip {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic build/seed order

	targets := make([]target, 0, len(ids))
	for _, id := range ids {
		fx := providerstub.Fixtures()[id]
		targets = append(targets, target{id: id, model: fx.Model, p: providerstub.Build(f, id)})
		f.Add(uint16(200), providerstub.LoadChatResponse(f, id)) // each real payload is a seed
	}

	// Edge seeds — shapes a real upstream or a broken proxy can return, across the
	// openai/anthropic/gemini/cohere wire families.
	for _, s := range []string{
		``, `{}`, `[]`, `null`, `"a bare string"`, `123`, `true`,
		`{"choices":null}`, `{"choices":[]}`, `{"choices":[{}]}`,
		`{"choices":[{"message":null}]}`,
		`{"choices":[{"message":{"content":123}}]}`,
		`{"choices":[{"message":{"content":"x"},"finish_reason":123}]}`,
		`{"usage":{"prompt_tokens":-1,"total_tokens":99999999999999999999}}`,
		`{"content":[{"type":"text","text":null}]}`,                      // anthropic-ish
		`{"candidates":[{"content":{"parts":[]}}]}`,                      // gemini-ish
		`{"message":{"content":[{"type":"text"}]}}`,                      // cohere-ish
		`{"choices":[{"message":{"content":"` + "\x00\x01\x1f" + `"}}]}`, // unescaped control bytes
		`{"choices":[{"message":{"content":"` + `truncated`,              // truncated
	} {
		f.Add(uint16(200), []byte(s))
	}
	f.Add(uint16(400), []byte(`{"error":{"message":"bad request"}}`))
	f.Add(uint16(429), []byte(`{"error":{"message":"rate limited"}}`))
	f.Add(uint16(500), []byte(`{"error":{"message":"boom","type":"server_error"}}`))
	f.Add(uint16(503), []byte(`not json at all`))

	f.Fuzz(func(t *testing.T, statusSeed uint16, body []byte) {
		status := 200 + int(statusSeed%400) // map into a real HTTP range 200..599
		for _, tg := range targets {
			restore := providerstub.InstallResponse(tg.id, status, body)
			// A safety-net deadline: a single-shot decode finishes in microseconds,
			// so this is never hit in practice — it only stops a future blocking
			// provider from wedging the fuzzer instead of failing the input.
			callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			resp, err := tg.p.Complete(callCtx, providerstub.ChatRequest(tg.model))
			cancel()
			restore()

			// The decoder must resolve to exactly one of a value or an error.
			// Never a panic (the fuzzer catches that), and never (nil, nil),
			// which would hand a caller a success with nothing in it.
			if err == nil && resp == nil {
				t.Fatalf("%s: Complete returned (nil, nil) for status=%d body=%q", tg.id, status, body)
			}
		}
	})
}
