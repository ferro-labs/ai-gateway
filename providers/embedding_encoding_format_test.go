package providers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers"
	"github.com/ferro-labs/ai-gateway/providers/core"
)

// TestEmbeddingEncodingFormatIsRefusedWith400 closes the class of defect that
// azure-openai and cohere each carried privately: an embeddings
// encoding_format this gateway cannot serve must be refused once, as a typed
// 400, by every provider that offers embeddings at all.
//
// Both halves matter. The STATUS is what makes the refusal correct: a bare
// error carries none, so internal/apierror classifies it 500 — blaming the
// gateway for a value the caller chose — and internal/strategies.shouldRetry
// reads a status-less error as a transport failure and retries it, spending the
// whole retry budget on a request whose outcome cannot change. Refusing BEFORE
// the call is what makes it cheap: azure-openai forwarded "base64" and then died
// decoding the reply into core.Embedding.Embedding, a []float64 that cannot hold
// a base64 string, after paying for the upstream call.
//
// It is driven from AllProviders() rather than a hand-listed table so a
// thirty-first provider inherits it without writing anything.
func TestEmbeddingEncodingFormatIsRefusedWith400(t *testing.T) {
	rec := newPathRecorder(t)
	ctx := context.Background()
	uncovered := encodingFormatUncovered()

	checked := 0
	for _, entry := range providers.AllProviders() {
		cfg, ok := probeConfig(entry, rec.URL+rootProbe)
		if !ok {
			continue
		}
		p, err := entry.Build(cfg)
		if err != nil {
			t.Fatalf("build %s: %v", entry.ID, err)
		}
		ep, embeds := p.(core.EmbeddingProvider)
		if !embeds {
			continue
		}
		// An allowlisted provider is probed too, and its entry is read against
		// what the probe finds. Skipping it outright is how an entry outlives the
		// gap it records: the provider gets fixed, the entry keeps excusing it,
		// and the next provider added under the same entry inherits the excuse.
		if reason, skip := uncovered[entry.ID]; skip {
			delete(uncovered, entry.ID)
			if problems := probeEncodingFormatRefusal(ctx, rec, entry.ID, ep); len(problems) == 0 {
				t.Errorf("uncovered entry %q (%s) is stale: the provider now refuses an unsupported encoding_format correctly; remove the entry", entry.ID, reason)
				continue
			}
			t.Logf("%s: not asserted — %s", entry.ID, reason)
			continue
		}

		t.Run(entry.ID, func(t *testing.T) {
			for _, problem := range probeEncodingFormatRefusal(ctx, rec, entry.ID, ep) {
				t.Error(problem)
			}
		})
		checked++
	}

	// A guard that silently exercised nothing is worse than no guard.
	if checked < 10 {
		t.Fatalf("asserted only %d embedding providers; probeConfig is no longer reaching them", checked)
	}
	// Read the allowlist from the other side too: an entry naming a provider the
	// probe no longer reaches excuses nothing and hides the next one added.
	for id, reason := range uncovered {
		t.Errorf("uncovered entry %q (%s) names no probe-reachable embedding provider; remove it", id, reason)
	}
}

// probeEncodingFormatRefusal asks one provider to embed with an unservable
// encoding_format and reports every way its answer falls short, empty when it
// refuses correctly. It returns problems rather than calling t so the same
// assertion can be read in both directions: as a requirement for a covered
// provider, and as a staleness check on an allowlisted one.
func probeEncodingFormatRefusal(ctx context.Context, rec *pathRecorder, id string, ep core.EmbeddingProvider) []string {
	rec.drain()
	_, err := ep.Embed(ctx, core.EmbeddingRequest{
		Model:          embedProbeModel(id),
		Input:          "hi",
		EncodingFormat: "base64",
	})
	if err == nil {
		return []string{fmt.Sprintf("%s accepted encoding_format=base64; core.Embedding.Embedding is []float64 and cannot carry it", id)}
	}
	var problems []string
	var statusErr *core.HTTPStatusError
	switch {
	case !errors.As(err, &statusErr):
		problems = append(problems, fmt.Sprintf("%s: errors.As found no *core.HTTPStatusError in %T: %v — a status-less error is classified 500 and retried as a transport failure", id, err, err))
	case statusErr.StatusCode != http.StatusBadRequest:
		problems = append(problems, fmt.Sprintf("%s: StatusCode = %d, want 400 — the caller sent an unsupported value, the gateway is not broken", id, statusErr.StatusCode))
	}
	// Call core.ValidateEmbeddingEncodingFormat, do not forward and hope.
	if paths := rec.drain(); len(paths) != 0 {
		problems = append(problems, fmt.Sprintf("%s issued %v before refusing; the value is unservable whatever the upstream answers", id, paths))
	}
	return problems
}

// encodingFormatUncovered names the embedding providers the guard above reaches
// but does not assert, with the reason. An entry is a recorded gap, not an
// exemption.
//
// bedrock and vertex-ai are absent because probeConfig cannot reach them at all
// — both are addressed through a signed vendor SDK rather than a configurable
// root, the same line test/conformance and TestBaseURLRootIsUsedVerbatim draw —
// and both do call the shared validator.
// It is empty on purpose: every probe-reachable embedding provider is asserted.
func encodingFormatUncovered() map[string]string {
	return map[string]string{}
}

// embedProbeModel supplies a model id for the providers that validate one before
// anything else. The id only has to survive that check — no request is expected
// to leave the process.
func embedProbeModel(providerID string) string {
	switch providerID {
	case "cohere":
		return "embed-english-v3.0"
	case "openai":
		return "text-embedding-3-small"
	default:
		return "probe-model"
	}
}
