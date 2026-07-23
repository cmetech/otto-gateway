package openai

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

func successfulOpenAIResponse() *canonical.ChatResponse {
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

func requestObservationsAdapter(eng Engine) (*Adapter, *[]RequestObservation) {
	observations := make([]RequestObservation, 0, 1)
	adapter := New(Config{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engine: eng,
		ObserveRequest: func(observation RequestObservation) {
			observations = append(observations, observation)
		},
	})
	return adapter, &observations
}

func invokeOpenAIHandler(t *testing.T, adapter *Adapter, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	switch path {
	case "/v1/chat/completions":
		adapter.handleChatCompletions(recorder, request)
	case "/v1/completions":
		adapter.handleCompletions(recorder, request)
	default:
		t.Fatalf("unsupported test path %q", path)
	}
	return recorder
}

func assertOpenAIObservation(t *testing.T, observations []RequestObservation, want RequestObservation) {
	t.Helper()
	if len(observations) != 1 {
		t.Fatalf("observations = %+v, want exactly one", observations)
	}
	if observations[0] != want {
		t.Errorf("observation = %+v, want %+v", observations[0], want)
	}
}

func TestRequestObservation_InvalidRequest(t *testing.T) {
	adapter, observations := requestObservationsAdapter(&fakeEngine{})
	invokeOpenAIHandler(t, adapter, "/v1/chat/completions", `{`)
	assertOpenAIObservation(t, *observations, RequestObservation{
		Outcome: "invalid_request", Stream: "unknown", SessionMode: "unknown",
	})
}

func TestRequestObservation_NonStreamingSuccess(t *testing.T) {
	adapter, observations := requestObservationsAdapter(&fakeEngine{collectResp: successfulOpenAIResponse()})
	invokeOpenAIHandler(t, adapter, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	assertOpenAIObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "false", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingSuccess(t *testing.T) {
	adapter, observations := requestObservationsAdapter(&fakeEngine{
		runFinal: &canonical.FinalResult{StopReason: canonical.StopEndTurn},
	})
	invokeOpenAIHandler(t, adapter, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	assertOpenAIObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "true", SessionMode: "stateless",
	})
}

func TestRequestObservation_StreamingIdleTimeoutAfterHeaders(t *testing.T) {
	observations := make([]RequestObservation, 0, 1)
	adapter := New(Config{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engine:            &fakeEngine{keepRunOpen: true},
		StreamIdleTimeout: time.Millisecond,
		ObserveRequest: func(observation RequestObservation) {
			observations = append(observations, observation)
		},
	})
	recorder := invokeOpenAIHandler(t, adapter, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want committed streaming 200", recorder.Code)
	}
	assertOpenAIObservation(t, observations, RequestObservation{
		Outcome: "stream_idle_timeout", Stream: "true", SessionMode: "stateless",
	})
}

func TestRequestObservation_StatefulSession(t *testing.T) {
	entry := session.NewEntryForTest(fakeACPClient{}, "sid-observed")
	registry := &fakeSessionRegistry{entry: entry}
	sessionEng := &sessionEngine{inner: &fakeEngine{collectResp: successfulOpenAIResponse()}}
	observations := make([]RequestObservation, 0, 1)
	adapter := New(Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Engine:   &fakeEngine{},
		Registry: registry,
		EngineForSession: func(*session.Entry) Engine {
			return sessionEng
		},
		ObserveRequest: func(observation RequestObservation) {
			observations = append(observations, observation)
		},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`))
	request.Header.Set("X-Session-Id", "sid-observed")
	adapter.handleChatCompletions(httptest.NewRecorder(), request)
	assertOpenAIObservation(t, observations, RequestObservation{
		Outcome: "success", Stream: "false", SessionMode: "stateful",
	})
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
			adapter, observations := requestObservationsAdapter(&fakeEngine{collectErr: test.err})
			invokeOpenAIHandler(t, adapter, "/v1/chat/completions",
				`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`)
			assertOpenAIObservation(t, *observations, RequestObservation{
				Outcome: test.outcome, Stream: "false", SessionMode: "stateless",
			})
		})
	}
}

func TestRequestObservation_Authentication(t *testing.T) {
	adapter, observations := requestObservationsAdapter(&fakeEngine{
		collectResp: &canonical.ChatResponse{
			StopReason: canonical.StopError,
			Message: canonical.Message{Content: []canonical.ContentPart{{
				Kind: canonical.ContentKindText,
				Text: "unauthorized",
			}}},
		},
	})
	invokeOpenAIHandler(t, adapter, "/v1/chat/completions",
		`{"model":"auto","messages":[{"role":"user","content":"hello"}],"stream":false}`)
	assertOpenAIObservation(t, *observations, RequestObservation{
		Outcome: "authentication", Stream: "false", SessionMode: "stateless",
	})
}

func TestRequestObservation_CompletionsSuccess(t *testing.T) {
	adapter, observations := requestObservationsAdapter(&fakeEngine{collectResp: successfulOpenAIResponse()})
	invokeOpenAIHandler(t, adapter, "/v1/completions", `{"model":"auto","prompt":"hello","stream":true}`)
	assertOpenAIObservation(t, *observations, RequestObservation{
		Outcome: "success", Stream: "false", SessionMode: "stateless",
	})
}
