package anthropicwire

import (
	"reflect"
	"testing"

	"github.com/ferro-labs/ai-gateway/providers/core"
)

// blockContent renders a non-system turn as a []Block (matching the contract
// BuildMessages relies on for tool_result merging: content MUST return []Block,
// not another slice type, when it emits blocks).
func blockContent(m core.Message) any {
	return []Block{{Type: "text", Text: m.Content}}
}

// stringContent renders a non-system turn as a plain string, exercising the
// BuildMessages fallback where the preceding user turn's content is not []Block.
func stringContent(m core.Message) any {
	return m.Content
}

func TestBuildMessages(t *testing.T) {
	tests := []struct {
		name       string
		req        core.Request
		content    func(core.Message) any
		want       []Message
		wantSystem string
	}{
		{
			name: "system turns are concatenated with newline and dropped from messages",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleSystem, Content: "sys-a"},
				{Role: core.RoleSystem, Content: "sys-b"},
				{Role: core.RoleUser, Content: "hi"},
			}},
			content:    blockContent,
			want:       []Message{{Role: core.RoleUser, Content: []Block{{Type: "text", Text: "hi"}}}},
			wantSystem: "sys-a\nsys-b",
		},
		{
			name: "single system turn yields system prompt with no separator",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleSystem, Content: "only"},
				{Role: core.RoleUser, Content: "hi"},
			}},
			content:    blockContent,
			want:       []Message{{Role: core.RoleUser, Content: []Block{{Type: "text", Text: "hi"}}}},
			wantSystem: "only",
		},
		{
			name: "no system turn yields empty system prompt",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleUser, Content: "hi"},
			}},
			content:    blockContent,
			want:       []Message{{Role: core.RoleUser, Content: []Block{{Type: "text", Text: "hi"}}}},
			wantSystem: "",
		},
		{
			name: "tool_result merges into preceding user []Block turn",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleUser, Content: "u1"},
				{Role: core.RoleTool, ToolCallID: "t1", Content: "r1"},
			}},
			content: blockContent,
			want: []Message{{
				Role: core.RoleUser,
				Content: []Block{
					{Type: "text", Text: "u1"},
					{Type: "tool_result", ToolUseID: "t1", Content: "r1"},
				},
			}},
			wantSystem: "",
		},
		{
			name: "parallel tool_results merge into the same preceding user turn",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleUser, Content: "u1"},
				{Role: core.RoleTool, ToolCallID: "t1", Content: "r1"},
				{Role: core.RoleTool, ToolCallID: "t2", Content: "r2"},
			}},
			content: blockContent,
			want: []Message{{
				Role: core.RoleUser,
				Content: []Block{
					{Type: "text", Text: "u1"},
					{Type: "tool_result", ToolUseID: "t1", Content: "r1"},
					{Type: "tool_result", ToolUseID: "t2", Content: "r2"},
				},
			}},
			wantSystem: "",
		},
		{
			name: "fallback when preceding user content is not []Block (string) adds a new user turn",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleUser, Content: "u1"},
				{Role: core.RoleTool, ToolCallID: "t1", Content: "r1"},
			}},
			content: stringContent,
			want: []Message{
				{Role: core.RoleUser, Content: "u1"},
				{Role: core.RoleUser, Content: []Block{{Type: "tool_result", ToolUseID: "t1", Content: "r1"}}},
			},
			wantSystem: "",
		},
		{
			name: "fallback when tool_result has no preceding message adds a new user turn",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleTool, ToolCallID: "t1", Content: "r1"},
			}},
			content: blockContent,
			want: []Message{
				{Role: core.RoleUser, Content: []Block{{Type: "tool_result", ToolUseID: "t1", Content: "r1"}}},
			},
			wantSystem: "",
		},
		{
			name: "fallback when preceding turn is not a user turn adds a new user turn",
			req: core.Request{Messages: []core.Message{
				{Role: core.RoleAssistant, Content: "a1"},
				{Role: core.RoleTool, ToolCallID: "t1", Content: "r1"},
			}},
			content: blockContent,
			want: []Message{
				{Role: core.RoleAssistant, Content: []Block{{Type: "text", Text: "a1"}}},
				{Role: core.RoleUser, Content: []Block{{Type: "tool_result", ToolUseID: "t1", Content: "r1"}}},
			},
			wantSystem: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			gotMessages, gotSystem := BuildMessages(tt.req, tt.content)

			// Assert
			if gotSystem != tt.wantSystem {
				t.Errorf("system = %q, want %q", gotSystem, tt.wantSystem)
			}
			if !reflect.DeepEqual(gotMessages, tt.want) {
				t.Errorf("messages = %#v, want %#v", gotMessages, tt.want)
			}
		})
	}
}

func TestMapToolChoice(t *testing.T) {
	tools := []core.Tool{{Type: "function", Function: core.Function{Name: "get_weather"}}}

	tests := []struct {
		name   string
		choice any
		tools  []core.Tool
		want   any
	}{
		{
			name:   "no tools returns nil even when a choice is set",
			choice: "auto",
			tools:  nil,
			want:   nil,
		},
		{
			name:   "auto maps to type auto",
			choice: "auto",
			tools:  tools,
			want:   map[string]string{"type": "auto"},
		},
		{
			name:   "none maps to type none",
			choice: "none",
			tools:  tools,
			want:   map[string]string{"type": "none"},
		},
		{
			name:   "required maps to type any",
			choice: "required",
			tools:  tools,
			want:   map[string]string{"type": "any"},
		},
		{
			name: "function object maps to type tool with name",
			choice: map[string]any{
				"type":     "function",
				"function": map[string]any{"name": "get_weather"},
			},
			tools: tools,
			want:  map[string]string{"type": "tool", "name": "get_weather"},
		},
		{
			name:   "nil choice normalizes to unset and returns nil",
			choice: nil,
			tools:  tools,
			want:   nil,
		},
		{
			name:   "unrecognized choice normalizes to unset and returns nil",
			choice: "banana",
			tools:  tools,
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := MapToolChoice(tt.choice, tt.tools)

			// Assert
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MapToolChoice(%#v, tools=%d) = %#v, want %#v",
					tt.choice, len(tt.tools), got, tt.want)
			}
		})
	}
}
