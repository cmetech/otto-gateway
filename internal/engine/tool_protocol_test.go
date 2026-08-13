package engine

import (
	"reflect"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
)

func TestToolProtocolPolicyFor_EligibilityAndRequirement(t *testing.T) {
	tools := []canonical.ToolSpec{{Name: "get_weather"}, {Name: "read_file"}}
	userTurn := canonical.Message{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "weather"}}}
	cases := []struct {
		name            string
		req             *canonical.ChatRequest
		wantEligible    bool
		wantRequirement toolProtocolRequirement
		wantNamedTool   string
	}{
		{"empty model", &canonical.ChatRequest{Tools: tools, Messages: []canonical.Message{userTurn}}, false, toolProtocolOptional, ""},
		{"auto model", &canonical.ChatRequest{Model: "auto", Tools: tools, Messages: []canonical.Message{userTurn}}, false, toolProtocolOptional, ""},
		{"no tools", &canonical.ChatRequest{Model: "selected", Messages: []canonical.Message{userTurn}}, false, toolProtocolOptional, ""},
		{"assistant final turn", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{{Role: canonical.RoleAssistant}}}, false, toolProtocolOptional, ""},
		{"role tool final turn", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{{Role: canonical.RoleTool}}}, false, toolProtocolOptional, ""},
		{"tool result content final turn", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindToolResult, ToolResult: &canonical.ToolResultPart{Content: "result"}}}}}}, false, toolProtocolOptional, ""},
		{"none", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "none"}}, false, toolProtocolOptional, ""},
		{"explicit optional", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}}, true, toolProtocolOptional, ""},
		{"nil choice", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: nil}, true, toolProtocolOptional, ""},
		{"auto choice", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "auto"}}, true, toolProtocolOptional, ""},
		{"unknown choice remains optional", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "future"}}, true, toolProtocolOptional, ""},
		{"required", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "required"}}, true, toolProtocolRequired, ""},
		{"anthropic any", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "any"}}, true, toolProtocolRequired, ""},
		{"validated named tool", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "tool", Name: "get_weather"}}, true, toolProtocolNamed, "get_weather"},
		{"unknown named tool remains optional", &canonical.ChatRequest{Model: "selected", Tools: tools, Messages: []canonical.Message{userTurn}, ToolChoice: &canonical.ToolChoice{Type: "tool", Name: "not_offered"}}, true, toolProtocolOptional, ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			policy, eligible := toolProtocolPolicyFor(tt.req)
			if eligible != tt.wantEligible {
				t.Fatalf("eligible = %v, want %v", eligible, tt.wantEligible)
			}
			if policy.requirement != tt.wantRequirement {
				t.Errorf("requirement = %q, want %q", policy.requirement, tt.wantRequirement)
			}
			if policy.namedTool != tt.wantNamedTool {
				t.Errorf("namedTool = %q, want %q", policy.namedTool, tt.wantNamedTool)
			}
		})
	}
}

func TestClassifyToolProtocolAttempt_ConservativeRecoveryMatrix(t *testing.T) {
	base := &canonical.ChatRequest{
		Model:    "selected",
		Tools:    []canonical.ToolSpec{{Name: "get_weather"}, {Name: "read_file"}},
		Messages: []canonical.Message{{Role: canonical.RoleUser}},
	}
	required := *base
	required.ToolChoice = &canonical.ToolChoice{Type: "required"}
	named := *base
	named.ToolChoice = &canonical.ToolChoice{Type: "tool", Name: "get_weather"}
	dispatcher := *base
	dispatcher.Tools = append(append([]canonical.ToolSpec(nil), base.Tools...), toolCallDispatcher())

	cases := []struct {
		name    string
		req     *canonical.ChatRequest
		obs     attemptObservation
		aliases map[string]string
		want    ToolProtocolReason
	}{
		{"offered native call", base, attemptObservation{NativeCall: true, ToolCalls: []canonical.ToolCall{{Name: "get_weather"}}}, nil, ""},
		{"aliased native call", base, attemptObservation{NativeCall: true, ToolCalls: []canonical.ToolCall{{Name: "execute"}}}, map[string]string{"execute": "get_weather"}, ""},
		{"direct wrapper", base, attemptObservation{Text: `{"tool_call":{"name":"get_weather","arguments":{"location":"Boston"}}}`}, nil, ""},
		{"deferred dispatcher wrapper", &dispatcher, attemptObservation{Text: `{"tool_call":{"name":"unregistered","arguments":{"location":"Boston"}}}`}, nil, ""},
		{
			"required unoffered native plus correct wrapper",
			&required,
			attemptObservation{
				NativeCall: true,
				ToolCalls:  []canonical.ToolCall{{Name: "execute"}},
				Text:       `{"tool_call":{"name":"get_weather","arguments":{"location":"Boston"}}}`,
			},
			nil,
			"",
		},
		{
			"named wrong native plus correct wrapper",
			&named,
			attemptObservation{
				NativeCall: true,
				ToolCalls:  []canonical.ToolCall{{Name: "read_file"}},
				Text:       `{"tool_call":{"name":"get_weather","arguments":{"location":"Boston"}}}`,
			},
			nil,
			"",
		},
		{"built in tool denied", base, attemptObservation{Final: &canonical.FinalResult{ToolDenials: 1}}, nil, ReasonBuiltInToolDenied},
		{"required missing", &required, attemptObservation{Text: "I will answer directly."}, nil, ReasonRequiredMissing},
		{"named mismatch", &named, attemptObservation{ToolCalls: []canonical.ToolCall{{Name: "read_file"}}}, nil, ReasonNamedMismatch},
		{"malformed whole response wrapper", base, attemptObservation{Text: `{"tool_call":{"name":"not_offered","arguments":{}}}`}, nil, ReasonMalformedWrapper},
		{"capability refusal", base, attemptObservation{Text: "The supplied connector tools are not actually available to me here."}, nil, ReasonCapabilityRefusal},
		{"gitlab permission denial", base, attemptObservation{Text: "GitLab returned permission denied for this project."}, nil, ""},
		{"safety refusal", base, attemptObservation{Text: "I can't help with that request."}, nil, ""},
		{"ordinary optional answer", base, attemptObservation{Text: "The weather is pleasant today."}, nil, ""},
		{"tool result error text", base, attemptObservation{Text: "Tool result: request failed with permission denied."}, nil, ""},
		{"buffer bypass", &required, attemptObservation{BufferBypass: true}, nil, ""},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			policy, eligible := toolProtocolPolicyFor(tt.req)
			if !eligible {
				t.Fatal("test setup produced an ineligible request")
			}
			if got := classifyToolProtocolAttempt(policy, tt.obs, tt.aliases); got != tt.want {
				t.Errorf("reason = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCorrectiveBlocks_StaticAndSafe(t *testing.T) {
	malicious := "user-secret arguments-secret schema-secret output-secret system-secret"
	base := &canonical.ChatRequest{
		Model:  "selected",
		System: malicious,
		Tools: []canonical.ToolSpec{{
			Name:        "get_weather",
			Description: malicious,
			Parameters:  map[string]any{"secret": malicious},
		}},
		Messages: []canonical.Message{{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: malicious}}}},
	}
	cases := []struct {
		name   string
		choice *canonical.ToolChoice
		want   []canonical.Block
	}{
		{
			name: "optional permits a normal answer",
			want: []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "Use an offered external tool by emitting a valid tool call when a tool is needed. A normal final answer is acceptable if no tool is needed."}}},
		},
		{
			name:   "required requires a call",
			choice: &canonical.ToolChoice{Type: "required"},
			want:   []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "Call one of the offered external tools by emitting a valid tool call. A normal final answer is not acceptable."}}},
		},
		{
			name:   "anthropic any requires a call",
			choice: &canonical.ToolChoice{Type: "any"},
			want:   []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "Call one of the offered external tools by emitting a valid tool call. A normal final answer is not acceptable."}}},
		},
		{
			name:   "validated named tool requires that call",
			choice: &canonical.ToolChoice{Type: "tool", Name: "get_weather"},
			want:   []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "Call the offered external tool `get_weather` by emitting a valid tool call. A normal final answer is not acceptable."}}},
		},
		{
			name:   "unvalidated named tool is never included",
			choice: &canonical.ToolChoice{Type: "tool", Name: malicious},
			want:   []canonical.Block{{Kind: canonical.BlockKindText, Text: &canonical.TextBlock{Content: "Use an offered external tool by emitting a valid tool call when a tool is needed. A normal final answer is acceptable if no tool is needed."}}},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := *base
			req.ToolChoice = tt.choice
			policy, eligible := toolProtocolPolicyFor(&req)
			if !eligible {
				t.Fatal("test setup produced an ineligible request")
			}
			got := correctiveBlocks(policy)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("correctiveBlocks() = %#v, want %#v", got, tt.want)
			}
			for _, block := range got {
				if block.Text != nil && strings.Contains(block.Text.Content, malicious) {
					t.Errorf("corrective prompt leaked request content: %q", block.Text.Content)
				}
			}
		})
	}
}
