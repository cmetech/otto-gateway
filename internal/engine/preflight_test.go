package engine

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

type preflightTestStream struct {
	chunks      <-chan canonical.Chunk
	final       *canonical.FinalResult
	err         error
	resultCalls atomic.Int32
	onResult    func()
}

type drainDependentPreflightStream struct {
	chunks       chan canonical.Chunk
	sourceClosed chan struct{}
	releaseClose chan struct{}
	abort        chan struct{}
	resultCalls  atomic.Int32
}

func newDrainDependentPreflightStream(chunkCount int) *drainDependentPreflightStream {
	s := &drainDependentPreflightStream{
		chunks:       make(chan canonical.Chunk),
		sourceClosed: make(chan struct{}),
		releaseClose: make(chan struct{}),
		abort:        make(chan struct{}),
	}
	go func() {
		defer close(s.sourceClosed)
		defer close(s.chunks)
		for i := 0; i < chunkCount; i++ {
			select {
			case s.chunks <- textPreflightChunk("live"):
			case <-s.abort:
				return
			}
		}
		select {
		case <-s.releaseClose:
		case <-s.abort:
		}
	}()
	return s
}

func (s *drainDependentPreflightStream) Chunks() <-chan canonical.Chunk { return s.chunks }

func (s *drainDependentPreflightStream) Result() (*canonical.FinalResult, error) {
	s.resultCalls.Add(1)
	<-s.sourceClosed
	return &canonical.FinalResult{ChunkCount: 3}, nil
}

func (s *preflightTestStream) Chunks() <-chan canonical.Chunk { return s.chunks }

func (s *preflightTestStream) Result() (*canonical.FinalResult, error) {
	s.resultCalls.Add(1)
	if s.onResult != nil {
		s.onResult()
	}
	return s.final, s.err
}

func closedPreflightStream(chunks []canonical.Chunk, final *canonical.FinalResult, err error) *preflightTestStream {
	ch := make(chan canonical.Chunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return &preflightTestStream{chunks: ch, final: final, err: err}
}

func collectPreflightChunks(t *testing.T, stream Stream) []canonical.Chunk {
	t.Helper()
	var got []canonical.Chunk
	for chunk := range stream.Chunks() {
		got = append(got, chunk)
	}
	return got
}

func textPreflightChunk(text string) canonical.Chunk {
	return canonical.Chunk{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: text}}
}

func thoughtPreflightChunk(text string) canonical.Chunk {
	return canonical.Chunk{Kind: canonical.ChunkKindThought, Thought: &canonical.ThoughtChunk{Content: text}}
}

func toolPreflightChunk(id, name string, args map[string]any) canonical.Chunk {
	return canonical.Chunk{Kind: canonical.ChunkKindToolCall, ToolCall: &canonical.ToolCallChunk{ID: id, Name: name, Args: args}}
}

func TestReplayStream_ReplaysExactOrderAndImmutableTerminalState(t *testing.T) {
	terminalErr := errors.New("terminal")
	chunks := []canonical.Chunk{
		textPreflightChunk("first"),
		thoughtPreflightChunk("second"),
		toolPreflightChunk("call-1", "weather", map[string]any{"city": "Boston"}),
	}
	final := &canonical.FinalResult{SessionID: "session-1", ChunkCount: 3, StopReason: canonical.StopEndTurn, ToolDenials: 2}
	stream := newReplayStream(context.Background(), chunks, final, terminalErr)

	// Mutating the source after construction must not alter replay-owned data.
	chunks[0].Text.Content = "mutated"
	chunks[2].ToolCall.Args["city"] = "Cambridge"
	final.ToolDenials = 99

	gotChunks := collectPreflightChunks(t, stream)
	wantChunks := []canonical.Chunk{
		textPreflightChunk("first"),
		thoughtPreflightChunk("second"),
		toolPreflightChunk("call-1", "weather", map[string]any{"city": "Boston"}),
	}
	if !reflect.DeepEqual(gotChunks, wantChunks) {
		t.Fatalf("chunks:\n got: %#v\nwant: %#v", gotChunks, wantChunks)
	}
	gotFinal, gotErr := stream.Result()
	if !errors.Is(gotErr, terminalErr) {
		t.Fatalf("Result error = %v, want %v", gotErr, terminalErr)
	}
	wantFinal := &canonical.FinalResult{SessionID: "session-1", ChunkCount: 3, StopReason: canonical.StopEndTurn, ToolDenials: 2}
	if !reflect.DeepEqual(gotFinal, wantFinal) {
		t.Fatalf("Result = %#v, want %#v", gotFinal, wantFinal)
	}

	// Result returns snapshots rather than an alias to retained terminal state.
	gotFinal.ToolDenials = 77
	again, againErr := stream.Result()
	if !errors.Is(againErr, terminalErr) || !reflect.DeepEqual(again, wantFinal) {
		t.Fatalf("second Result = (%#v, %v), want (%#v, %v)", again, againErr, wantFinal, terminalErr)
	}
}

func TestReplayStream_ZeroChunksClosesAndReturnsNilFinal(t *testing.T) {
	terminalErr := errors.New("empty terminal")
	stream := newReplayStream(context.Background(), nil, nil, terminalErr)
	select {
	case _, ok := <-stream.Chunks():
		if ok {
			t.Fatal("zero-chunk replay channel remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("zero-chunk replay did not close immediately")
	}
	final, err := stream.Result()
	if final != nil || !errors.Is(err, terminalErr) {
		t.Fatalf("Result = (%#v, %v), want (nil, %v)", final, err, terminalErr)
	}
}

func TestPrefixLiveStream_ReplaysPrefixThenUntouchedLiveBoundary(t *testing.T) {
	live := make(chan canonical.Chunk, 2)
	live <- textPreflightChunk("boundary")
	live <- textPreflightChunk("tail")
	close(live)
	underlying := &preflightTestStream{chunks: live, final: &canonical.FinalResult{ChunkCount: 4}}
	stream := newPrefixLiveStream(context.Background(), []canonical.Chunk{
		textPreflightChunk("prefix-1"), textPreflightChunk("prefix-2"),
	}, underlying, nil)

	got := collectPreflightChunks(t, stream)
	want := []canonical.Chunk{
		textPreflightChunk("prefix-1"), textPreflightChunk("prefix-2"),
		textPreflightChunk("boundary"), textPreflightChunk("tail"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks:\n got: %#v\nwant: %#v", got, want)
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result error = %v", err)
	}
	if calls := underlying.resultCalls.Load(); calls != 1 {
		t.Fatalf("underlying Result calls = %d, want 1", calls)
	}
}

func TestPrefixLiveStream_PreservesBackpressure(t *testing.T) {
	live := make(chan canonical.Chunk)
	underlying := &preflightTestStream{chunks: live, final: &canonical.FinalResult{ChunkCount: 3}}
	stream := newPrefixLiveStream(context.Background(), []canonical.Chunk{
		textPreflightChunk("prefix-1"), textPreflightChunk("prefix-2"),
	}, underlying, nil)
	out := stream.Chunks()

	sent := make(chan struct{})
	go func() {
		live <- textPreflightChunk("boundary")
		close(sent)
		close(live)
	}()
	select {
	case <-sent:
		t.Fatal("live boundary was consumed before the blocked prefix drained")
	case <-time.After(30 * time.Millisecond):
	}
	<-out
	<-out
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("live send stayed blocked after prefix drained")
	}
	<-out
	if _, ok := <-out; ok {
		t.Fatal("composite channel remained open")
	}
}

func TestPrefixLiveStream_ResultAndFinishExactlyOnceInOrder(t *testing.T) {
	live := make(chan canonical.Chunk)
	close(live)
	var mu sync.Mutex
	var events []string
	underlying := &preflightTestStream{
		chunks: live,
		final:  &canonical.FinalResult{SessionID: "terminal", ToolDenials: 3},
		err:    errors.New("terminal error"),
		onResult: func() {
			mu.Lock()
			events = append(events, "result")
			mu.Unlock()
		},
	}
	stream := newPrefixLiveStream(context.Background(), nil, underlying, func() {
		mu.Lock()
		events = append(events, "finish")
		mu.Unlock()
	})
	collectPreflightChunks(t, stream)

	for i := 0; i < 2; i++ {
		final, err := stream.Result()
		if final == nil || final.ToolDenials != 3 || err == nil || err.Error() != "terminal error" {
			t.Fatalf("Result %d = (%#v, %v)", i, final, err)
		}
		final.ToolDenials = 100
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(events, []string{"result", "finish"}) {
		t.Fatalf("events = %v, want [result finish]", events)
	}
	if calls := underlying.resultCalls.Load(); calls != 1 {
		t.Fatalf("underlying Result calls = %d, want 1", calls)
	}
}

func TestPrefixLiveStream_ContextCancellationStopsPump(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	underlying := newDrainDependentPreflightStream(3)
	t.Cleanup(func() { close(underlying.abort) })
	var finishCalls atomic.Int32
	stream := newPrefixLiveStream(ctx, []canonical.Chunk{textPreflightChunk("prefix")}, underlying, func() {
		finishCalls.Add(1)
	})
	out := stream.Chunks()
	cancel()
	closed := make(chan struct{})
	go func() {
		for range out {
			// A send that won immediately before cancellation is observable;
			// cancellation must still terminate the pump without blocking.
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("composite pump did not close after cancellation")
	}
	select {
	case <-underlying.sourceClosed:
		t.Fatal("source closed before the test released it; output responsiveness was not isolated")
	default:
	}
	close(underlying.releaseClose)
	select {
	case <-underlying.sourceClosed:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("source was not drained to closure after cancellation")
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result error = %v", err)
	}
	if calls := underlying.resultCalls.Load(); calls != 1 {
		t.Fatalf("underlying Result calls = %d, want 1", calls)
	}
	if calls := finishCalls.Load(); calls != 1 {
		t.Fatalf("finish calls = %d, want 1", calls)
	}
}

func TestCaptureToolProtocolAttempt_CompleteBelowCapsReturnsReplay(t *testing.T) {
	chunks := []canonical.Chunk{
		textPreflightChunk("ordinary answer"),
		thoughtPreflightChunk("private thought"),
	}
	final := &canonical.FinalResult{SessionID: "s", ChunkCount: 2, ToolDenials: 1}
	source := closedPreflightStream(chunks, final, nil)
	var finishCalls atomic.Int32
	capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, toolProtocolPolicy{}, nil, func() {
		finishCalls.Add(1)
	})
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	obs, replay := capture.observation, capture.stream
	if obs.Text != "ordinary answer" || obs.Final == nil || obs.Final.ToolDenials != 1 || obs.BufferBypass {
		t.Fatalf("observation = %#v", obs)
	}
	if _, ok := replay.(*replayStream); !ok {
		t.Fatalf("stream type = %T, want *replayStream", replay)
	}
	if got := collectPreflightChunks(t, replay); !reflect.DeepEqual(got, chunks) {
		t.Fatalf("replayed chunks = %#v, want %#v", got, chunks)
	}
	if finishCalls.Load() != 0 {
		t.Fatalf("finish calls = %d, want Task 5 to own finish after full capture", finishCalls.Load())
	}
}

func TestCaptureToolProtocolAttempt_DecisiveMatchingNativeCallReturnsPrefixLive(t *testing.T) {
	live := make(chan canonical.Chunk, 2)
	live <- toolPreflightChunk("call-1", "execute", map[string]any{"command": "pwd"})
	live <- textPreflightChunk("untouched")
	close(live)
	source := &preflightTestStream{chunks: live, final: &canonical.FinalResult{ChunkCount: 2}}
	policy := toolProtocolPolicy{
		requirement: toolProtocolRequired,
		tools:       []canonical.ToolSpec{{Name: "run_shell"}},
	}
	var finishCalls atomic.Int32
	capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, policy, map[string]string{"execute": "run_shell"}, func() {
		finishCalls.Add(1)
	})
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	obs, stream := capture.observation, capture.stream
	if !obs.NativeCall || obs.BufferBypass || len(obs.ToolCalls) != 1 {
		t.Fatalf("observation = %#v", obs)
	}
	if _, ok := stream.(*prefixLiveStream); !ok {
		t.Fatalf("stream type = %T, want *prefixLiveStream", stream)
	}
	want := []canonical.Chunk{
		toolPreflightChunk("call-1", "execute", map[string]any{"command": "pwd"}),
		textPreflightChunk("untouched"),
	}
	if got := collectPreflightChunks(t, stream); !reflect.DeepEqual(got, want) {
		t.Fatalf("chunks = %#v, want %#v", got, want)
	}
	if _, err := stream.Result(); err != nil {
		t.Fatalf("Result error = %v", err)
	}
	if finishCalls.Load() != 1 {
		t.Fatalf("finish calls = %d, want 1", finishCalls.Load())
	}
}

func TestCaptureToolProtocolAttempt_NamedMismatchIsNotDecisive(t *testing.T) {
	chunks := []canonical.Chunk{
		toolPreflightChunk("wrong", "read_file", nil),
		textPreflightChunk("tail"),
	}
	policy := toolProtocolPolicy{
		requirement: toolProtocolNamed,
		namedTool:   "get_weather",
		tools: []canonical.ToolSpec{
			{Name: "get_weather"}, {Name: "read_file"},
		},
	}
	capture, err := captureToolProtocolAttempt(context.Background(), closedPreflightStream(chunks, &canonical.FinalResult{}, nil), time.Second, policy, nil, nil)
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	obs, stream := capture.observation, capture.stream
	if _, ok := stream.(*replayStream); !ok {
		t.Fatalf("stream type = %T, want full replay after mismatch", stream)
	}
	if reason := classifyToolProtocolAttempt(policy, obs, nil); reason != ReasonNamedMismatch {
		t.Fatalf("classification = %q, want %q", reason, ReasonNamedMismatch)
	}
}

func TestNativeCallSatisfiesPolicy_RequiresOfferedTool(t *testing.T) {
	policy := toolProtocolPolicy{requirement: toolProtocolOptional}
	if nativeCallSatisfiesPolicy("execute", policy, nil) {
		t.Fatal("native call with zero offered tools was incorrectly decisive")
	}
}

func TestCaptureToolProtocolAttempt_ByteCapBypassesWithoutReadingNextChunk(t *testing.T) {
	first := textPreflightChunk(strings.Repeat("x", maxToolProtocolPreflightBytes))
	boundary := textPreflightChunk("overflow")
	next := textPreflightChunk("untouched")
	live := make(chan canonical.Chunk, 3)
	live <- first
	live <- boundary
	live <- next
	close(live)
	source := &preflightTestStream{chunks: live, final: &canonical.FinalResult{ChunkCount: 3}}

	capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, toolProtocolPolicy{}, nil, nil)
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	obs, stream := capture.observation, capture.stream
	if !obs.BufferBypass {
		t.Fatalf("BufferBypass = false, observation %#v", obs)
	}
	if remaining := len(live); remaining != 1 {
		t.Fatalf("live channel retained %d chunks, want 1 untouched next chunk", remaining)
	}
	if got := collectPreflightChunks(t, stream); !reflect.DeepEqual(got, []canonical.Chunk{first, boundary, next}) {
		t.Fatalf("chunks = %#v; boundary was duplicated or dropped", got)
	}
}

func TestCaptureToolProtocolAttempt_AllChunkKindsCountTowardByteCap(t *testing.T) {
	tests := []struct {
		name   string
		first  canonical.Chunk
		policy toolProtocolPolicy
	}{
		{
			name:  "thought",
			first: thoughtPreflightChunk(strings.Repeat("x", maxToolProtocolPreflightBytes)),
		},
		{
			name: "plan",
			first: canonical.Chunk{
				Kind: canonical.ChunkKindPlan,
				Plan: &canonical.PlanChunk{Content: strings.Repeat("x", maxToolProtocolPreflightBytes)},
			},
		},
		{
			name:   "tool call",
			first:  toolPreflightChunk(strings.Repeat("x", maxToolProtocolPreflightBytes), "not_offered", map[string]any{"nested": []any{"payload"}}),
			policy: toolProtocolPolicy{tools: []canonical.ToolSpec{{Name: "offered"}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := make(chan canonical.Chunk, 2)
			live <- tt.first
			live <- textPreflightChunk("boundary")
			close(live)
			source := &preflightTestStream{chunks: live, final: &canonical.FinalResult{ChunkCount: 2}}
			capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, tt.policy, nil, nil)
			if err != nil {
				t.Fatalf("capture error = %v", err)
			}
			if !capture.observation.BufferBypass {
				t.Fatalf("%s payload did not count toward byte cap", tt.name)
			}
			if got := len(collectPreflightChunks(t, capture.stream)); got != 2 {
				t.Fatalf("composite chunk count = %d, want 2", got)
			}
		})
	}
}

func TestRetainedChunkBytesWithinBudget_LargeEmptyContainerExceeds(t *testing.T) {
	items := make([]any, maxToolProtocolPreflightBytes/16+1)
	chunk := toolPreflightChunk("id", "not_offered", map[string]any{"items": items})
	if _, exceeded := retainedChunkBytesWithinBudget(chunk, maxToolProtocolPreflightBytes); !exceeded {
		t.Fatal("large []any of nil values did not exceed retained-byte budget")
	}
}

func TestRetainedChunkBytesWithinBudget_DeepAndCyclicGraphsExceed(t *testing.T) {
	t.Run("deep", func(t *testing.T) {
		root := map[string]any{}
		cursor := root
		for i := 0; i < 128; i++ {
			next := map[string]any{}
			cursor["next"] = next
			cursor = next
		}
		chunk := toolPreflightChunk("id", "not_offered", root)
		if _, exceeded := retainedChunkBytesWithinBudget(chunk, maxToolProtocolPreflightBytes); !exceeded {
			t.Fatal("excessively deep argument graph did not exceed safe traversal budget")
		}
	})

	t.Run("cycle", func(t *testing.T) {
		cycle := map[string]any{}
		cycle["self"] = cycle
		chunk := toolPreflightChunk("id", "not_offered", cycle)
		if _, exceeded := retainedChunkBytesWithinBudget(chunk, maxToolProtocolPreflightBytes); !exceeded {
			t.Fatal("cyclic argument graph did not exceed safe traversal budget")
		}
	})
}

func TestSaturatingRetainedBytes_DoesNotOverflow(t *testing.T) {
	got, exceeded := saturatingRetainedBytes(math.MaxInt, math.MaxInt, maxToolProtocolPreflightBytes)
	if !exceeded || got != maxToolProtocolPreflightBytes+1 {
		t.Fatalf("saturatingRetainedBytes = (%d, %v), want (%d, true)", got, exceeded, maxToolProtocolPreflightBytes+1)
	}
}

func TestCaptureToolProtocolAttempt_LargeEmptyContainerBypassesBeforeClone(t *testing.T) {
	items := make([]any, maxToolProtocolPreflightBytes/16+1)
	chunk := toolPreflightChunk("id", "not_offered", map[string]any{"items": items})
	policy := toolProtocolPolicy{tools: []canonical.ToolSpec{{Name: "offered"}}}
	source := closedPreflightStream([]canonical.Chunk{chunk}, &canonical.FinalResult{ChunkCount: 1}, nil)
	capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, policy, nil, nil)
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	if !capture.observation.BufferBypass {
		t.Fatal("large empty container was cloned into replay instead of bypassed")
	}
	if got := collectPreflightChunks(t, capture.stream); !reflect.DeepEqual(got, []canonical.Chunk{chunk}) {
		t.Fatalf("boundary passthrough = %#v, want original chunk", got)
	}
}

func TestCaptureToolProtocolAttempt_ChunkCapBypassesWithoutReadingNextChunk(t *testing.T) {
	live := make(chan canonical.Chunk, maxToolProtocolPreflightChunks+2)
	for i := 0; i < maxToolProtocolPreflightChunks+2; i++ {
		live <- textPreflightChunk("")
	}
	close(live)
	source := &preflightTestStream{chunks: live, final: &canonical.FinalResult{ChunkCount: maxToolProtocolPreflightChunks + 2}}
	capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, toolProtocolPolicy{}, nil, nil)
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	obs, stream := capture.observation, capture.stream
	if !obs.BufferBypass {
		t.Fatal("chunk overflow did not set BufferBypass")
	}
	if remaining := len(live); remaining != 1 {
		t.Fatalf("live channel retained %d chunks, want 1 untouched next chunk", remaining)
	}
	if got := len(collectPreflightChunks(t, stream)); got != maxToolProtocolPreflightChunks+2 {
		t.Fatalf("replayed/live chunk count = %d, want %d", got, maxToolProtocolPreflightChunks+2)
	}
}

func TestCaptureToolProtocolAttempt_IdleTimeout(t *testing.T) {
	live := make(chan canonical.Chunk)
	capture, err := captureToolProtocolAttempt(context.Background(), &preflightTestStream{chunks: live}, 20*time.Millisecond, toolProtocolPolicy{}, nil, nil)
	if capture.stream != nil {
		t.Fatalf("stream = %T, want nil on idle timeout", capture.stream)
	}
	if !errors.Is(err, canonical.ErrStreamIdleTimeout) {
		t.Fatalf("error = %v, want wrapped ErrStreamIdleTimeout", err)
	}
}

func TestCaptureToolProtocolAttempt_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	capture, err := captureToolProtocolAttempt(ctx, &preflightTestStream{chunks: make(chan canonical.Chunk)}, time.Second, toolProtocolPolicy{}, nil, nil)
	if capture.stream != nil {
		t.Fatalf("stream = %T, want nil on cancellation", capture.stream)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want wrapped context.Canceled", err)
	}
}

func TestCaptureToolProtocolAttempt_PreservesTerminalResultError(t *testing.T) {
	terminalErr := errors.New("upstream terminal")
	source := closedPreflightStream([]canonical.Chunk{textPreflightChunk("answer")}, &canonical.FinalResult{ToolDenials: 4}, terminalErr)
	capture, err := captureToolProtocolAttempt(context.Background(), source, time.Second, toolProtocolPolicy{}, nil, nil)
	if !errors.Is(err, terminalErr) {
		t.Fatalf("capture error = %v, want preserved %v", err, terminalErr)
	}
	if capture.observation.Final == nil || capture.observation.Final.ToolDenials != 4 {
		t.Fatalf("observation final = %#v", capture.observation.Final)
	}
	if capture.stream != nil {
		t.Fatalf("stream = %T, want nil after terminal error", capture.stream)
	}
}

func TestCaptureToolProtocolAttempt_ThoughtsReplayButAreNotWrapperScanned(t *testing.T) {
	wrapper := `{"tool_call":{"name":"get_weather","arguments":{"city":"Boston"}}}`
	chunks := []canonical.Chunk{
		thoughtPreflightChunk(wrapper),
		textPreflightChunk("ordinary answer"),
	}
	policy := toolProtocolPolicy{
		requirement: toolProtocolRequired,
		tools:       []canonical.ToolSpec{{Name: "get_weather"}},
	}
	capture, err := captureToolProtocolAttempt(context.Background(), closedPreflightStream(chunks, &canonical.FinalResult{}, nil), time.Second, policy, nil, nil)
	if err != nil {
		t.Fatalf("capture error = %v", err)
	}
	obs, stream := capture.observation, capture.stream
	if obs.Text != "ordinary answer" {
		t.Fatalf("observation text = %q, want only visible text", obs.Text)
	}
	if reason := classifyToolProtocolAttempt(policy, obs, nil); reason != ReasonRequiredMissing {
		t.Fatalf("classification = %q, want %q (thought wrapper must not count)", reason, ReasonRequiredMissing)
	}
	if got := collectPreflightChunks(t, stream); !reflect.DeepEqual(got, chunks) {
		t.Fatalf("thought was not retained for replay: %#v", got)
	}
}
