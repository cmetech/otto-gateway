// Task 9 (compress model-suffix split + X-Compression header stamp) —
// handler-level tests for the Ollama surface (/api/chat, /api/generate).
//
// Mirrors the fakeEngine harness in handlers_test.go (lastReq/lastCtx
// capture) and the doPost helper's request-construction style. Covers:
//   - X-Compression header parses to a ctx-stamped tri-state directive
//     (compress.HeaderDirectiveFromContext), observed via fakeEngine.lastCtx.
//   - the +compress suffix on the request model is stripped before the
//     canonical request reaches the engine (fakeEngine.lastReq.Model).
//   - the response always echoes the caller's ORIGINAL directive-bearing
//     model string, streaming (NDJSON) and non-streaming (JSON) alike.
package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/plugin/compress"
)

// compressTestModel carries a +compress suffix on every row of the
// X-Compression header table so the "captured canonical request carries
// the stripped base" assertion applies uniformly (Task 9 brief Step 1
// item c) — including the "header wins over suffix" row, where the
// header value ("0") disagrees with the suffix's own directive ("+").
const compressTestModel = "qwen-2.5+compress"

// compressHeaderCase is one row of the X-Compression header-stamp table
// shared by the /api/chat and /api/generate subtests below.
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
	// suffix directive (suffix says enable, header says disable) — the
	// ctx stamp must still carry the HEADER's value; precedence itself
	// is a CompressionHook concern, but the adapter must not silently
	// skip stamping just because a suffix is also present.
	{"header_0_wins_over_plus_suffix", "0", true, false},
}

// doPostWithHeader posts body to path on the adapter's protected router
// with optional extra headers set (X-Compression, etc.). Mirrors
// handlers_test.go's doPost plus the anthropic package's
// doPostWithHeader convention.
func doPostWithHeader(t *testing.T, a *Adapter, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	a.ProtectedRouter().ServeHTTP(w, r)
	return w
}

// assertNDJSONModelEcho scans an NDJSON response body and asserts every
// frame that carries a non-empty "model" field echoes wantModel.
func assertNDJSONModelEcho(t *testing.T, body, wantModel string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	sawModel := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		var frame struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			t.Fatalf("decode NDJSON line: %v; line=%q", err, line)
		}
		if frame.Model != "" {
			sawModel = true
			if frame.Model != wantModel {
				t.Errorf("NDJSON frame model: got %q, want %q", frame.Model, wantModel)
			}
		}
	}
	if !sawModel {
		t.Errorf("NDJSON body: no frame carried a model field; body=%q", body)
	}
}

// TestOllama_XCompressionHeader_Chat covers /api/chat, streaming and
// non-streaming, over the compressHeaderCases table.
func TestOllama_XCompressionHeader_Chat(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range compressHeaderCases {
			streamLabel := "non_streaming"
			if streaming {
				streamLabel = "streaming"
			}
			t.Run(tc.name+"/"+streamLabel, func(t *testing.T) {
				eng := &fakeEngine{
					resp: &canonical.ChatResponse{
						Message: canonical.Message{
							Role:    canonical.RoleAssistant,
							Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "hi"}},
						},
						StopReason: canonical.StopEndTurn,
					},
					runChunks: []canonical.Chunk{
						{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "hi"}},
					},
				}
				a := newTestAdapter(eng, nil)

				streamField := `"stream":false`
				if streaming {
					streamField = `"stream":true`
				}
				body := `{"model":"` + compressTestModel + `","messages":[{"role":"user","content":"hi"}],` + streamField + `}`

				headers := map[string]string{}
				if tc.header != "" {
					headers["X-Compression"] = tc.header
				}
				w := doPostWithHeader(t, a, "/chat", body, headers)

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
				if eng.lastReq.Model != "qwen-2.5" {
					t.Errorf("captured req.Model: got %q, want stripped base qwen-2.5", eng.lastReq.Model)
				}

				if streaming {
					assertNDJSONModelEcho(t, w.Body.String(), compressTestModel)
				} else {
					var resp ollamaChatResponse
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

// TestOllama_XCompressionHeader_Generate covers /api/generate, streaming
// and non-streaming, over the compressHeaderCases table.
func TestOllama_XCompressionHeader_Generate(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range compressHeaderCases {
			streamLabel := "non_streaming"
			if streaming {
				streamLabel = "streaming"
			}
			t.Run(tc.name+"/"+streamLabel, func(t *testing.T) {
				eng := &fakeEngine{
					resp: &canonical.ChatResponse{
						Message: canonical.Message{
							Role:    canonical.RoleAssistant,
							Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "blue"}},
						},
						StopReason: canonical.StopEndTurn,
					},
					runChunks: []canonical.Chunk{
						{Kind: canonical.ChunkKindText, Text: &canonical.TextChunk{Content: "blue"}},
					},
				}
				a := newTestAdapter(eng, nil)

				streamField := `"stream":false`
				if streaming {
					streamField = `"stream":true`
				}
				body := `{"model":"` + compressTestModel + `","prompt":"why is the sky blue?",` + streamField + `}`

				headers := map[string]string{}
				if tc.header != "" {
					headers["X-Compression"] = tc.header
				}
				w := doPostWithHeader(t, a, "/generate", body, headers)

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
				if eng.lastReq.Model != "qwen-2.5" {
					t.Errorf("captured req.Model: got %q, want stripped base qwen-2.5", eng.lastReq.Model)
				}

				if streaming {
					assertNDJSONModelEcho(t, w.Body.String(), compressTestModel)
				} else {
					var resp ollamaGenerateResponse
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
