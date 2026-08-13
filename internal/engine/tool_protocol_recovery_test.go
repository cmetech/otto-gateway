package engine

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

type recoveryPromptScript struct {
	stream Stream
	err    error
}

type recoveryModelCall struct {
	sessionID string
	model     string
}

type recoveryPromptCall struct {
	sessionID string
	blocks    []canonical.Block
}

type recordingRecoveryACP struct {
	mu sync.Mutex

	sessionID   string
	setModelErr error
	beginErr    error
	prompts     []recoveryPromptScript

	newSessionCalls int
	setModelCalls   []recoveryModelCall
	promptCalls     []recoveryPromptCall
	cancelCalls     []string
	beginCalls      []string
	finishCalls     int
}

func (f *recordingRecoveryACP) NewSession(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.newSessionCalls++
	if f.sessionID == "" {
		return "recovery-sid", nil
	}
	return f.sessionID, nil
}

func (f *recordingRecoveryACP) SetModel(_ context.Context, sessionID, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setModelCalls = append(f.setModelCalls, recoveryModelCall{sessionID: sessionID, model: model})
	return f.setModelErr
}

func (f *recordingRecoveryACP) BeginPromptSequence(sessionID string) (func(), error) {
	f.mu.Lock()
	f.beginCalls = append(f.beginCalls, sessionID)
	err := f.beginErr
	if len(f.setModelCalls) == 0 && err == nil {
		err = errors.New("prompt sequence began before SetModel")
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() {
		f.mu.Lock()
		f.finishCalls++
		f.mu.Unlock()
	}, nil
}

func (f *recordingRecoveryACP) Prompt(_ context.Context, sessionID string, blocks []canonical.Block) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ownedBlocks := append([]canonical.Block(nil), blocks...)
	f.promptCalls = append(f.promptCalls, recoveryPromptCall{sessionID: sessionID, blocks: ownedBlocks})
	index := len(f.promptCalls) - 1
	if index > 0 && f.finishCalls != 0 {
		return nil, errors.New("prompt sequence finished before corrective prompt")
	}
	if index >= len(f.prompts) {
		return nil, errors.New("unexpected prompt")
	}
	return f.prompts[index].stream, f.prompts[index].err
}

func (f *recordingRecoveryACP) Cancel(sessionID string) {
	f.mu.Lock()
	f.cancelCalls = append(f.cancelCalls, sessionID)
	f.mu.Unlock()
}

type recoveryACPSnapshot struct {
	newSessionCalls int
	setModelCalls   []recoveryModelCall
	promptCalls     []recoveryPromptCall
	cancelCalls     []string
	beginCalls      []string
	finishCalls     int
}

func (f *recordingRecoveryACP) snapshot() recoveryACPSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return recoveryACPSnapshot{
		newSessionCalls: f.newSessionCalls,
		setModelCalls:   append([]recoveryModelCall(nil), f.setModelCalls...),
		promptCalls:     append([]recoveryPromptCall(nil), f.promptCalls...),
		cancelCalls:     append([]string(nil), f.cancelCalls...),
		beginCalls:      append([]string(nil), f.beginCalls...),
		finishCalls:     f.finishCalls,
	}
}

type recoveryEventRecorder struct {
	mu     sync.Mutex
	events []ToolProtocolEvent
}

func (r *recoveryEventRecorder) observe(event ToolProtocolEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *recoveryEventRecorder) snapshot() []ToolProtocolEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ToolProtocolEvent(nil), r.events...)
}

type blockingRecoveryStream struct {
	chunks chan canonical.Chunk
}

func newBlockingRecoveryStream() *blockingRecoveryStream {
	return &blockingRecoveryStream{chunks: make(chan canonical.Chunk)}
}

func (s *blockingRecoveryStream) Chunks() <-chan canonical.Chunk { return s.chunks }

func (s *blockingRecoveryStream) Result() (*canonical.FinalResult, error) {
	return nil, errors.New("blocking recovery stream Result called before terminal")
}

type cancelOnCaptureStream struct {
	cancel context.CancelFunc
	chunks chan canonical.Chunk
}

func newCancelOnCaptureStream(cancel context.CancelFunc) *cancelOnCaptureStream {
	return &cancelOnCaptureStream{cancel: cancel, chunks: make(chan canonical.Chunk)}
}

func (s *cancelOnCaptureStream) Chunks() <-chan canonical.Chunk {
	s.cancel()
	return s.chunks
}

func (s *cancelOnCaptureStream) Result() (*canonical.FinalResult, error) {
	return nil, errors.New("cancel-on-capture Result called after cancellation")
}

func recoveryRequest(model string, choice *canonical.ToolChoice) *canonical.ChatRequest {
	return &canonical.ChatRequest{
		Model: model,
		Messages: []canonical.Message{{
			Role: canonical.RoleUser,
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: "check the weather",
			}},
		}},
		Tools: []canonical.ToolSpec{
			{Name: "get_weather", Parameters: map[string]any{"type": "object"}},
			{Name: "get_calendar", Parameters: map[string]any{"type": "object"}},
		},
		ToolChoice: choice,
	}
}

func recoveryTextStream(text string, final *canonical.FinalResult) Stream {
	if final == nil {
		final = &canonical.FinalResult{SessionID: "recovery-sid", ChunkCount: 1, StopReason: canonical.StopEndTurn}
	}
	return closedPreflightStream([]canonical.Chunk{textPreflightChunk(text)}, final, nil)
}

func recoveryToolStream(name string, tail ...string) Stream {
	chunks := []canonical.Chunk{toolPreflightChunk("call-1", name, map[string]any{"city": "Boston"})}
	for _, text := range tail {
		chunks = append(chunks, textPreflightChunk(text))
	}
	return closedPreflightStream(chunks, &canonical.FinalResult{
		SessionID: "recovery-sid", ChunkCount: len(chunks), StopReason: canonical.StopEndTurn,
	}, nil)
}

func newRecoveryEngine(t *testing.T, acpClient *recordingRecoveryACP, events *recoveryEventRecorder, opts ...func(*Config)) *Engine {
	t.Helper()
	return newTestEngine(t, (*fakeACP)(nil), func(cfg *Config) {
		cfg.ACP = acpClient
		if events != nil {
			cfg.OnToolProtocolEvent = events.observe
		}
		for _, opt := range opts {
			opt(cfg)
		}
	})
}

func assertSelectedProtocolError(t *testing.T, err error, forbidden string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want selected-model tool-protocol failure")
	}
	var selected *canonical.SelectedModelError
	if !errors.As(err, &selected) {
		t.Fatalf("error type = %T, want *canonical.SelectedModelError: %v", err, err)
	}
	if selected.Code != canonical.CodeSelectedModelToolProtocolFailed {
		t.Fatalf("selected-model error code = %q, want %q", selected.Code, canonical.CodeSelectedModelToolProtocolFailed)
	}
	if forbidden != "" && strings.Contains(err.Error(), forbidden) {
		t.Fatalf("safe selected-model error exposed upstream detail %q: %q", forbidden, err.Error())
	}
}

func TestToolProtocolRecovery_ValidWrapperFinishesSequenceBeforeReplay(t *testing.T) {
	const wrapper = `{"tool_call":{"name":"get_weather","arguments":{"city":"Boston"}}}`
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{stream: recoveryTextStream(wrapper, nil)}}}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events)

	run, err := eng.Run(context.Background(), recoveryRequest("selected-model", nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	snapshot := acpClient.snapshot()
	if snapshot.finishCalls != 1 {
		t.Fatalf("sequence finish calls before replay = %d, want 1", snapshot.finishCalls)
	}
	got := collectPreflightChunks(t, run.Stream())
	if !reflect.DeepEqual(got, []canonical.Chunk{textPreflightChunk(wrapper)}) {
		t.Fatalf("replayed chunks = %#v, want valid wrapper only", got)
	}
	if _, err := run.Stream().Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}
	if len(snapshot.promptCalls) != 1 || len(snapshot.setModelCalls) != 1 || len(snapshot.beginCalls) != 1 {
		t.Fatalf("lifecycle calls = set:%v begin:%v prompt:%v", snapshot.setModelCalls, snapshot.beginCalls, snapshot.promptCalls)
	}
	wantEvent := ToolProtocolEvent{Model: "selected-model", Outcome: OutcomeFirstAttempt}
	if gotEvents := events.snapshot(); !reflect.DeepEqual(gotEvents, []ToolProtocolEvent{wantEvent}) {
		t.Fatalf("events = %#v, want %#v", gotEvents, []ToolProtocolEvent{wantEvent})
	}
}

func TestToolProtocolRecovery_ValidNativeCallPassesThroughLive(t *testing.T) {
	stream := recoveryToolStream("get_weather", "tail")
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{stream: stream}}}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events)

	run, err := eng.Run(context.Background(), recoveryRequest("selected-model", nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if run.Stream() == stream {
		t.Fatal("guarded native stream was not wrapped for prefix/live handoff")
	}
	if got := acpClient.snapshot().finishCalls; got != 0 {
		t.Fatalf("sequence finished before live stream terminal: %d", got)
	}
	wantChunks := []canonical.Chunk{
		toolPreflightChunk("call-1", "get_weather", map[string]any{"city": "Boston"}),
		textPreflightChunk("tail"),
	}
	if got := collectPreflightChunks(t, run.Stream()); !reflect.DeepEqual(got, wantChunks) {
		t.Fatalf("prefix/live chunks = %#v, want %#v", got, wantChunks)
	}
	if _, err := run.Stream().Result(); err != nil {
		t.Fatalf("Result: %v", err)
	}
	if stop := run.StopWatchdog(); stop != nil {
		stop()
	}
	if got := acpClient.snapshot().finishCalls; got != 1 {
		t.Fatalf("sequence finish calls after live terminal = %d, want 1", got)
	}
	if gotEvents := events.snapshot(); !reflect.DeepEqual(gotEvents, []ToolProtocolEvent{{Model: "selected-model", Outcome: OutcomeFirstAttempt}}) {
		t.Fatalf("events = %#v", gotEvents)
	}
}

func TestToolProtocolRecovery_CorrectsHighConfidenceFailuresOnce(t *testing.T) {
	const validWrapper = `{"tool_call":{"name":"get_weather","arguments":{"city":"Boston"}}}`
	tests := []struct {
		name        string
		choice      *canonical.ToolChoice
		first       Stream
		wantReason  ToolProtocolReason
		failedBytes string
	}{
		{
			name:        "BuiltInToolDenied",
			first:       recoveryTextStream("denied-first-attempt", &canonical.FinalResult{ToolDenials: 1, StopReason: canonical.StopEndTurn}),
			wantReason:  ReasonBuiltInToolDenied,
			failedBytes: "denied-first-attempt",
		},
		{
			name:        "capability_refusal",
			first:       recoveryTextStream("The tools you supplied are not available to me.", nil),
			wantReason:  ReasonCapabilityRefusal,
			failedBytes: "not available to me",
		},
		{
			name:        "malformed_wrapper",
			first:       recoveryTextStream(`{"tool_call":{"name":"not_offered","arguments":{}}}`, nil),
			wantReason:  ReasonMalformedWrapper,
			failedBytes: "not_offered",
		},
		{
			name:        "required_missing",
			choice:      &canonical.ToolChoice{Type: "required"},
			first:       recoveryTextStream("ordinary first answer", nil),
			wantReason:  ReasonRequiredMissing,
			failedBytes: "ordinary first answer",
		},
		{
			name:        "named_mismatch",
			choice:      &canonical.ToolChoice{Type: "tool", Name: "get_weather"},
			first:       recoveryTextStream(`{"tool_call":{"name":"get_calendar","arguments":{}}}`, nil),
			wantReason:  ReasonNamedMismatch,
			failedBytes: "get_calendar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			acpClient := &recordingRecoveryACP{
				sessionID: "same-recovery-sid",
				prompts: []recoveryPromptScript{
					{stream: tt.first},
					{stream: recoveryTextStream(validWrapper, nil)},
				},
			}
			events := &recoveryEventRecorder{}
			eng := newRecoveryEngine(t, acpClient, events)
			resp, err := eng.Collect(context.Background(), recoveryRequest("selected-model", tt.choice))
			if err != nil {
				t.Fatalf("Collect: %v", err)
			}
			if got := resp.Message.Content[0].Text; got != validWrapper || strings.Contains(got, tt.failedBytes) {
				t.Fatalf("response text = %q, want corrected wrapper only", got)
			}
			snapshot := acpClient.snapshot()
			if !reflect.DeepEqual(snapshot.setModelCalls, []recoveryModelCall{{sessionID: "same-recovery-sid", model: "selected-model"}}) {
				t.Fatalf("SetModel calls = %#v", snapshot.setModelCalls)
			}
			if len(snapshot.promptCalls) != 2 || snapshot.promptCalls[0].sessionID != "same-recovery-sid" || snapshot.promptCalls[1].sessionID != "same-recovery-sid" {
				t.Fatalf("Prompt calls = %#v, want two on same session", snapshot.promptCalls)
			}
			policy, ok := toolProtocolPolicyFor(recoveryRequest("selected-model", tt.choice))
			if !ok || !reflect.DeepEqual(snapshot.promptCalls[1].blocks, correctiveBlocks(policy)) {
				t.Fatalf("corrective blocks = %#v, want %#v", snapshot.promptCalls[1].blocks, correctiveBlocks(policy))
			}
			if len(snapshot.beginCalls) != 1 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 0 {
				t.Fatalf("sequence lifecycle = begin:%v finish:%d cancel:%v", snapshot.beginCalls, snapshot.finishCalls, snapshot.cancelCalls)
			}
			wantEvent := ToolProtocolEvent{
				Model: "selected-model", Reason: tt.wantReason, Outcome: OutcomeCorrected, CorrectiveAttempts: 1,
			}
			if got := events.snapshot(); !reflect.DeepEqual(got, []ToolProtocolEvent{wantEvent}) {
				t.Fatalf("events = %#v, want %#v", got, []ToolProtocolEvent{wantEvent})
			}
		})
	}
}

func TestToolProtocolRecovery_TwoFailuresReturnOneSafeRecommendation(t *testing.T) {
	const secondSensitive = "second-secret-output"
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{
		{stream: recoveryTextStream(`{"tool_call":{"name":"not_offered","arguments":{}}}`, nil)},
		{stream: recoveryTextStream(secondSensitive, &canonical.FinalResult{ToolDenials: 1})},
	}}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events)

	_, err := eng.Run(context.Background(), recoveryRequest("selected-model", nil))
	assertSelectedProtocolError(t, err, secondSensitive)
	snapshot := acpClient.snapshot()
	if len(snapshot.promptCalls) != 2 {
		t.Fatalf("Prompt calls = %d, want exactly one correction", len(snapshot.promptCalls))
	}
	if len(snapshot.setModelCalls) != 1 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 1 {
		t.Fatalf("cleanup lifecycle = set:%v finish:%d cancel:%v", snapshot.setModelCalls, snapshot.finishCalls, snapshot.cancelCalls)
	}
	wantEvent := ToolProtocolEvent{
		Model: "selected-model", Reason: ReasonMalformedWrapper, Outcome: OutcomeFailed,
		CorrectiveAttempts: 1, RecommendAuto: true,
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, []ToolProtocolEvent{wantEvent}) {
		t.Fatalf("events = %#v, want %#v", got, []ToolProtocolEvent{wantEvent})
	}
}

func TestToolProtocolRecovery_IneligibleTurnsPreserveOnePromptLivePath(t *testing.T) {
	tests := []struct {
		name string
		req  *canonical.ChatRequest
	}{
		{name: "auto", req: recoveryRequest("auto", nil)},
		{name: "no_tools", req: simpleUserReq("hello", "selected-model")},
		{name: "none", req: recoveryRequest("selected-model", &canonical.ToolChoice{Type: "none"})},
		{name: "tool_result_final_turn", req: func() *canonical.ChatRequest {
			req := recoveryRequest("selected-model", nil)
			req.Messages[0].Content = append(req.Messages[0].Content, canonical.ContentPart{
				Kind: canonical.ContentKindToolResult,
				ToolResult: &canonical.ToolResultPart{
					ToolUseID: "call-previous", Content: "completed",
				},
			})
			return req
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := recoveryTextStream("ordinary-live-response", nil)
			acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{stream: source}}}
			events := &recoveryEventRecorder{}
			eng := newRecoveryEngine(t, acpClient, events)
			run, err := eng.Run(context.Background(), tt.req)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if run.Stream() != source {
				t.Fatal("ineligible turn did not preserve original live stream identity")
			}
			if got := collectPreflightChunks(t, run.Stream()); !reflect.DeepEqual(got, []canonical.Chunk{textPreflightChunk("ordinary-live-response")}) {
				t.Fatalf("chunks = %#v", got)
			}
			if _, err := run.Stream().Result(); err != nil {
				t.Fatalf("Result: %v", err)
			}
			if stop := run.StopWatchdog(); stop != nil {
				stop()
			}
			snapshot := acpClient.snapshot()
			if len(snapshot.promptCalls) != 1 || len(snapshot.beginCalls) != 0 || snapshot.finishCalls != 0 {
				t.Fatalf("ineligible lifecycle = prompt:%v begin:%v finish:%d", snapshot.promptCalls, snapshot.beginCalls, snapshot.finishCalls)
			}
			if len(events.snapshot()) != 0 {
				t.Fatalf("ineligible turn emitted recovery events: %#v", events.snapshot())
			}
		})
	}
}

func TestToolProtocolRecovery_BufferCapBypassesWithoutCorrection(t *testing.T) {
	huge := strings.Repeat("x", maxToolProtocolPreflightBytes+1)
	source := closedPreflightStream([]canonical.Chunk{
		textPreflightChunk(huge),
		textPreflightChunk("tail"),
	}, &canonical.FinalResult{ChunkCount: 2, StopReason: canonical.StopEndTurn}, nil)
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{stream: source}}}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events)

	resp, err := eng.Collect(context.Background(), recoveryRequest("selected-model", nil))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != huge+"tail" {
		t.Fatalf("bypass response length/content = %d/%v, want exact prefix/live response", len(got), strings.HasSuffix(got, "tail"))
	}
	snapshot := acpClient.snapshot()
	if len(snapshot.promptCalls) != 1 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 0 {
		t.Fatalf("bypass lifecycle = prompts:%d finish:%d cancel:%v", len(snapshot.promptCalls), snapshot.finishCalls, snapshot.cancelCalls)
	}
	wantEvent := ToolProtocolEvent{Model: "selected-model", Outcome: OutcomeBufferBypass}
	if got := events.snapshot(); !reflect.DeepEqual(got, []ToolProtocolEvent{wantEvent}) {
		t.Fatalf("events = %#v, want %#v", got, []ToolProtocolEvent{wantEvent})
	}
}

func TestToolProtocolRecovery_CorrectiveBufferBypassKeepsFailedBytesHidden(t *testing.T) {
	huge := strings.Repeat("y", maxToolProtocolPreflightBytes+1)
	second := closedPreflightStream([]canonical.Chunk{
		textPreflightChunk(huge),
		textPreflightChunk("corrective-tail"),
	}, &canonical.FinalResult{ChunkCount: 2, StopReason: canonical.StopEndTurn}, nil)
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{
		{stream: recoveryTextStream(`{"tool_call":{"name":"not_offered","arguments":{}}}`, nil)},
		{stream: second},
	}}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events)

	resp, err := eng.Collect(context.Background(), recoveryRequest("selected-model", nil))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := resp.Message.Content[0].Text; got != huge+"corrective-tail" || strings.Contains(got, "not_offered") {
		t.Fatalf("corrective bypass exposed failed first attempt or lost live bytes")
	}
	snapshot := acpClient.snapshot()
	if len(snapshot.promptCalls) != 2 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 0 {
		t.Fatalf("corrective bypass lifecycle = prompts:%d finish:%d cancel:%v", len(snapshot.promptCalls), snapshot.finishCalls, snapshot.cancelCalls)
	}
	wantEvent := ToolProtocolEvent{
		Model: "selected-model", Reason: ReasonMalformedWrapper,
		Outcome: OutcomeBufferBypass, CorrectiveAttempts: 0,
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, []ToolProtocolEvent{wantEvent}) {
		t.Fatalf("events = %#v, want %#v", got, []ToolProtocolEvent{wantEvent})
	}
}

func TestToolProtocolRecovery_CorrectivePromptFailuresAreSafeAndCleanedUp(t *testing.T) {
	const sensitive = "retry-upstream-secret"
	tests := []struct {
		name        string
		second      recoveryPromptScript
		idleTimeout time.Duration
	}{
		{name: "prompt_error", second: recoveryPromptScript{err: errors.New(sensitive)}},
		{name: "idle_timeout", second: recoveryPromptScript{stream: newBlockingRecoveryStream()}, idleTimeout: 10 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pre := &fakePreHook{}
			post := &fakePostHook{}
			acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{
				{stream: recoveryTextStream(`{"tool_call":{"name":"not_offered","arguments":{}}}`, nil)},
				tt.second,
			}}
			events := &recoveryEventRecorder{}
			eng := newRecoveryEngine(t, acpClient, events, func(cfg *Config) {
				cfg.PreHooks = []PreHook{pre}
				cfg.PostHooks = []PostHook{post}
				cfg.StreamIdleTimeout = tt.idleTimeout
			})

			_, err := eng.Run(context.Background(), recoveryRequest("selected-model", nil))
			assertSelectedProtocolError(t, err, sensitive)
			snapshot := acpClient.snapshot()
			if len(snapshot.promptCalls) != 2 || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) != 1 {
				t.Fatalf("failure cleanup = prompts:%d finish:%d cancel:%v", len(snapshot.promptCalls), snapshot.finishCalls, snapshot.cancelCalls)
			}
			if pre.calls != 1 || post.calls != 1 {
				t.Fatalf("hook calls = pre:%d post:%d, want once each", pre.calls, post.calls)
			}
			wantEvent := ToolProtocolEvent{
				Model: "selected-model", Reason: ReasonMalformedWrapper, Outcome: OutcomeFailed,
				CorrectiveAttempts: 1, RecommendAuto: true,
			}
			if got := events.snapshot(); !reflect.DeepEqual(got, []ToolProtocolEvent{wantEvent}) {
				t.Fatalf("events = %#v, want %#v", got, []ToolProtocolEvent{wantEvent})
			}
		})
	}
}

func TestToolProtocolRecovery_ContextCancellationWinsDuringEitherAttempt(t *testing.T) {
	tests := []struct {
		name    string
		prompts func(context.CancelFunc) []recoveryPromptScript
		wantN   int
	}{
		{
			name: "first_attempt",
			prompts: func(cancel context.CancelFunc) []recoveryPromptScript {
				return []recoveryPromptScript{{stream: newCancelOnCaptureStream(cancel)}}
			},
			wantN: 1,
		},
		{
			name: "corrective_attempt",
			prompts: func(cancel context.CancelFunc) []recoveryPromptScript {
				return []recoveryPromptScript{
					{stream: recoveryTextStream(`{"tool_call":{"name":"not_offered","arguments":{}}}`, nil)},
					{stream: newCancelOnCaptureStream(cancel)},
				}
			},
			wantN: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pre := &fakePreHook{}
			post := &fakePostHook{}
			acpClient := &recordingRecoveryACP{prompts: tt.prompts(cancel)}
			events := &recoveryEventRecorder{}
			eng := newRecoveryEngine(t, acpClient, events, withPreHooks(pre), withPostHooks(post), func(cfg *Config) {
				// Cancellation intentionally races the engine watchdog. A
				// test-bound logger must not retain t after this subtest exits.
				cfg.Logger = slog.New(slog.NewTextHandler(discardWriter{}, nil))
			})

			_, err := eng.Run(ctx, recoveryRequest("selected-model", nil))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run error = %v, want context.Canceled", err)
			}
			var selected *canonical.SelectedModelError
			if errors.As(err, &selected) {
				t.Fatalf("cancellation was replaced by synthetic selected-model error: %v", err)
			}
			snapshot := acpClient.snapshot()
			if len(snapshot.promptCalls) != tt.wantN || snapshot.finishCalls != 1 || len(snapshot.cancelCalls) == 0 {
				t.Fatalf("cancellation cleanup = prompts:%d finish:%d cancel:%v", len(snapshot.promptCalls), snapshot.finishCalls, snapshot.cancelCalls)
			}
			if pre.calls != 1 || post.calls != 1 {
				t.Fatalf("hook calls = pre:%d post:%d, want once each", pre.calls, post.calls)
			}
			gotEvents := events.snapshot()
			if len(gotEvents) != 1 || gotEvents[0].Outcome != OutcomeFailed || gotEvents[0].RecommendAuto {
				t.Fatalf("cancellation event = %#v, want one non-recommendation failure", gotEvents)
			}
		})
	}
}

func TestToolProtocolRecovery_HooksModelCounterAndEventFireOnce(t *testing.T) {
	pre := &fakePreHook{}
	post := &fakePostHook{}
	var modelMu sync.Mutex
	var modelRequests []string
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{
		{stream: recoveryTextStream(`{"tool_call":{"name":"not_offered","arguments":{}}}`, nil)},
		{stream: recoveryTextStream(`{"tool_call":{"name":"get_weather","arguments":{}}}`, nil)},
	}}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events, func(cfg *Config) {
		cfg.PreHooks = []PreHook{pre}
		cfg.PostHooks = []PostHook{post}
		cfg.OnModelRequest = func(model string) {
			modelMu.Lock()
			modelRequests = append(modelRequests, model)
			modelMu.Unlock()
		}
	})

	if _, err := eng.Collect(context.Background(), recoveryRequest("selected-model", nil)); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	modelMu.Lock()
	gotModels := append([]string(nil), modelRequests...)
	modelMu.Unlock()
	if pre.calls != 1 || post.calls != 1 || !reflect.DeepEqual(gotModels, []string{"selected-model"}) || len(events.snapshot()) != 1 {
		t.Fatalf("once-only observers = pre:%d post:%d models:%v events:%#v", pre.calls, post.calls, gotModels, events.snapshot())
	}
}

func TestToolProtocolRecovery_NilObserverDoesNotPanic(t *testing.T) {
	acpClient := &recordingRecoveryACP{prompts: []recoveryPromptScript{{
		stream: recoveryTextStream(`{"tool_call":{"name":"get_weather","arguments":{}}}`, nil),
	}}}
	eng := newRecoveryEngine(t, acpClient, nil)
	if _, err := eng.Collect(context.Background(), recoveryRequest("selected-model", nil)); err != nil {
		t.Fatalf("Collect with nil OnToolProtocolEvent: %v", err)
	}
}

func TestToolProtocolRecovery_BeginSequenceFailureCleansUp(t *testing.T) {
	const sensitive = "sequence-start-secret"
	pre := &fakePreHook{}
	post := &fakePostHook{}
	acpClient := &recordingRecoveryACP{beginErr: errors.New(sensitive)}
	events := &recoveryEventRecorder{}
	eng := newRecoveryEngine(t, acpClient, events, withPreHooks(pre), withPostHooks(post))

	_, err := eng.Run(context.Background(), recoveryRequest("selected-model", nil))
	assertSelectedProtocolError(t, err, sensitive)
	snapshot := acpClient.snapshot()
	if len(snapshot.beginCalls) != 1 || len(snapshot.promptCalls) != 0 || snapshot.finishCalls != 0 || len(snapshot.cancelCalls) != 1 {
		t.Fatalf("begin failure lifecycle = begin:%v prompt:%v finish:%d cancel:%v", snapshot.beginCalls, snapshot.promptCalls, snapshot.finishCalls, snapshot.cancelCalls)
	}
	if pre.calls != 1 || post.calls != 1 {
		t.Fatalf("hook calls = pre:%d post:%d, want once each", pre.calls, post.calls)
	}
	if got := events.snapshot(); len(got) != 1 || got[0].Outcome != OutcomeFailed || got[0].CorrectiveAttempts != 0 || !got[0].RecommendAuto {
		t.Fatalf("begin failure events = %#v", got)
	}
}
