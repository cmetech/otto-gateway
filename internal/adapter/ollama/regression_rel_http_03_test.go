package ollama

// Regression test for REL-HTTP-03 (H-3) — Ollama surface:
// When kiro-cli dies mid-stream, run.Stream().Result() returns a non-nil error.
// The current finalizeNDJSON path logs at debug and returns without emitting a
// `{"done":true,"done_reason":"error"}` terminal line. The client receives HTTP
// 200 + partial NDJSON lines + clean TCP close. LangFlow's NDJSON consumer
// never sees the `done:true` marker it aggregates on.
//
// Pre-fix observable: the NDJSON body does NOT contain `"done_reason":"error"`,
// and does NOT contain a `{"done":true,...}` terminal line after the mid-stream
// worker death.
//
// Post-fix: emit the same surface-native terminal error line the idle-timeout
// path already emits (ndjson.go:437-450 for chat; :445-450 for generate), log
// at WARN. Unskip in Phase 15 fix commit and flip assertions.

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent demonstrates that a
// mid-stream terminal error on the Ollama NDJSON surface silently truncates
// the stream without emitting a done:true terminal line or done_reason:"error".
//
// The reproducer uses fakeRunHandle with a fakeStream whose final result carries
// a non-nil error (simulating kiro-cli dying after partial chunk delivery). The
// emitter finishes via finalizeNDJSON, which calls run.Stream().Result() and
// encounters the error — falling through to the silent return path.
func TestRegression_REL_HTTP_03_MidStreamTruncationIsSilent(t *testing.T) {
	t.Skip("REL-HTTP-03 (H-3): regression test — unskip in Phase 15 fix commit")

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
		t.Error("pre-fix reproducer: runNDJSONEmitter returned nil error on worker death; expected non-nil")
	}

	body := rec.Body.String()

	// Pre-fix observable (assertion 1): body does NOT contain done_reason:"error".
	// The idle-timeout path emits it (ndjson.go:441); finalizeNDJSON does not on
	// the error path.
	if strings.Contains(body, `"done_reason":"error"`) {
		t.Errorf("pre-fix reproducer: body unexpectedly contains done_reason:error — "+
			"bug may already be fixed; body=%q", body)
	}

	// Pre-fix observable (assertion 2): body does NOT contain a terminal
	// done:true line. Clients that aggregate on done:true never see end-of-stream.
	if strings.Contains(body, `"done":true`) {
		t.Errorf("pre-fix reproducer: body unexpectedly contains done:true — "+
			"bug may already be fixed; body=%q", body)
	}

	t.Logf("pre-fix observable confirmed: no done:true or done_reason:error in truncated Ollama stream; body=%q", body)
}
