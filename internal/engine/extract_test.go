package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairControlChars(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "raw newline inside quoted string becomes escaped",
			input:    `{"key":"line1` + "\n" + `line2"}`,
			expected: `{"key":"line1` + "\\" + `nline2"}`,
		},
		{
			name:     "raw carriage return inside quoted string becomes escaped",
			input:    `{"key":"value` + "\r" + `"}`,
			expected: `{"key":"value` + "\\" + `r"}`,
		},
		{
			name:     "raw tab inside quoted string becomes escaped",
			input:    `{"key":"value` + "\t" + `end"}`,
			expected: `{"key":"value` + "\\" + `tend"}`,
		},
		{
			name:     "newline outside strings is untouched",
			input:    `{"key":"value"}` + "\n" + `{"other":1}`,
			expected: `{"key":"value"}` + "\n" + `{"other":1}`,
		},
		{
			name:     "already-escaped newline is untouched",
			input:    `{"key":"value\\nmore"}`,
			expected: `{"key":"value\\nmore"}`,
		},
		{
			name:     "multiple control chars",
			input:    `{"a":"x` + "\n" + `y` + "\r" + `z` + "\t" + `end"}`,
			expected: `{"a":"x` + "\\" + `ny` + "\\" + `rz` + "\\" + `tend"}`,
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "no control chars",
			input:    `{"key":"value"}`,
			expected: `{"key":"value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repairControlChars(tt.input)
			if got != tt.expected {
				t.Errorf("repairControlChars(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractToolCallObjects(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantLen  int
		validate func(t *testing.T, objs []map[string]any)
	}{
		{
			name:    "plain tool_call object",
			input:   `{"tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}}`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				if _, ok := objs[0]["tool_call"]; !ok {
					t.Error("tool_call key not found in parsed object")
				}
			},
		},
		{
			name:    "tool_call inside json fence",
			input:   "```json\n" + `{"tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}}` + "\n```",
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				if _, ok := objs[0]["tool_call"]; !ok {
					t.Error("tool_call key not found in parsed object")
				}
			},
		},
		{
			name:    "tool_call embedded in prose",
			input:   `Sure: {"tool_call":{"name":"x","arguments":{}}} done`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				if _, ok := objs[0]["tool_call"]; !ok {
					t.Error("tool_call key not found in parsed object")
				}
			},
		},
		{
			name:    "two tool_call objects in one text",
			input:   `{"tool_call":{"name":"x","arguments":{}}} and {"tool_call":{"name":"y","arguments":{}}}`,
			wantLen: 2,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 2 {
					t.Fatalf("expected 2 objects, got %d", len(objs))
				}
				for i, obj := range objs {
					if _, ok := obj["tool_call"]; !ok {
						t.Errorf("tool_call key not found in object %d", i)
					}
				}
			},
		},
		{
			name:    "truncated tool_call (missing closing brace)",
			input:   `{"tool_call":{"name":"x","arguments":{"city":"Paris"}}`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				if _, ok := objs[0]["tool_call"]; !ok {
					t.Error("tool_call key not found in parsed object")
				}
			},
		},
		{
			name:    "string value containing closing brace and raw newline",
			input:   `{"tool_call":{"name":"write","arguments":{"content":"line1` + "\n" + `line2 }"}}}`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				if _, ok := objs[0]["tool_call"]; !ok {
					t.Error("tool_call key not found in parsed object")
				}
				// Verify the content was parsed correctly (with escaped newline)
				toolCall, ok := objs[0]["tool_call"].(map[string]any)
				if !ok {
					t.Fatal("tool_call is not a map")
				}
				args, ok := toolCall["arguments"].(map[string]any)
				if !ok {
					t.Fatal("arguments is not a map")
				}
				content, ok := args["content"].(string)
				if !ok {
					t.Fatal("content is not a string")
				}
				if !strings.Contains(content, "line1") || !strings.Contains(content, "line2") {
					t.Errorf("content not properly parsed: %q", content)
				}
			},
		},
		{
			name:     "no tool_call in text",
			input:    `{"data":{"value":1}} and some other text`,
			wantLen:  0,
			validate: nil,
		},
		{
			name:     "empty string",
			input:    "",
			wantLen:  0,
			validate: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolCallObjects(tt.input)
			if len(got) != tt.wantLen {
				t.Errorf("extractToolCallObjects() returned %d objects, want %d", len(got), tt.wantLen)
			}
			if tt.validate != nil {
				tt.validate(t, got)
			}
		})
	}
}

// TestExtractToolCallObjectsValidJSON verifies parsed objects are valid JSON.
func TestExtractToolCallObjectsValidJSON(t *testing.T) {
	input := `{"tool_call":{"name":"test","arguments":{"key":"value"}}}`
	objs := extractToolCallObjects(input)
	if len(objs) != 1 {
		t.Fatalf("expected 1 object, got %d", len(objs))
	}

	// Verify it's valid by re-marshaling
	data, err := json.Marshal(objs[0])
	if err != nil {
		t.Errorf("failed to marshal extracted object: %v", err)
	}
	if len(data) == 0 {
		t.Error("marshaled data is empty")
	}
}
