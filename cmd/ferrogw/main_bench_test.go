package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ferro-labs/ai-gateway/internal/apierror"
	"github.com/ferro-labs/ai-gateway/internal/handler"
	"github.com/ferro-labs/ai-gateway/internal/sse"
	"github.com/ferro-labs/ai-gateway/providers"
)

func BenchmarkWriteSSE(b *testing.B) {
	chunk := providers.StreamChunk{
		ID:    "stream-1",
		Model: "test-stream-model",
		Choices: []providers.StreamChoice{{
			Index: 0,
			Delta: providers.MessageDelta{Role: "assistant", Content: "hello"},
		}},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ch := make(chan providers.StreamChunk, 1)
		ch <- chunk
		close(ch)

		w := httptest.NewRecorder()
		sse.Write(context.Background(), w, ch, nil)
	}
}

func BenchmarkDecodeChatCompletionRequest(b *testing.B) {
	payload := []byte(`{
		"model":"test-model",
		"messages":[
			{"role":"system","content":"You are a helpful assistant."},
			{"role":"user","content":"Summarize the latest deploy and highlight errors."},
			{"role":"user","content":[
				{"type":"text","text":"Also mention latency regressions."}
			]}
		],
		"temperature":0.2,
		"max_tokens":512,
		"stream":false,
		"tools":[
			{
				"type":"function",
				"function":{
					"name":"get_release_status",
					"description":"Fetch release status",
					"parameters":{
						"type":"object",
						"properties":{"environment":{"type":"string"}},
						"required":["environment"]
					}
				}
			}
		],
		"metadata":{"tenant":"bench","request_id":"req-123"}
	}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := handler.DecodeChatCompletionRequest(bytes.NewReader(payload)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEncodeChatCompletionResponse(b *testing.B) {
	resp := providers.Response{
		ID:       "chatcmpl-bench",
		Object:   "chat.completion",
		Created:  1710000000,
		Model:    "test-model",
		Provider: "test",
		Choices: []providers.Choice{{
			Index: 0,
			Message: providers.Message{
				Role:    "assistant",
				Content: "Deployment completed successfully. One provider showed elevated p95 latency, but error rate remained stable.",
			},
			FinishReason: "stop",
		}},
		Usage: providers.Usage{
			PromptTokens:     128,
			CompletionTokens: 32,
			TotalTokens:      160,
		},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := bytes.NewBuffer(make([]byte, 0, 512))
		if err := json.NewEncoder(buf).Encode(resp); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteOpenAIError(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		apierror.WriteOpenAI(w, http.StatusBadRequest, "invalid request", "invalid_request_error", "invalid_request")
		_, _ = io.Copy(io.Discard, w.Result().Body)
	}
}
