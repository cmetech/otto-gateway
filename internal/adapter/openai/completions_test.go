package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// mountedAdapterForCompletions builds a server with the given fake engine
// for /v1/completions tests.
func mountedAdapterForCompletions(eng Engine) *httptest.Server {
	a := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		Engine: eng,
	})
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	return httptest.NewServer(r)
}

// doCompletions posts body to /v1/completions on the test server and
// returns the response.
func doCompletions(t *testing.T, srv *httptest.Server, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodPost, srv.URL+"/v1/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return resp
}

// TestCompletions covers the main /v1/completions behaviors.
func TestCompletions(t *testing.T) {
	t.Run("basic_string_prompt", func(t *testing.T) {
		eng := &fakeEngine{
			collectResp: &canonical.ChatResponse{
				StopReason: canonical.StopEndTurn,
				Message: canonical.Message{
					Role: canonical.RoleAssistant,
					Content: []canonical.ContentPart{
						{Kind: canonical.ContentKindText, Text: "hello from completions"},
					},
				},
			},
		}
		srv := mountedAdapterForCompletions(eng)
		defer srv.Close()

		resp := doCompletions(t, srv, `{"model":"auto","prompt":"say hi"}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix", ct)
		}

		var tc textCompletion
		if err := json.NewDecoder(resp.Body).Decode(&tc); err != nil {
			t.Fatalf("decode textCompletion: %v", err)
		}

		if tc.Object != "text_completion" {
			t.Errorf("object: got %q, want text_completion", tc.Object)
		}
		if !strings.HasPrefix(tc.ID, "cmpl-") {
			t.Errorf("id: got %q, want cmpl- prefix", tc.ID)
		}
		if len(tc.Choices) != 1 {
			t.Fatalf("choices: got %d, want 1", len(tc.Choices))
		}
		if tc.Choices[0].Text != "hello from completions" {
			t.Errorf("choices[0].text: got %q, want %q", tc.Choices[0].Text, "hello from completions")
		}
		if tc.Choices[0].FinishReason != "stop" {
			t.Errorf("choices[0].finish_reason: got %q, want stop", tc.Choices[0].FinishReason)
		}
		if tc.Choices[0].Logprobs != nil {
			t.Errorf("choices[0].logprobs: got %v, want null", tc.Choices[0].Logprobs)
		}
		if tc.Choices[0].Index != 0 {
			t.Errorf("choices[0].index: got %d, want 0", tc.Choices[0].Index)
		}
		// usage honest zeros
		if tc.Usage.PromptTokens != 0 || tc.Usage.CompletionTokens != 0 || tc.Usage.TotalTokens != 0 {
			t.Errorf("usage: got %+v, want all zeros", tc.Usage)
		}
		if tc.Model != "auto" {
			t.Errorf("model: got %q, want auto", tc.Model)
		}
	})

	t.Run("array_prompt_joined", func(t *testing.T) {
		// prompt as []string — must be joined into one user Message
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

		resp := doCompletions(t, srv, `{"model":"auto","prompt":["hello","world"]}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}

		if receivedReq == nil {
			t.Fatal("engine never received a request")
		}
		// Should have exactly one user Message with joined content
		if len(receivedReq.Messages) != 1 {
			t.Fatalf("messages: got %d, want 1", len(receivedReq.Messages))
		}
		joined := receivedReq.Messages[0].Content[0].Text
		if joined != "hello\nworld" && joined != "hello world" {
			t.Errorf("joined prompt: got %q, want 'hello\\nworld' or 'hello world'", joined)
		}
		if receivedReq.Messages[0].Role != canonical.RoleUser {
			t.Errorf("role: got %v, want RoleUser", receivedReq.Messages[0].Role)
		}
	})

	t.Run("ignored_params_no_400", func(t *testing.T) {
		// logprobs, echo, suffix, best_of, n, max_tokens must NOT cause 400
		eng := &fakeEngine{
			collectResp: &canonical.ChatResponse{
				StopReason: canonical.StopEndTurn,
				Message: canonical.Message{
					Role:    canonical.RoleAssistant,
					Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ok"}},
				},
			},
		}
		srv := mountedAdapterForCompletions(eng)
		defer srv.Close()

		body := `{
			"model":"auto",
			"prompt":"hi",
			"logprobs":5,
			"echo":true,
			"suffix":".",
			"best_of":3,
			"n":2,
			"max_tokens":100
		}`
		resp := doCompletions(t, srv, body)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status: got %d, want 200 (advanced params must not 400); body=%s",
				resp.StatusCode, raw)
		}
	})

	t.Run("stream_true_downgrade_to_json", func(t *testing.T) {
		// stream:true must be silently downgraded — response is JSON, NOT SSE
		eng := &fakeEngine{
			collectResp: &canonical.ChatResponse{
				StopReason: canonical.StopEndTurn,
				Message: canonical.Message{
					Role:    canonical.RoleAssistant,
					Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "pong"}},
				},
			},
		}
		srv := mountedAdapterForCompletions(eng)
		defer srv.Close()

		resp := doCompletions(t, srv, `{"model":"auto","prompt":"hi","stream":true}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}

		ct := resp.Header.Get("Content-Type")
		// MUST be JSON, NOT text/event-stream (D-03 JSON-only shim)
		if strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("stream:true must be downgraded to JSON; got Content-Type: %q", ct)
		}
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix", ct)
		}

		// Must be decodable as textCompletion (not SSE frames)
		var tc textCompletion
		if err := json.NewDecoder(resp.Body).Decode(&tc); err != nil {
			t.Fatalf("decode textCompletion from stream:true request: %v", err)
		}
		if tc.Object != "text_completion" {
			t.Errorf("object: got %q, want text_completion", tc.Object)
		}
	})

	t.Run("nil_engine_503", func(t *testing.T) {
		a := New(Config{}) // nil engine
		r := chi.NewRouter()
		r.Route("/v1", func(sub chi.Router) {
			a.RegisterRoutes(sub)
		})
		srv := httptest.NewServer(r)
		defer srv.Close()

		resp := doCompletions(t, srv, `{"model":"auto","prompt":"hi"}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status: got %d, want 503", resp.StatusCode)
		}
	})

	t.Run("empty_prompt_400", func(t *testing.T) {
		eng := &fakeEngine{}
		srv := mountedAdapterForCompletions(eng)
		defer srv.Close()

		// empty string prompt
		resp := doCompletions(t, srv, `{"model":"auto","prompt":""}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status: got %d, want 400 (empty prompt)", resp.StatusCode)
		}
	})

	t.Run("oversize_body_413", func(t *testing.T) {
		eng := &fakeEngine{}
		srv := mountedAdapterForCompletions(eng)
		defer srv.Close()

		big := make([]byte, int(chatBodyCap)+1024)
		for i := range big {
			big[i] = 'x'
		}
		body := append([]byte(`{"model":"auto","prompt":"`), big...)
		body = append(body, []byte(`"}`)...)

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost, srv.URL+"/v1/completions", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("status: got %d, want 413", resp.StatusCode)
		}
	})

	t.Run("engine_error_500_no_echo", func(t *testing.T) {
		// T-02-33: engine error must produce 500 with generic message,
		// never echoing the raw err.Error()
		secretErr := errors.New("SECRET_INTERNAL_ERROR_DO_NOT_EXPOSE_xzy123")
		eng := &fakeEngine{
			collectErr: secretErr,
		}
		srv := mountedAdapterForCompletions(eng)
		defer srv.Close()

		resp := doCompletions(t, srv, `{"model":"auto","prompt":"trigger error"}`)
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("status: got %d, want 500", resp.StatusCode)
		}

		raw, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(raw), "SECRET_INTERNAL_ERROR_DO_NOT_EXPOSE_xzy123") {
			t.Errorf("T-02-33 violated: engine error echoed in response body: %s", raw)
		}
	})
}

// TestTextCompletionRender covers the render shapes for text_completion.
func TestTextCompletionRender(t *testing.T) {
	t.Run("basic_shape", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "answer text"},
				},
			},
		}
		out := chatResponseToTextCompletion(resp, "auto")

		if out.Object != "text_completion" {
			t.Errorf("object: got %q, want text_completion", out.Object)
		}
		if !strings.HasPrefix(out.ID, "cmpl-") {
			t.Errorf("id: got %q, want cmpl- prefix", out.ID)
		}
		if len(out.Choices) != 1 {
			t.Fatalf("choices: got %d, want 1", len(out.Choices))
		}
		if out.Choices[0].Text != "answer text" {
			t.Errorf("choices[0].text: got %q, want answer text", out.Choices[0].Text)
		}
		if out.Choices[0].FinishReason != "stop" {
			t.Errorf("choices[0].finish_reason: got %q, want stop", out.Choices[0].FinishReason)
		}
		if out.Choices[0].Logprobs != nil {
			t.Errorf("choices[0].logprobs: got %v, want null", out.Choices[0].Logprobs)
		}
		if out.Usage.PromptTokens != 0 || out.Usage.CompletionTokens != 0 || out.Usage.TotalTokens != 0 {
			t.Errorf("usage: got %+v, want all zeros", out.Usage)
		}
	})

	t.Run("joined_multi_part_text", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopEndTurn,
			Message: canonical.Message{
				Role: canonical.RoleAssistant,
				Content: []canonical.ContentPart{
					{Kind: canonical.ContentKindText, Text: "part1 "},
					{Kind: canonical.ContentKindText, Text: "part2"},
				},
			},
		}
		out := chatResponseToTextCompletion(resp, "gpt-3.5-turbo-instruct")
		if out.Choices[0].Text != "part1 part2" {
			t.Errorf("text: got %q, want 'part1 part2'", out.Choices[0].Text)
		}
		if out.Model != "gpt-3.5-turbo-instruct" {
			t.Errorf("model: got %q, want gpt-3.5-turbo-instruct", out.Model)
		}
	})

	t.Run("max_tokens_finish_reason", func(t *testing.T) {
		resp := &canonical.ChatResponse{
			StopReason: canonical.StopMaxTokens,
			Message:    canonical.Message{Role: canonical.RoleAssistant},
		}
		out := chatResponseToTextCompletion(resp, "auto")
		if out.Choices[0].FinishReason != "length" {
			t.Errorf("finish_reason: got %q, want length", out.Choices[0].FinishReason)
		}
	})

	t.Run("nil_response_safe", func(t *testing.T) {
		// nil resp must not panic — defensive handling
		out := chatResponseToTextCompletion(nil, "auto")
		if out.Object != "text_completion" {
			t.Errorf("object: got %q, want text_completion", out.Object)
		}
		if len(out.Choices) != 1 {
			t.Fatalf("choices: got %d, want 1", len(out.Choices))
		}
	})
}

// TestPromptDecode covers promptToCanonicalMessage: string/array/empty handling.
func TestPromptDecode(t *testing.T) {
	t.Run("string_prompt", func(t *testing.T) {
		req := &completionWireRequest{
			Model:  "auto",
			Prompt: jsonRaw(`"hello world"`),
		}
		msgs, err := promptToMessages(req.Prompt)
		if err != nil {
			t.Fatalf("promptToMessages: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("messages: got %d, want 1", len(msgs))
		}
		if msgs[0].Content[0].Text != "hello world" {
			t.Errorf("text: got %q, want hello world", msgs[0].Content[0].Text)
		}
		if msgs[0].Role != canonical.RoleUser {
			t.Errorf("role: got %v, want RoleUser", msgs[0].Role)
		}
	})

	t.Run("array_prompt", func(t *testing.T) {
		req := &completionWireRequest{
			Model:  "auto",
			Prompt: jsonRaw(`["line1","line2"]`),
		}
		msgs, err := promptToMessages(req.Prompt)
		if err != nil {
			t.Fatalf("promptToMessages: %v", err)
		}
		if len(msgs) != 1 {
			t.Fatalf("messages: got %d, want 1", len(msgs))
		}
		text := msgs[0].Content[0].Text
		if text != "line1\nline2" && text != "line1 line2" {
			t.Errorf("joined: got %q, want 'line1\\nline2' or 'line1 line2'", text)
		}
	})

	t.Run("empty_string_prompt_error", func(t *testing.T) {
		req := &completionWireRequest{
			Model:  "auto",
			Prompt: jsonRaw(`""`),
		}
		_, err := promptToMessages(req.Prompt)
		if err == nil {
			t.Error("empty prompt should return an error (→ 400)")
		}
	})

	t.Run("nil_prompt_error", func(t *testing.T) {
		req := &completionWireRequest{
			Model:  "auto",
			Prompt: nil,
		}
		_, err := promptToMessages(req.Prompt)
		if err == nil {
			t.Error("nil prompt should return an error (→ 400)")
		}
	})
}

// ----------------------------------------------------------------------------
// captureEngine records the request passed to Collect.
// Used to verify prompt→Message mapping.
// ----------------------------------------------------------------------------

type captureEngine struct {
	collectResp *canonical.ChatResponse
	collectErr  error
	captureReq  **canonical.ChatRequest
	// lastCtx captures the ctx Collect received so Task 9
	// X-Compression header-stamp tests can observe
	// compress.HeaderDirectiveFromContext(lastCtx).
	lastCtx context.Context
}

func (c *captureEngine) Collect(ctx context.Context, req *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	c.lastCtx = ctx
	if c.captureReq != nil {
		*c.captureReq = req
	}
	return c.collectResp, c.collectErr
}

func (c *captureEngine) Run(_ context.Context, _ *canonical.ChatRequest) (RunHandle, error) {
	return nil, errors.New("captureEngine: Run not expected")
}

// RunPostHooks is a no-op — completions tests only verify the
// non-streaming /completions path which goes through Collect, not Run.
func (c *captureEngine) RunPostHooks(_ context.Context, _ *canonical.ChatRequest, _ *canonical.ChatResponse) error {
	return nil
}

// CollectFromRun is unused in completions tests (T-5b interface).
func (c *captureEngine) CollectFromRun(_ context.Context, _ RunHandle, _ *canonical.ChatRequest) (*canonical.ChatResponse, error) {
	return c.collectResp, c.collectErr
}
