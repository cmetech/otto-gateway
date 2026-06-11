package openai

// Regression test for REL-HTTP-03 (H-3) — OpenAI surface:
// When kiro-cli dies mid-stream, run.Stream().Result() returns a non-nil error.
// The current finalizeSSE path logs at debug and returns without emitting an
// error data-frame or the mandatory [DONE] terminator. The client receives HTTP
// 200 + partial deltas + clean TCP close — a half-finished answer presented as
// complete.
//
// Pre-fix observable: the SSE body does NOT contain `data: {"error":` and does
// NOT contain `data: [DONE]` after the mid-stream worker death.
//
// Post-fix: emit the same surface-native terminal error frame that the
// idle-timeout path already emits (openai/sse.go:470-472), then [DONE], and
// log at WARN. Unskip this test in the Phase 15 fix commit and flip the
// assertions.

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent demonstrates that a
// mid-stream terminal error on the OpenAI surface silently truncates the stream
// without emitting an error frame or [DONE] terminator.
//
// The reproducer uses fakeRunHandle with a fakeStream whose final result carries
// a non-nil error (simulating kiro-cli dying after partial chunk delivery). The
// emitter finishes via finalizeSSE, which calls run.Stream().Result() and
// encounters the error — falling through to the silent return path.
func TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent(t *testing.T) {
	t.Skip("REL-HTTP-03 (H-3): regression test — unskip in Phase 15 fix commit")
	defer goleak.VerifyNone(t)

	// Pre-populate the chunks channel with one text chunk, then close it.
	// This triggers the chunks-closed → finalizeSSE path, where Result() returns
	// an error simulating kiro-cli mid-stream death.
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{
		Kind: canonical.ChunkKindText,
		Text: &canonical.TextChunk{Content: "partial answer"},
	}
	close(ch) // channel close triggers finalizeSSE

	run := &fakeRunHandle{
		stream: &fakeStream{
			chunks: ch,
			// Final result carries a non-nil error — kiro-cli died mid-stream.
			rerr: errors.New("worker died"),
		},
		sessionID: "session_mid_stream_death",
	}

	rec := httptest.NewRecorder()
	_, err := runSSEEmitter(context.Background(), rec, run, &canonical.ChatRequest{}, "auto", 0, nullLogger())

	// runSSEEmitter should return a non-nil error wrapping the worker death.
	if err == nil {
		t.Error("pre-fix reproducer: runSSEEmitter returned nil error on worker death; expected non-nil")
	}

	body := rec.Body.String()

	// Pre-fix observable (assertion 1): body does NOT contain an error frame.
	// The idle-timeout path emits one (sse.go:470); finalizeSSE does not.
	if strings.Contains(body, `data: {"error":`) {
		t.Errorf("pre-fix reproducer: body unexpectedly contains error frame — "+
			"bug may already be fixed; body=%q", body)
	}

	// Pre-fix observable (assertion 2): body does NOT contain [DONE] after the
	// error. The idle-timeout path emits it (sse.go:471); finalizeSSE does not.
	if strings.Contains(body, "data: [DONE]") {
		t.Errorf("pre-fix reproducer: body unexpectedly contains [DONE] — "+
			"bug may already be fixed; body=%q", body)
	}

	t.Logf("pre-fix observable confirmed: no error frame, no [DONE] in truncated stream; body=%q", body)
}
