package streamwrap

import (
	"context"
	"testing"
	"time"

	"github.com/ferro-labs/ai-gateway/models"
	"github.com/ferro-labs/ai-gateway/providers"
)

// TestMeter_SuppressUsageForClient_AccountingSeesRealUsage is the bypass
// regression guard for the A11/B1 fix: a client that sends
// stream_options.include_usage=false must still be metered on real usage.
// Before this fix existed, the only way to honor the client's false was to
// stop requesting usage upstream entirely, which zeroed accounting and let a
// budget plugin's soft cap never trip (unlimited spend on demand). This test
// asserts the opposite of that failure mode directly: the after-request
// plugin stage (CompletionFn) sees the REAL, non-zero usage, while the
// client-facing stream carries no usage chunk at all.
func TestMeter_SuppressUsageForClient_AccountingSeesRealUsage(t *testing.T) {
	const promptTokens, completionTokens, totalTokens = 123, 45, 168

	src := feed(
		providers.StreamChunk{ID: "1", Choices: []providers.StreamChoice{{
			Delta: providers.MessageDelta{Content: "hello"},
		}}},
		providers.StreamChunk{
			ID:    "2",
			Usage: &providers.Usage{PromptTokens: promptTokens, CompletionTokens: completionTokens, TotalTokens: totalTokens},
		},
	)

	var pluginSawUsage providers.Usage
	var completionFnCalled bool
	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:               "openai",
		Model:                  "gpt-4o",
		MetricModel:            "gpt-4o",
		Catalog:                models.Catalog{},
		SuppressUsageForClient: true, // client sent include_usage:false
		CompletionFn: func(_ context.Context, resp *providers.Response) error {
			completionFnCalled = true
			pluginSawUsage = resp.Usage
			return nil
		},
	})

	var forwarded []providers.StreamChunk
	for c := range out {
		forwarded = append(forwarded, c)
	}

	if !completionFnCalled {
		t.Fatal("CompletionFn was never called")
	}
	if pluginSawUsage.TotalTokens != totalTokens || pluginSawUsage.PromptTokens != promptTokens || pluginSawUsage.CompletionTokens != completionTokens {
		t.Fatalf("plugin stage saw usage %+v, want real usage {Prompt:%d Completion:%d Total:%d} — a budget plugin reading zero usage here is exactly the spend-cap bypass this test guards against",
			pluginSawUsage, promptTokens, completionTokens, totalTokens)
	}

	if len(forwarded) != 2 {
		t.Fatalf("forwarded %d chunks, want 2", len(forwarded))
	}
	for _, c := range forwarded {
		if c.Usage != nil {
			t.Fatalf("client-facing chunk %+v carries usage, want it stripped (client asked for include_usage:false)", c)
		}
	}
}

// TestMeter_SuppressUsageForClient_PreservesContentOnUsageChunk verifies the
// design note "prefer clearing Usage over dropping the chunk": a chunk that
// carries both content/finish_reason AND usage must still deliver the
// content to the client — only the Usage field is cleared, not the whole
// chunk.
func TestMeter_SuppressUsageForClient_PreservesContentOnUsageChunk(t *testing.T) {
	src := feed(providers.StreamChunk{
		ID: "1",
		Choices: []providers.StreamChoice{{
			Delta:        providers.MessageDelta{Content: "final words"},
			FinishReason: "stop",
		}},
		Usage: &providers.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	})

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:               "openai",
		Model:                  "gpt-4o",
		MetricModel:            "gpt-4o",
		Catalog:                models.Catalog{},
		SuppressUsageForClient: true,
	})

	var forwarded []providers.StreamChunk
	for c := range out {
		forwarded = append(forwarded, c)
	}

	if len(forwarded) != 1 {
		t.Fatalf("forwarded %d chunks, want 1", len(forwarded))
	}
	got := forwarded[0]
	if got.Usage != nil {
		t.Errorf("Usage = %+v, want nil (stripped)", got.Usage)
	}
	if len(got.Choices) != 1 || got.Choices[0].Delta.Content != "final words" {
		t.Fatalf("content was dropped along with usage: %+v, want content preserved", got)
	}
	if got.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", got.Choices[0].FinishReason, "stop")
	}
}

// TestMeter_SuppressUsageForClient_ZeroValueKeepsUsage locks in the "zero
// /v1 breaking changes" requirement: MeterMeta.SuppressUsageForClient
// defaults to false, so every existing caller that does not set it
// (including every caller that predates this field) keeps delivering the
// usage chunk to the client exactly as before.
func TestMeter_SuppressUsageForClient_ZeroValueKeepsUsage(t *testing.T) {
	src := feed(providers.StreamChunk{
		ID:    "1",
		Usage: &providers.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4},
	})

	out := Meter(context.Background(), src, time.Now(), MeterMeta{
		Provider:    "openai",
		Model:       "gpt-4o",
		MetricModel: "gpt-4o",
		Catalog:     models.Catalog{},
		// SuppressUsageForClient intentionally left unset.
	})

	var forwarded []providers.StreamChunk
	for c := range out {
		forwarded = append(forwarded, c)
	}

	if len(forwarded) != 1 || forwarded[0].Usage == nil {
		t.Fatalf("forwarded %+v, want the usage chunk delivered unchanged (default behaviour)", forwarded)
	}
	if forwarded[0].Usage.TotalTokens != 4 {
		t.Errorf("TotalTokens = %d, want 4", forwarded[0].Usage.TotalTokens)
	}
}
