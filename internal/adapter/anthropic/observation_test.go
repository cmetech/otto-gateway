package anthropic

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/canonical"
	"otto-gateway/internal/session"
)

func successfulAnthropicResponse() *canonical.ChatResponse {
	return &canonical.ChatResponse{
		StopReason: canonical.StopEndTurn,
		Message: canonical.Message{
			Role: canonical.RoleAssistant,
			Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: "ok",
			}},
		},
	}
}

func requestObservationAdapter(eng Engine, extra func(*Config)) (*Adapter, *[]RequestObservation) {
	observations := make([]RequestObservation, 0, 1)
	cfg := Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engine: eng,
		ObserveRequest: func(observation RequestObservation) {
			observations = append(observations, observation)
		},
	}
	if extra != nil {
		extra(&cfg)
	}
	return New(cfg), &observations
}

func invokeAnthropicHandler(t *testing.T, adapter *Adapter, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/messages", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("anthropic-version", "2023-06-01")
	recorder := httptest.NewRecorder()
	adapter.ProtectedRouter().ServeHTTP(recorder, request)
	return recorder
}

func assertAnthropicObservation(t *testing.T, observations []RequestObservation, want RequestObservation) {
	t.Helper()
	if len(observations) != 1 {
		t.Fatalf("observations = %+v, want exactly one", observations)
	}
	if observations[0] != want {
		t.Errorf("observation = %+v, want %+v", observations[0], want)
	}
}

func TestRequestObservation_InvalidRequest(t *testing.T) {
	adapter, observations := requestObservationAdapter(&fakeEngine{}, nil)
	invokeAnthropicHandler(t, adapter, `{`)
	assertAnthropicObservation(t, *observations, RequestObservation{
		Outcome: "invalid_request", Stream: "unknown", SessionMode: "unknown",
	})
}

func TestRequestObservation_NonStreamingSuccess(t *testing.T) {
	adapter, observations := requestObservationAdapter(
		&fakeEngine{collectResp: successfulAnthropicResponse()}, nil,
	)
	invokeAnthropicHandler(t, adapter,
		`{"model":"auto","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	assertAnthropicObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "false", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingSuccess(t *testing.T) {
	adapter, observations := requestObservationAdapter(
		&fakeEngine{collectResp: successfulAnthropicResponse()}, nil,
	)
	invokeAnthropicHandler(t, adapter,
		`{"model":"auto","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	assertAnthropicObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "true", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingIdleTimeoutAfterHeaders(t *testing.T) {
	chunks := make(chan canonical.Chunk)
	eng := &fakeEngine{runHandle: &fakeRunHandle{
		stream:    &fakeStream{chunks: chunks},
		sessionID: "idle-session",
	}}
	adapter, observations := requestObservationAdapter(eng, func(cfg *Config) {
		cfg.StreamIdleTimeout = time.Millisecond
	})
	recorder := invokeAnthropicHandler(t, adapter,
		`{"model":"auto","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want committed streaming 200", recorder.Code)
	}
	assertAnthropicObservation(t, *observations, RequestObservation{
		Outcome: "stream_idle_timeout", Stream: "true", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingWriteFailureIsInternal(t *testing.T) {
	err := errors.New("anthropic: write content_block_delta: broken pipe")
	if got := classifyStreamingError(err); got != "internal_error" {
		t.Errorf("classifyStreamingError = %q, want internal_error", got)
	}
}

func TestRequestObservation_EngineFailures(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome string
	}{
		{name: "pool exhausted", err: canonical.ErrPoolExhausted, outcome: "pool_exhausted"},
		{name: "idle timeout", err: canonical.ErrStreamIdleTimeout, outcome: "stream_idle_timeout"},
		{name: "client cancelled", err: context.Canceled, outcome: "client_cancelled"},
		{name: "upstream", err: errors.New("kiro failed"), outcome: "upstream_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, observations := requestObservationAdapter(&fakeEngine{collectErr: test.err}, nil)
			invokeAnthropicHandler(t, adapter,
				`{"model":"auto","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
			assertAnthropicObservation(t, *observations, RequestObservation{
				Outcome: test.outcome, Stream: "false", SessionMode: "stateless",
			})
		})
	}
}

func TestRequestObservation_Authentication(t *testing.T) {
	response := successfulAnthropicResponse()
	response.StopReason = canonical.StopError
	adapter, observations := requestObservationAdapter(&fakeEngine{collectResp: response}, nil)
	invokeAnthropicHandler(t, adapter,
		`{"model":"auto","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
	assertAnthropicObservation(t, *observations, RequestObservation{
		Outcome: "authentication", Stream: "false", SessionMode: "stateless",
	})
}

func TestRequestObservation_StatefulSession(t *testing.T) {
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-observed")
	registry := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: &fakeEngine{collectResp: successfulAnthropicResponse()}}
	adapter, observations := requestObservationAdapter(&fakeEngine{}, func(cfg *Config) {
		cfg.Registry = registry
		cfg.EngineForSession = func(*session.Entry) Engine { return sessionEng }
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/messages",
		strings.NewReader(`{"model":"auto","max_tokens":128,"messages":[{"role":"user","content":"hello"}],"stream":false}`))
	request.Header.Set("anthropic-version", "2023-06-01")
	request.Header.Set("X-Session-Id", "sid-observed")
	adapter.ProtectedRouter().ServeHTTP(httptest.NewRecorder(), request)
	assertAnthropicObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "false", SessionMode: "stateful",
	})
}
