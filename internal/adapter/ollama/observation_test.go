package ollama

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

func successfulOllamaResponse() *canonical.ChatResponse {
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

func invokeOllamaHandler(t *testing.T, adapter *Adapter, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	adapter.ProtectedRouter().ServeHTTP(recorder, request)
	return recorder
}

func assertOllamaObservation(t *testing.T, observations []RequestObservation, want RequestObservation) {
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
	invokeOllamaHandler(t, adapter, "/chat", `{`)
	assertOllamaObservation(t, *observations, RequestObservation{
		Outcome: "invalid_request", Stream: "unknown", SessionMode: "unknown",
	})
}

func TestRequestObservation_ChatAndGenerateSuccess(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat",
			path: "/chat",
			body: `{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`,
		},
		{
			name: "generate",
			path: "/generate",
			body: `{"model":"auto","prompt":"hello","stream":false}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, observations := requestObservationAdapter(
				&fakeEngine{resp: successfulOllamaResponse()}, nil,
			)
			invokeOllamaHandler(t, adapter, test.path, test.body)
			assertOllamaObservation(t, *observations, RequestObservation{
				Outcome: "success", Stream: "false", SessionMode: "stateless",
			})
		})
	}
}

func TestRequestObservation_StreamingSuccess(t *testing.T) {
	adapter, observations := requestObservationAdapter(&fakeEngine{}, nil)
	invokeOllamaHandler(t, adapter, "/chat",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	assertOllamaObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "true", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingIdleTimeoutAfterHeaders(t *testing.T) {
	chunks := make(chan canonical.Chunk)
	runHandle := &fakeRunHandle{
		stream:    &fakeStream{ch: chunks},
		sessionID: "idle-session",
	}
	adapter, observations := requestObservationAdapter(&fakeEngine{runHandle: runHandle}, func(cfg *Config) {
		cfg.StreamIdleTimeout = time.Millisecond
	})
	recorder := invokeOllamaHandler(t, adapter, "/chat",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want committed streaming 200", recorder.Code)
	}
	assertOllamaObservation(t, *observations, RequestObservation{
		Outcome: "stream_idle_timeout", Stream: "true", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingWriteFailureIsInternal(t *testing.T) {
	err := errors.New("ollama: ndjson write chunk: broken pipe")
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
			adapter, observations := requestObservationAdapter(&fakeEngine{err: test.err}, nil)
			invokeOllamaHandler(t, adapter, "/chat",
				`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			assertOllamaObservation(t, *observations, RequestObservation{
				Outcome: test.outcome, Stream: "false", SessionMode: "stateless",
			})
		})
	}
}

func TestRequestObservation_Authentication(t *testing.T) {
	response := successfulOllamaResponse()
	response.StopReason = canonical.StopError
	adapter, observations := requestObservationAdapter(&fakeEngine{resp: response}, nil)
	invokeOllamaHandler(t, adapter, "/chat",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	assertOllamaObservation(t, *observations, RequestObservation{
		Outcome: "authentication", Stream: "false", SessionMode: "stateless",
	})
}

func TestRequestObservation_StatefulSession(t *testing.T) {
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-observed")
	registry := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: &fakeEngine{resp: successfulOllamaResponse()}}
	adapter, observations := requestObservationAdapter(&fakeEngine{}, func(cfg *Config) {
		cfg.Registry = registry
		cfg.EngineForSession = func(*session.Entry) Engine { return sessionEng }
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/chat",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`))
	request.Header.Set("X-Session-Id", "sid-observed")
	adapter.ProtectedRouter().ServeHTTP(httptest.NewRecorder(), request)
	assertOllamaObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "false", SessionMode: "stateful",
	})
}
