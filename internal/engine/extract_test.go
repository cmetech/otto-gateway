package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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
			// Defect 2 (nested sibling): a closed nested object ({"x":1})
			// precedes the "tool_call" key inside the SAME enclosing object.
			// The scanner must associate the key with its TRUE enclosing
			// object, not the sibling that closed before it.
			name:    "tool_call after nested sibling object",
			input:   `prefix {"meta":{"x":1},"tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}} suffix`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				tc, ok := objs[0]["tool_call"].(map[string]any)
				if !ok {
					t.Fatal("tool_call is not a map")
				}
				if name, _ := tc["name"].(string); name != "get_weather" {
					t.Errorf("tool_call.name = %q, want get_weather", name)
				}
			},
		},
		{
			// Wrapper nested one level deep: "tool_call" is a DIRECT key of
			// the inner object. Intended behavior (matching the JS reference:
			// extract the object of which tool_call is a direct key) is to
			// return the minimal enclosing object, so the extracted map has
			// the tool_call key and NOT the "outer" key.
			name:    "tool_call nested one level deep",
			input:   `{"outer":{"tool_call":{"name":"get_weather","arguments":{}}}}`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				if _, ok := objs[0]["outer"]; ok {
					t.Error("extracted object has outer key; expected minimal enclosing object")
				}
				tc, ok := objs[0]["tool_call"].(map[string]any)
				if !ok {
					t.Fatal("tool_call is not a map")
				}
				if name, _ := tc["name"].(string); name != "get_weather" {
					t.Errorf("tool_call.name = %q, want get_weather", name)
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
			// Value-vs-key discrimination (nextNonSpaceIsColon): the
			// "note":"tool_call" VALUE equals tool_call but is NOT a key, so
			// it must not create a spurious/duplicate mark. Only the real
			// "tool_call": KEY counts → exactly 1 object, name get_weather.
			name:    "string value equal to tool_call is not the key",
			input:   `{"note":"tool_call","tool_call":{"name":"get_weather","arguments":{"city":"Paris"}}}`,
			wantLen: 1,
			validate: func(t *testing.T, objs []map[string]any) {
				if len(objs) != 1 {
					t.Fatalf("expected 1 object, got %d", len(objs))
				}
				tc, ok := objs[0]["tool_call"].(map[string]any)
				if !ok {
					t.Fatal("tool_call is not a map")
				}
				if name, _ := tc["name"].(string); name != "get_weather" {
					t.Errorf("tool_call.name = %q, want get_weather", name)
				}
			},
		},
		{
			// A tool_call substring inside a string VALUE, with no real
			// "tool_call": key anywhere → 0 objects.
			name:     "tool_call substring inside a value is not a key",
			input:    `{"x":"see the tool_call docs"}`,
			wantLen:  0,
			validate: nil,
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

// TestExtractToolCallObjects_UnrelatedPriorClosedObject_NoHang
// Regression test for infinite-loop bug: when an unrelated earlier
// object has already closed before the "tool_call" text, the LastIndex
// finds that earlier object's opening brace, scans to its closing brace
// (which is before the key position), and if not guarded by end >= idx,
// this false match causes idx to move backward indefinitely.
func TestExtractToolCallObjects_UnrelatedPriorClosedObject_NoHang(t *testing.T) {
	input := `{"status":"ok"} the "tool_call" field is missing`
	objs := extractToolCallObjects(input)
	// Should return 0 objects (no valid tool_call wrapper found).
	if len(objs) != 0 {
		t.Errorf("expected 0 objects for unrelated prior object, got %d", len(objs))
	}
	// Test passes if it completes without hanging.
}

// TestExtractToolCallObjects_WrapperAfterUnrelatedObject
// Regression test: verify that when a real tool_call wrapper exists
// after an unrelated closed object, it is correctly extracted (not
// confused with the prior object).
func TestExtractToolCallObjects_WrapperAfterUnrelatedObject(t *testing.T) {
	input := `{"status":"ok"} then {"tool_call":{"name":"x","arguments":{}}}`
	objs := extractToolCallObjects(input)
	if len(objs) != 1 {
		t.Errorf("expected 1 object, got %d", len(objs))
	}
	// Verify the extracted object is the tool_call one, not the status one.
	if len(objs) > 0 {
		if _, ok := objs[0]["tool_call"]; !ok {
			t.Error("extracted object does not have tool_call key; extracted wrong object")
		}
		if _, ok := objs[0]["status"]; ok {
			t.Error("extracted object has status key; extracted prior object instead of tool_call")
		}
	}
}

// TestExtractToolCallObjects_KeyNotInObject
// Regression test: bare "tool_call" text not in braces should not hang.
func TestExtractToolCallObjects_KeyNotInObject(t *testing.T) {
	input := `Some text mentioning "tool_call" with no braces nearby.`
	objs := extractToolCallObjects(input)
	if len(objs) != 0 {
		t.Errorf("expected 0 objects for bare text, got %d", len(objs))
	}
	// Test passes if it completes without hanging.
}

// TestExtractToolCallObjects_AdversarialSize_Bounded
// Defect 1 (DoS): the pre-fix backward-LastIndex + per-key rescan scanner
// was O(n^2) on repeated "tool_call" keys. Assistant text from kiro is
// unbounded (MaxBytesReader caps the request body, not kiro's output), so a
// ~5 MB input of 400k repeated `"tool_call" ` tokens forced hundreds of
// thousands of full-length rescans + repair allocations. The single forward
// pass plus the input-size budget must keep this bounded: it must RETURN
// quickly and yield 0 valid wrappers (none is a real {"tool_call":...}).
func TestExtractToolCallObjects_AdversarialSize_Bounded(t *testing.T) {
	input := "{" + strings.Repeat(`"tool_call" `, 400_000) // ~5 MB
	start := time.Now()
	objs := extractToolCallObjects(input)
	elapsed := time.Since(start)
	if len(objs) != 0 {
		t.Errorf("expected 0 objects for adversarial input, got %d", len(objs))
	}
	// Generous bound; the O(n) forward pass over a 1 MiB budget completes in
	// low-single-digit milliseconds. 5s would only trip on quadratic blowup.
	if elapsed > 5*time.Second {
		t.Fatalf("adversarial input took %v — expected bounded/near-instant", elapsed)
	}
}

// TestExtractToolCallObjects_DeeplyNested_Bounded
// Exercises the nesting budget: a runaway stream of `{` must not push an
// unbounded stack or hang. The scanner caps open-object depth and returns.
func TestExtractToolCallObjects_DeeplyNested_Bounded(t *testing.T) {
	input := strings.Repeat("{", 500_000)
	start := time.Now()
	objs := extractToolCallObjects(input) // must not panic/OOM/hang
	if len(objs) != 0 {
		t.Errorf("expected 0 objects for deeply-nested braces, got %d", len(objs))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("deeply-nested input took %v — expected bounded", elapsed)
	}
}
