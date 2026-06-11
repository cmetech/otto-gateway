package ollama

// Regression test for REL-HTTP-03 (H-3) — Ollama surface:
// When kiro-cli dies mid-stream, run.Stream().Result() returns a non-nil error.
// Post-fix: finalizeNDJSON emits a done:true + done_reason:error terminal line
// and logs at WARN (D-09/D-10), so LangFlow's NDJSON aggregator gets an explicit
// end-of-stream marker instead of a silent truncated body.
//
// H-3 fix (REL-HTTP-03): unskipped in Phase 15. Assertions flipped from
// pre-fix "body does NOT contain done_reason:error / done:true" to post-fix
// "body DOES contain done_reason:error AND done:true".

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent verifies that a
// mid-stream terminal error on the Ollama NDJSON surface emits a done:true +
// done_reason:error terminal line rather than silently truncating the stream.
//
// The reproducer uses fakeRunHandle with a fakeStream whose final result carries
// a non-nil error (simulating kiro-cli dying after partial chunk delivery). The
// emitter finishes via finalizeNDJSON, which calls run.Stream().Result() and
// encounters the error — post-fix it emits the terminal done:true frame and
// logs at WARN.
func TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent(t *testing.T) {
	// Pre-populate with one text chunk then close the channel.
	// This triggers the chunks-closed → finalizeNDJSON path, where Result()
	// returns an error simulating kiro-cli mid-stream death.
	ch := make(chan canonical.Chunk, 1)
	ch <- canonical.Chunk{
		Kind: canonical.ChunkKindText,
		Text: &canonical.TextChunk{Content: "partial answer"},
	}
	close(ch) // triggers finalizeNDJSON

	run := newFakeRunHandle(
		nil, // chunks already in pre-populated ch; re-use via fakeStream directly
		nil,
		errors.New("worker died"),
	)
	// Override the channel to use the pre-populated one.
	run.stream.ch = ch

	rec := httptest.NewRecorder()
	start := time.Now()

	_, err := runNDJSONEmitter(context.Background(), noopCancelFn, rec, run,
		"auto", true, start, nilLogger(), nil, 0)

	// runNDJSONEmitter should return a non-nil error wrapping the worker death.
	if err == nil {
		t.Error("post-fix: runNDJSONEmitter returned nil error on worker death; expected non-nil")
	}

	body := rec.Body.String()

	// Post-fix assertion 1: body DOES contain done_reason:"error".
	if !strings.Contains(body, `"done_reason":"error"`) {
		t.Errorf("post-fix regression: body does not contain done_reason:error; body=%q", body)
	}

	// Post-fix assertion 2: body DOES contain a terminal done:true line.
	// LangFlow's NDJSON aggregator needs this to recognize end-of-stream on failure.
	if !strings.Contains(body, `"done":true`) {
		t.Errorf("post-fix regression: body does not contain done:true; body=%q", body)
	}

	t.Logf("post-fix verified: done:true and done_reason:error present in Ollama body after mid-stream worker death; body=%q", body)
}
