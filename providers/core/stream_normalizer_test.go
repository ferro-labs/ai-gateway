package core

import "testing"

// chunkWithRole is a one-choice chunk, the shape every provider emits per token.
func chunkWithRole(id, model, role, content string) StreamChunk {
	return StreamChunk{
		ID:    id,
		Model: model,
		Choices: []StreamChoice{{
			Index: 0,
			Delta: MessageDelta{Role: role, Content: content},
		}},
	}
}

// OpenAI sends delta.role once, on the first chunk. Providers disagree: some
// repeat it on every chunk, some never send it. A client that applies each
// delta verbatim ends up with a message whose role was set many times or never,
// so the stream is normalised to the one shape the contract defines.
func TestStreamNormalizerEmitsRoleExactlyOnce(t *testing.T) {
	tests := map[string][]string{
		"provider repeats the role on every chunk": {RoleAssistant, RoleAssistant, RoleAssistant},
		"provider never sends a role":              {"", "", ""},
		"provider already sends it once":           {RoleAssistant, "", ""},
	}

	for name, roles := range tests {
		t.Run(name, func(t *testing.T) {
			var n StreamNormalizer
			var got []string
			for _, role := range roles {
				chunk := chunkWithRole("id-1", "model-1", role, "x")
				n.Normalize(&chunk)
				got = append(got, chunk.Choices[0].Delta.Role)
			}
			if got[0] != RoleAssistant {
				t.Errorf("first chunk role = %q, want %q", got[0], RoleAssistant)
			}
			for i, role := range got[1:] {
				if role != "" {
					t.Errorf("chunk %d role = %q, want empty; the role belongs on the first chunk only", i+1, role)
				}
			}
		})
	}
}

// n>1 produces one stream per choice index, and each is a message in its own
// right: every one needs its own opening role.
func TestStreamNormalizerTracksRolePerChoice(t *testing.T) {
	var n StreamNormalizer
	chunk := StreamChunk{
		ID: "id-1", Model: "model-1",
		Choices: []StreamChoice{
			{Index: 0, Delta: MessageDelta{Content: "a"}},
			{Index: 1, Delta: MessageDelta{Content: "b"}},
		},
	}
	n.Normalize(&chunk)
	for _, choice := range chunk.Choices {
		if choice.Delta.Role != RoleAssistant {
			t.Errorf("choice %d role = %q, want %q on its first chunk", choice.Index, choice.Delta.Role, RoleAssistant)
		}
	}

	next := StreamChunk{
		ID: "id-1", Model: "model-1",
		Choices: []StreamChoice{
			{Index: 0, Delta: MessageDelta{Content: "c"}},
			{Index: 1, Delta: MessageDelta{Content: "d"}},
		},
	}
	n.Normalize(&next)
	for _, choice := range next.Choices {
		if choice.Delta.Role != "" {
			t.Errorf("choice %d role = %q on its second chunk, want empty", choice.Index, choice.Delta.Role)
		}
	}
}

// A provider that reports the id and model on an opening event only leaves
// every later chunk unattributable. The last reported values are carried
// forward so a client can correlate the whole stream.
func TestStreamNormalizerCarriesIdentityForward(t *testing.T) {
	var n StreamNormalizer
	first := chunkWithRole("chatcmpl-9", "gpt-4o-mini", RoleAssistant, "hi")
	n.Normalize(&first)

	later := chunkWithRole("", "", "", " there")
	n.Normalize(&later)

	if later.ID != "chatcmpl-9" {
		t.Errorf("ID = %q, want %q carried forward from the opening chunk", later.ID, "chatcmpl-9")
	}
	if later.Model != "gpt-4o-mini" {
		t.Errorf("Model = %q, want %q carried forward from the opening chunk", later.Model, "gpt-4o-mini")
	}
}

// Carrying forward must not overwrite: a provider that reports a value on every
// chunk keeps reporting its own.
func TestStreamNormalizerKeepsReportedIdentity(t *testing.T) {
	var n StreamNormalizer
	first := chunkWithRole("id-1", "model-1", RoleAssistant, "a")
	n.Normalize(&first)

	second := chunkWithRole("id-2", "model-2", "", "b")
	n.Normalize(&second)

	if second.ID != "id-2" || second.Model != "model-2" {
		t.Errorf("id/model = %q/%q, want id-2/model-2; a reported value must win over the carried one",
			second.ID, second.Model)
	}
}

// A terminal usage-only chunk carries no choices. It still gets the stream's
// id and model, and must not be handed a role delta it has no choice to put on.
func TestStreamNormalizerHandlesChoicelessChunk(t *testing.T) {
	var n StreamNormalizer
	first := chunkWithRole("id-1", "model-1", RoleAssistant, "a")
	n.Normalize(&first)

	usageOnly := StreamChunk{Usage: &Usage{TotalTokens: 5}}
	n.Normalize(&usageOnly)

	if usageOnly.ID != "id-1" || usageOnly.Model != "model-1" {
		t.Errorf("id/model = %q/%q, want id-1/model-1", usageOnly.ID, usageOnly.Model)
	}
	if len(usageOnly.Choices) != 0 {
		t.Errorf("Choices = %+v, want none invented", usageOnly.Choices)
	}
}

// The normalizer reports what the stream reported. A provider that never sends
// an id gets none invented for it — a fabricated id would look authoritative
// and correlate to nothing upstream.
func TestStreamNormalizerInventsNoIdentity(t *testing.T) {
	var n StreamNormalizer
	chunk := chunkWithRole("", "", "", "a")
	n.Normalize(&chunk)

	if chunk.ID != "" || chunk.Model != "" {
		t.Errorf("id/model = %q/%q, want both empty", chunk.ID, chunk.Model)
	}
}
