// Task 9 (compress model-suffix split + X-Compression header stamp) —
// handler-level tests for the Anthropic surface (/v1/messages).
//
// Mirrors the fakeEngine harness in handlers_test.go (lastReq/lastCtx
// capture) and doPostWithHeader. Both wire.Stream branches route through
// CollectAnthropicChat -> eng.Run (Phase 6 Plan 04 Task 2 — see
// TestHandleMessages_Happy_NonStreaming's collectN==0/runN==1 assertion),
// so a fakeEngine with only collectResp set (no runHandle) exercises both
// the streaming (SSE) and non-streaming (JSON) response shapes via the
// same synthesizeRunHandleFromCollectResp path. Covers:
//   - X-Compression header parses to a ctx-stamped tri-state directive
//     (compress.HeaderDirectiveFromContext), observed via fakeEngine.lastCtx.
//   - the +compress suffix on the request model is stripped before the
//     canonical request reaches the engine (fakeEngine.lastReq.Model),
//     BEFORE hyphen-version normalization.
//   - the response always echoes the caller's ORIGINAL directive-bearing
//     model string, streaming (SSE message_start) and non-streaming
//     (JSON) alike.
package anthropic

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/compress"
)

// compressTestModel carries a +compress suffix on every row of the
// X-Compression header table so the "captured canonical request carries
// the stripped base" assertion applies uniformly, including the
// "header wins over suffix" row where the header disagrees with the
// suffix's own directive.
const compressTestModel = "claude-sonnet-4-6+compress"

// compressStrippedModel is compressTestModel with the +compress suffix
// removed and hyphen-version normalized (claude-sonnet-4-6 -> 4.6),
// mirroring wireToChatRequest's stated ordering (strip THEN normalize).
const compressStrippedModel = "claude-sonnet-4.6"

// compressHeaderCase is one row of the X-Compression header-stamp table
// shared by the streaming/non-streaming subtests below.
type compressHeaderCase struct {
	name          string
	header        string // "" means no X-Compression header sent
	wantStamped   bool
	wantDirective bool
}

var compressHeaderCases = []compressHeaderCase{
	{"header_1_enable", "1", true, true},
	{"header_true_enable", "true", true, true},
	{"header_0_disable", "0", true, false},
	{"header_off_disable", "off", true, false},
	{"header_garbage_not_stamped", "garbage", false, false},
	{"no_header_not_stamped", "", false, false},
	// review MINOR-8 / brief item b: header disagrees with the model
	// suffix directive (suffix says enable, header says disable) —
	// the ctx stamp must carry the HEADER's value.
	{"header_0_wins_over_plus_suffix", "0", true, false},
}

// sseMessageStart mirrors the subset of the message_start payload
// needed to assert the response model-echo contract.
type sseMessageStart struct {
	Message struct {
		Model string `json:"model"`
	} `json:"message"`
}

// assertSSEModelEcho scans an SSE response body for the message_start
// event and asserts its embedded message.model echoes wantModel.
func assertSSEModelEcho(t *testing.T, body, wantModel string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "event: message_start" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if !strings.HasPrefix(lines[j], "data: ") {
				continue
			}
			var payload sseMessageStart
			if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[j], "data: ")), &payload); err != nil {
				t.Fatalf("decode message_start data: %v; line=%q", err, lines[j])
			}
			if payload.Message.Model != wantModel {
				t.Errorf("message_start.message.model: got %q, want original %q", payload.Message.Model, wantModel)
			}
			return
		}
		t.Fatalf("message_start event had no following data: line; body=%q", body)
	}
	t.Errorf("no message_start event found in SSE body; body=%q", body)
}

// TestAnthropic_XCompressionHeader covers /v1/messages, streaming and
// non-streaming, over the compressHeaderCases table.
func TestAnthropic_XCompressionHeader(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range compressHeaderCases {
			streamLabel := "non_streaming"
			if streaming {
				streamLabel = "streaming"
			}
			t.Run(tc.name+"/"+streamLabel, func(t *testing.T) {
				eng := &fakeEngine{
					collectResp: &canonical.ChatResponse{
						Message: canonical.Message{
							Role:    canonical.RoleAssistant,
							Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "Hello!"}},
						},
						StopReason: canonical.StopEndTurn,
					},
				}
				a := newTestAdapter(eng)

				streamField := `"stream":false`
				if streaming {
					streamField = `"stream":true`
				}
				body := `{"model":"` + compressTestModel + `","max_tokens":256,"messages":[{"role":"user","content":"hi"}],` + streamField + `}`

				headers := map[string]string{}
				if tc.header != "" {
					headers["X-Compression"] = tc.header
				}
				w := doPostWithHeader(t, a, "/messages", body, headers)

				if w.Code != http.StatusOK {
					t.Fatalf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
				}

				got, ok := compress.HeaderDirectiveFromContext(eng.lastCtx)
				if ok != tc.wantStamped {
					t.Errorf("HeaderDirectiveFromContext ok: got %v, want %v", ok, tc.wantStamped)
				}
				if tc.wantStamped && got != tc.wantDirective {
					t.Errorf("HeaderDirectiveFromContext value: got %v, want %v", got, tc.wantDirective)
				}

				if eng.lastReq == nil {
					t.Fatal("engine never received a request")
				}
				if eng.lastReq.Model != compressStrippedModel {
					t.Errorf("captured req.Model: got %q, want stripped+normalized %q", eng.lastReq.Model, compressStrippedModel)
				}

				if streaming {
					if ct := w.Header().Get("Content-Type"); ct != "text/event-stream" {
						t.Fatalf("Content-Type: got %q, want text/event-stream", ct)
					}
					assertSSEModelEcho(t, w.Body.String(), compressTestModel)
				} else {
					var resp anthropicMessage
					if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
						t.Fatalf("decode response: %v", err)
					}
					if resp.Model != compressTestModel {
						t.Errorf("response model echo: got %q, want original %q", resp.Model, compressTestModel)
					}
				}
			})
		}
	}
}
