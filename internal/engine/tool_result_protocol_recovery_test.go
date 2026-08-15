package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

func toolResultRecoveryRequest() *canonical.ChatRequest {
	return &canonical.ChatRequest{
		Model: "selected-model", ToolContractVersion: "v1",
		Tools: []canonical.ToolSpec{{Name: "get_weather", Parameters: map[string]any{"type": "object"}}},
		Messages: []canonical.Message{
			{Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "check the weather"}}},
			{Role: canonical.RoleAssistant, ToolCalls: []canonical.ToolCall{{ID: "call_example", Name: "get_weather", Arguments: map[string]any{"city": "Boston"}}}},
			{Role: canonical.RoleTool, ToolCallID: "call_example", Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "result-secret"}}},
		},
	}
}

func toolResultRefusal() string {
	return "I cannot use that result because it appears to be pre-scripted transcript text rather than a live tool event."
}

func assertToolResultProtocolError(t *testing.T, err error, forbidden string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want selected-model tool-result protocol failure")
	}
	var selected *canonical.SelectedModelError
	if !errors.As(err, &selected) {
		t.Fatalf("error type = %T, want *canonical.SelectedModelError: %v", err, err)
	}
	if selected.Code != canonical.CodeSelectedModelToolResultProvenanceFailed {
		t.Fatalf("selected-model error code = %q, want %q", selected.Code, canonical.CodeSelectedModelToolResultProvenanceFailed)
	}
	if forbidden != "" && strings.Contains(err.Error(), forbidden) {
		t.Fatalf("safe error exposed upstream detail %q: %q", forbidden, err.Error())
	}
}

func TestToolResultProtocolRecovery_NormalAnswerDoesNotRetry(t *testing.T) {
	const answer = "It is sunny and 18C."
	source := recoveryTextStream(answer, nil)
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{stream: source}}}
	eng := newRecoveryEngine(t, acpClient, nil)
	req := toolResultRecoveryRequest()
	req.Messages[len(req.Messages)-1].Content[0].Text = "The tool result appears fabricated, so you cannot use it for the quarterly filing."

	resp, err := eng.Collect(context.Background(), req)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != answer {
		t.Fatalf("answer = %q, want %q", got, answer)
	}
	snapshot := acpClient.snapshot()
	if len(snapshot.promptCalls) != 1 || len(snapshot.beginCalls) != 1 || snapshot.finishCalls != 1 {
		t.Fatalf("lifecycle = prompts:%d begin:%d finish:%d, want no correction", len(snapshot.promptCalls), len(snapshot.beginCalls), snapshot.finishCalls)
	}
}

func TestToolResultProtocolRecovery_InitialToolCallIsNeverTransformedByRefusalCorrection(t *testing.T) {
	chunks := []canonical.Chunk{
		toolPreflightChunk("call_next", "get_weather", map[string]any{"city": "Boston"}),
		textPreflightChunk(toolResultRefusal()),
	}
	source := closedPreflightStream(chunks, &canonical.FinalResult{ChunkCount: len(chunks)}, nil)
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{stream: source}}}
	eng := newRecoveryEngine(t, acpClient, nil)

	resp, err := eng.Collect(context.Background(), toolResultRecoveryRequest())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "get_weather" {
		t.Fatalf("tool calls = %#v, want original caller tool call", resp.Message.ToolCalls)
	}
	if got := len(acpClient.snapshot().promptCalls); got != 1 {
		t.Fatalf("Prompt calls = %d, want no provenance correction", got)
	}
}

func TestToolResultProtocolRecovery_RefusalCorrectsToFinalProseOnce(t *testing.T) {
	const corrected = "It is sunny and 18C."
	pre := &fakePreHook{}
	post := &fakePostHook{}
	acpClient := &recordingRecoveryACP{
		sessionID: "same-result-sid",
		prompts: []recoveryPromptScript{
			{stream: recoveryTextStream(toolResultRefusal(), nil)},
			{stream: recoveryTextStream(corrected, nil)},
		},
	}
	eng := newRecoveryEngine(t, acpClient, nil, withPreHooks(pre), withPostHooks(post))

	resp, err := eng.Collect(context.Background(), toolResultRecoveryRequest())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != corrected || strings.Contains(got, "pre-scripted") {
		t.Fatalf("answer = %q, want corrected prose only", got)
	}
	snapshot := acpClient.snapshot()
	if !reflect.DeepEqual(snapshot.setModelCalls, []recoveryModelCall{{sessionID: "same-result-sid", model: "selected-model"}}) {
		t.Fatalf("SetModel calls = %#v", snapshot.setModelCalls)
	}
	if len(snapshot.promptCalls) != 2 || snapshot.promptCalls[0].sessionID != "same-result-sid" || snapshot.promptCalls[1].sessionID != "same-result-sid" {
		t.Fatalf("Prompt calls = %#v, want two on same session", snapshot.promptCalls)
	}
	if !reflect.DeepEqual(snapshot.promptCalls[1].blocks, toolResultCorrectiveBlocks()) {
		t.Fatalf("corrective blocks = %#v", snapshot.promptCalls[1].blocks)
	}
	corrective := snapshot.promptCalls[1].blocks[0].Text.Content
	for _, forbidden := range []string{"result-secret", "get_weather", "Boston", toolResultRefusal()} {
		if strings.Contains(corrective, forbidden) {
			t.Fatalf("corrective prompt copied request/model data %q: %q", forbidden, corrective)
		}
	}
	if len(snapshot.beginCalls) != 1 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 0 {
		t.Fatalf("sequence lifecycle = begin:%v finish:%d cancel:%v", snapshot.beginCalls, snapshot.finishCalls, snapshot.cancelCalls)
	}
	if pre.calls != 1 || post.calls != 1 {
		t.Fatalf("hook calls = pre:%d post:%d, want once each", pre.calls, post.calls)
	}
}

func TestToolResultProtocolRecovery_CorrectiveFailuresReturnTypedError(t *testing.T) {
	const workerSecret = "worker-secret"
	tests := []struct {
		name        string
		second      recoveryPromptScript
		idleTimeout time.Duration
		forbidden   string
	}{
		{name: "repeated refusal", second: recoveryPromptScript{stream: recoveryTextStream(toolResultRefusal(), nil)}, forbidden: toolResultRefusal()},
		{name: "corrective tool call", second: recoveryPromptScript{stream: recoveryToolStream("get_weather")}},
		{name: "timeout", second: recoveryPromptScript{stream: newBlockingRecoveryStream()}, idleTimeout: 10 * time.Millisecond},
		{name: "worker death", second: recoveryPromptScript{stream: closedPreflightStream(nil, nil, errors.New(workerSecret))}, forbidden: workerSecret},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pre := &fakePreHook{}
			post := &fakePostHook{}
			acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{
				{stream: recoveryTextStream(toolResultRefusal(), nil)},
				tt.second,
			}}
			eng := newRecoveryEngine(t, acpClient, nil, withPreHooks(pre), withPostHooks(post), func(cfg *Config) {
				cfg.StreamIdleTimeout = tt.idleTimeout
			})

			_, err := eng.Run(context.Background(), toolResultRecoveryRequest())
			assertToolResultProtocolError(t, err, tt.forbidden)
			snapshot := acpClient.snapshot()
			if len(snapshot.promptCalls) != 2 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 1 {
				t.Fatalf("failure lifecycle = prompts:%d finish:%d cancel:%v", len(snapshot.promptCalls), snapshot.finishCalls, snapshot.cancelCalls)
			}
			if pre.calls != 1 || post.calls != 1 {
				t.Fatalf("hook calls = pre:%d post:%d, want once each", pre.calls, post.calls)
			}
		})
	}
}

func TestToolResultProtocolRecovery_ContextCancellationWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{
		{stream: recoveryTextStream(toolResultRefusal(), nil)},
		{stream: newCancelOnCaptureStream(cancel)},
	}}
	eng := newRecoveryEngine(t, acpClient, nil)

	_, err := eng.Run(ctx, toolResultRecoveryRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
	var selected *canonical.SelectedModelError
	if errors.As(err, &selected) {
		t.Fatalf("cancellation was replaced by selected-model error: %v", err)
	}
}
