package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamToolCallDeltaOmitsEmptyMetadata(t *testing.T) {
	index := 0
	chunk := StreamChunk{
		Choices: []StreamChoice{{
			Delta: MessageDelta{
				ToolCalls: []ToolCall{{
					Index: &index,
					Function: FunctionCall{
						Arguments: `{"city"`,
					},
				}},
			},
		}},
	}

	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("marshal stream chunk: %v", err)
	}
	var decoded struct {
		Choices []struct {
			Delta struct {
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal stream chunk: %v", err)
	}
	if len(decoded.Choices) != 1 || len(decoded.Choices[0].Delta.ToolCalls) != 1 {
		t.Fatalf("tool call delta missing from %s", raw)
	}
	body := string(decoded.Choices[0].Delta.ToolCalls[0])
	for _, emptyField := range []string{`"id":""`, `"type":""`, `"name":""`} {
		if strings.Contains(body, emptyField) {
			t.Fatalf("stream tool-call delta leaked empty metadata field %s in %s", emptyField, body)
		}
	}
	if !strings.Contains(body, `"arguments":"{\"city\""`) {
		t.Fatalf("stream tool-call delta dropped arguments: %s", body)
	}
}
