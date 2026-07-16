package openai

// Regression test for REL-HTTP-03 (H-3) — OpenAI surface:
// When kiro-cli dies mid-stream, run.Stream().Result() returns a non-nil error.
// Post-fix: finalizeSSE emits an error data-frame + [DONE] terminator and logs
// at WARN (D-09/D-10), so the client gets an explicit signal instead of a
// silent truncated stream.
//
// H-3 fix (REL-HTTP-03): unskipped in Phase 15. Assertions flipped from
// pre-fix "body does NOT contain error frame / [DONE]" to post-fix "body DOES
// contain upstream_disconnect frame AND [DONE]".

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent verifies that a
// mid-stream terminal error on the OpenAI surface emits an error frame and
// [DONE] terminator rather than silently truncating the stream.
//
// The reproducer uses fakeRunHandle with a fakeStream whose final result carries
// a non-nil error (simulating kiro-cli dying after partial chunk delivery). The
// emitter finishes via finalizeSSE, which calls run.Stream().Result() and
// encounters the error — post-fix it emits the terminal frames and logs at WARN.
func TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent(t *testing.T) {
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
	_, err := runSSEEmitter(context.Background(), rec, run, &canonical.ChatRequest{}, nil, "auto", 0, nullLogger())

	// runSSEEmitter should return a non-nil error wrapping the worker death.
	if err == nil {
		t.Error("post-fix: runSSEEmitter returned nil error on worker death; expected non-nil")
	}

	body := rec.Body.String()

	// Post-fix assertion 1: body DOES contain an upstream_disconnect error frame.
	if !strings.Contains(body, `"code":"upstream_disconnect"`) {
		t.Errorf("post-fix regression: body does not contain upstream_disconnect error frame; body=%q", body)
	}

	// Post-fix assertion 2: body DOES contain [DONE] after the error frame.
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("post-fix regression: body does not contain [DONE] terminator; body=%q", body)
	}

	t.Logf("post-fix verified: error frame and [DONE] present in body after mid-stream worker death; body=%q", body)
}
