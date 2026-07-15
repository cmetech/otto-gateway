// Whitebox unit tests for permission-related functions in translate.go (package acp).
package acp

import (
	"encoding/json"
	"testing"
)

// TestPickRejectOption validates the pickRejectOption function behavior per Track 3a spec.
// The function should:
// 1. Return the OptionID of the first option matching "reject" AND "always" (case-insensitive).
// 2. Fall back to the first option matching "reject" (case-insensitive).
// 3. Return "reject_always" as the final fallback.
func TestPickRejectOption(t *testing.T) {
	t.Parallel()
	rows := []struct {
		name    string
		options []permissionOption
		want    string
	}{
		{
			name: "both reject and reject_always, returns reject_always",
			options: []permissionOption{
				{OptionID: "reject_once", Kind: "reject"},
				{OptionID: "reject_always", Kind: "reject_always"},
			},
			want: "reject_always",
		},
		{
			name: "allow and reject, returns reject",
			options: []permissionOption{
				{OptionID: "allow_once", Kind: "allow"},
				{OptionID: "deny", Kind: "reject"},
			},
			want: "deny",
		},
		{
			name:    "empty slice returns reject_always",
			options: []permissionOption{},
			want:    "reject_always",
		},
		{
			name: "only allow options returns reject_always",
			options: []permissionOption{
				{OptionID: "allow_always", Kind: "allow"},
			},
			want: "reject_always",
		},
		{
			name: "reject_always with uppercase",
			options: []permissionOption{
				{OptionID: "reject_always", Kind: "REJECT_ALWAYS"},
			},
			want: "reject_always",
		},
		{
			name: "multiple rejects, first reject_always wins",
			options: []permissionOption{
				{OptionID: "reject_once", Kind: "reject"},
				{OptionID: "reject_always", Kind: "reject_always"},
				{OptionID: "reject_another", Kind: "reject"},
			},
			want: "reject_always",
		},
	}
	for _, r := range rows {
		r := r
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			got := pickRejectOption(r.options)
			if got != r.want {
				t.Errorf("pickRejectOption(%v): got %q, want %q", r.options, got, r.want)
			}
		})
	}
}

// TestPermissionParams_Unmarshal validates that permissionParams correctly deserializes
// a session/request_permission frame body containing requestId, options, and toolCall.
func TestPermissionParams_Unmarshal(t *testing.T) {
	t.Parallel()
	jsonData := []byte(`{
		"requestId":"r1",
		"options":[
			{"optionId":"reject_always","kind":"reject_always"},
			{"optionId":"allow_once","kind":"allow"}
		],
		"toolCall":{"title":"Read file /etc/hosts"}
	}`)

	var p permissionParams
	err := json.Unmarshal(jsonData, &p)
	if err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if p.RequestID != "r1" {
		t.Errorf("RequestID: got %q, want %q", p.RequestID, "r1")
	}

	if len(p.Options) != 2 {
		t.Errorf("len(Options): got %d, want 2", len(p.Options))
	}

	if len(p.Options) >= 1 && p.Options[0].OptionID != "reject_always" {
		t.Errorf("Options[0].OptionID: got %q, want %q", p.Options[0].OptionID, "reject_always")
	}

	if len(p.Options) >= 2 && p.Options[1].OptionID != "allow_once" {
		t.Errorf("Options[1].OptionID: got %q, want %q", p.Options[1].OptionID, "allow_once")
	}

	if p.ToolCall.Title != "Read file /etc/hosts" {
		t.Errorf("ToolCall.Title: got %q, want %q", p.ToolCall.Title, "Read file /etc/hosts")
	}
}
