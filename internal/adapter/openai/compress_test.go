// Task 9 (compress model-suffix split + X-Compression header stamp) —
// handler-level tests for the OpenAI surface (/v1/chat/completions,
// /v1/completions).
//
// /v1/chat/completions reuses the fakeEngine harness (integration_test.go)
// extended with lastReq/lastCtx capture. /v1/completions builds its
// canonical.ChatRequest INLINE in handleCompletions (the fifth builder
// site missed by the original plan — review MAJOR-3) rather than via
// wireToChatRequest, so it is covered here at the handler level using the
// captureEngine harness from completions_test.go (extended with lastCtx).
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
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
const compressTestModel = "qwen-2.5+compress"
const compressStrippedModel = "qwen-2.5"

// compressHeaderCase is one row of the X-Compression header-stamp table
// shared by the subtests below.
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
	// suffix directive — the ctx stamp must carry the HEADER's value.
	{"header_0_wins_over_plus_suffix", "0", true, false},
}

// doJSONPost posts body to path on srv with optional extra headers.
func doJSONPost(t *testing.T, srv httpDoer, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := srv.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// httpDoer is satisfied by *http.Client (srv.Client()).
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// assertSSEChatModelEcho scans a /v1/chat/completions SSE body and
// asserts every chat.completion.chunk frame's "model" field echoes
// wantModel.
func assertSSEChatModelEcho(t *testing.T, body, wantModel string) {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	sawModel := false
	for scanner.Scan() {
		line := scanner.Text()
		if line == "data: [DONE]" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatalf("unmarshal chunk: %v (payload=%q)", err, line)
		}
		model, ok := chunk["model"].(string)
		if !ok || model == "" {
			continue
		}
		sawModel = true
		if model != wantModel {
			t.Errorf("chunk model: got %q, want original %q", model, wantModel)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}
	if !sawModel {
		t.Errorf("no chunk carried a model field; body=%q", body)
	}
}

// TestOpenAI_XCompressionHeader_ChatCompletions covers /v1/chat/completions,
// streaming and non-streaming, over the compressHeaderCases table.
func TestOpenAI_XCompressionHeader_ChatCompletions(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range compressHeaderCases {
			streamLabel := "non_streaming"
			if streaming {
				streamLabel = "streaming"
			}
			t.Run(tc.name+"/"+streamLabel, func(t *testing.T) {
				eng := &fakeEngine{
					collectResp: &canonical.ChatResponse{
						StopReason: canonical.StopEndTurn,
						Message: canonical.Message{
							Role:    canonical.RoleAssistant,
							Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "pong"}},
						},
					},
					runChunks: []canonical.Chunk{
						{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "pong"}},
					},
					runFinal: &canonical.FinalResult{StopReason: canonical.StopEndTurn},
				}
				srv := mountedAdapter(newFakeAdapter(eng))
				defer srv.Close()

				streamField := `"stream":false`
				if streaming {
					streamField = `"stream":true`
				}
				body := `{"model":"` + compressTestModel + `","messages":[{"role":"user","content":"ping"}],` + streamField + `}`

				headers := map[string]string{}
				if tc.header != "" {
					headers["X-Compression"] = tc.header
				}
				resp := doJSONPost(t, srv.Client(), srv.URL+"/v1/chat/completions", body, headers)
				defer func() { _ = resp.Body.Close() }()

				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status: got %d, want 200", resp.StatusCode)
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
					t.Errorf("captured req.Model: got %q, want stripped base %q", eng.lastReq.Model, compressStrippedModel)
				}

				rawBody, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if streaming {
					assertSSEChatModelEcho(t, string(rawBody), compressTestModel)
				} else {
					var completion chatCompletion
					if err := json.Unmarshal(rawBody, &completion); err != nil {
						t.Fatalf("decode chat.completion: %v; body=%s", err, rawBody)
					}
					if completion.Model != compressTestModel {
						t.Errorf("response model echo: got %q, want original %q", completion.Model, compressTestModel)
					}
				}
			})
		}
	}
}

// TestHandleCompletions_CompressSuffix covers Task 9 Step 1 item (a) —
// the FIFTH builder site: /v1/completions builds its canonical.ChatRequest
// INLINE in handleCompletions (not via wireToChatRequest), so the
// model-suffix split must be applied there directly too.
func TestHandleCompletions_CompressSuffix(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		wantModel     string
		wantHasKey    bool
		wantDirective bool
	}{
		{"plus_compress", "qwen-2.5+compress", "qwen-2.5", true, true},
		{"minus_compress", "qwen-2.5-compress", "qwen-2.5", true, false},
		{"no_suffix", "qwen-2.5", "qwen-2.5", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var receivedReq *canonical.ChatRequest
			eng := &captureEngine{
				collectResp: &canonical.ChatResponse{
					StopReason: canonical.StopEndTurn,
					Message: canonical.Message{
						Role:    canonical.RoleAssistant,
						Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
					},
				},
				captureReq: &receivedReq,
			}
			srv := mountedAdapterForCompletions(eng)
			defer srv.Close()

			resp := doCompletions(t, srv, `{"model":"`+tc.model+`","prompt":"hi"}`)
			defer func() { _ = resp.Body.Close() }()

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d, want 200", resp.StatusCode)
			}
			if receivedReq == nil {
				t.Fatal("engine never received a request")
			}
			if receivedReq.Model != tc.wantModel {
				t.Errorf("Model: got %q, want %q", receivedReq.Model, tc.wantModel)
			}
			got, ok := receivedReq.Metadata[compress.MetadataKey].(bool)
			if ok != tc.wantHasKey {
				t.Errorf("Metadata[%q] present: got %v, want %v", compress.MetadataKey, ok, tc.wantHasKey)
			}
			if tc.wantHasKey && got != tc.wantDirective {
				t.Errorf("Metadata[%q]: got %v, want %v", compress.MetadataKey, got, tc.wantDirective)
			}
		})
	}
}

// TestOpenAI_XCompressionHeader_Completions covers /v1/completions —
// JSON-only, non-streaming row plus a stream:true row asserting the
// D-03 downgrade (still JSON, no SSE) while verifying header precedence
// and the response model echo.
func TestOpenAI_XCompressionHeader_Completions(t *testing.T) {
	for _, streamTrue := range []bool{false, true} {
		for _, tc := range compressHeaderCases {
			label := "stream_false"
			if streamTrue {
				label = "stream_true_downgraded"
			}
			t.Run(tc.name+"/"+label, func(t *testing.T) {
				var receivedReq *canonical.ChatRequest
				eng := &captureEngine{
					collectResp: &canonical.ChatResponse{
						StopReason: canonical.StopEndTurn,
						Message: canonical.Message{
							Role:    canonical.RoleAssistant,
							Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "pong"}},
						},
					},
					captureReq: &receivedReq,
				}
				srv := mountedAdapterForCompletions(eng)
				defer srv.Close()

				streamField := ""
				if streamTrue {
					streamField = `,"stream":true`
				}
				body := `{"model":"` + compressTestModel + `","prompt":"hi"` + streamField + `}`

				headers := map[string]string{}
				if tc.header != "" {
					headers["X-Compression"] = tc.header
				}
				resp := doJSONPost(t, srv.Client(), srv.URL+"/v1/completions", body, headers)
				defer func() { _ = resp.Body.Close() }()

				if resp.StatusCode != http.StatusOK {
					t.Fatalf("status: got %d, want 200", resp.StatusCode)
				}
				// D-03: /v1/completions is JSON-only — stream:true must
				// NOT produce an SSE response even when requested.
				if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/event-stream") {
					t.Fatalf("Content-Type: got %q, must not be SSE (D-03 JSON-only shim)", ct)
				}

				got, ok := compress.HeaderDirectiveFromContext(eng.lastCtx)
				if ok != tc.wantStamped {
					t.Errorf("HeaderDirectiveFromContext ok: got %v, want %v", ok, tc.wantStamped)
				}
				if tc.wantStamped && got != tc.wantDirective {
					t.Errorf("HeaderDirectiveFromContext value: got %v, want %v", got, tc.wantDirective)
				}

				if receivedReq == nil {
					t.Fatal("engine never received a request")
				}
				if receivedReq.Model != compressStrippedModel {
					t.Errorf("captured req.Model: got %q, want stripped base %q", receivedReq.Model, compressStrippedModel)
				}

				var tc2 textCompletion
				if err := json.NewDecoder(resp.Body).Decode(&tc2); err != nil {
					t.Fatalf("decode textCompletion: %v", err)
				}
				if tc2.Model != compressTestModel {
					t.Errorf("response model echo: got %q, want original %q", tc2.Model, compressTestModel)
				}
			})
		}
	}
}
