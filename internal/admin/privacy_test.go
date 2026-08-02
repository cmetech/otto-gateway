package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const privacyTriageTestToken = "task-13-triage-token"

type stubPrivacyTriage struct {
	scopes   []PrivacyScopeRow
	mappings map[string][]PrivacyMappingRow
	inspect  error
	clear    map[string]PrivacyClearResult
	clearErr error
	clearAll PrivacyClearResult

	inspectedIDs  []string
	clearedIDs    []string
	clearAllCalls int
}

func (s *stubPrivacyTriage) ListPrivacyScopes() []PrivacyScopeRow {
	return append([]PrivacyScopeRow(nil), s.scopes...)
}

func (s *stubPrivacyTriage) InspectPrivacyScope(scopeID string) ([]PrivacyMappingRow, error) {
	s.inspectedIDs = append(s.inspectedIDs, scopeID)
	if s.inspect != nil {
		return nil, s.inspect
	}
	return append([]PrivacyMappingRow(nil), s.mappings[scopeID]...), nil
}

func (s *stubPrivacyTriage) ClearPrivacyScope(scopeID string) (PrivacyClearResult, error) {
	s.clearedIDs = append(s.clearedIDs, scopeID)
	if s.clearErr != nil {
		return PrivacyClearResult{}, s.clearErr
	}
	return s.clear[scopeID], nil
}

func (s *stubPrivacyTriage) ClearAllPrivacyScopes() PrivacyClearResult {
	s.clearAllCalls++
	return s.clearAll
}

func privacyTriageRequest(t *testing.T, h http.Handler, method, path, remoteAddr, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
	req.RemoteAddr = remoteAddr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertPrivacyTriageHeaders(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control=%q, want no-store", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("Access-Control-Allow-Origin=%q, want absent", got)
	}
}

func TestPrivacyTriageDisabledDoesNotRegisterRoutes(t *testing.T) {
	h := Handler(Deps{
		PrivacyTriage:        &stubPrivacyTriage{},
		PrivacyTriageToken:   privacyTriageTestToken,
		PrivacyTriageEnabled: false,
	})

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/privacy/scopes"},
		{http.MethodGet, "/api/privacy/scopes/run-1/mapping"},
		{http.MethodDelete, "/api/privacy/scopes/run-1"},
		{http.MethodDelete, "/api/privacy/scopes"},
	} {
		rec := privacyTriageRequest(t, h, request.method, request.path, "127.0.0.1:43120", privacyTriageTestToken)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s=%d, want 404", request.method, request.path, rec.Code)
		}
	}
}

func TestPrivacyTriageEnabledRequiresActualLoopbackPeerAndSeparateBearer(t *testing.T) {
	now := time.Date(2026, 8, 1, 14, 30, 0, 0, time.UTC)
	source := &stubPrivacyTriage{
		scopes: []PrivacyScopeRow{{
			ID: "run-1", Profile: "strict", State: "active", Entries: 1,
			CreatedAt: now, LastUsedAt: now, ExpiresAt: now.Add(time.Hour),
		}},
		mappings: map[string][]PrivacyMappingRow{
			"run-1": {{Entity: "IPv4", Original: "10.0.0.1", Synthetic: "192.0.2.1", Provenance: "input", CreatedAt: now}},
		},
	}
	h := Handler(Deps{
		PrivacyTriage:        source,
		PrivacyTriageToken:   privacyTriageTestToken,
		PrivacyTriageEnabled: true,
	})

	for _, remoteAddr := range []string{"127.0.0.1:43120", "[::1]:43120", "[::ffff:127.0.0.1]:43120"} {
		rec := privacyTriageRequest(t, h, http.MethodGet, "/api/privacy/scopes", remoteAddr, privacyTriageTestToken)
		if rec.Code != http.StatusOK {
			t.Errorf("loopback peer %q status=%d, want 200; body=%s", remoteAddr, rec.Code, rec.Body.String())
		}
		assertPrivacyTriageHeaders(t, rec)
	}

	for _, tc := range []struct {
		name       string
		remoteAddr string
		token      string
		wantStatus int
	}{
		{"non-loopback", "192.0.2.10:43120", privacyTriageTestToken, http.StatusForbidden},
		{"malformed-peer", "localhost", privacyTriageTestToken, http.StatusForbidden},
		{"missing-token", "127.0.0.1:43120", "", http.StatusUnauthorized},
		{"wrong-token", "127.0.0.1:43120", "wrong-token", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/privacy/scopes", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("Forwarded", "for=127.0.0.1")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status=%d, want %d; body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			assertPrivacyTriageHeaders(t, rec)
		})
	}
}

func TestPrivacyTriageDeniedSingleScopeDeleteRecordsClearOperation(t *testing.T) {
	var events []string
	h := Handler(Deps{
		PrivacyTriage:        &stubPrivacyTriage{},
		PrivacyTriageToken:   privacyTriageTestToken,
		PrivacyTriageEnabled: true,
		PrivacyTriageObserver: func(operation, result string) {
			events = append(events, operation+":"+result)
		},
	})

	rec := privacyTriageRequest(t, h, http.MethodDelete, "/api/privacy/scopes/run-1", "127.0.0.1:43120", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if fmt.Sprint(events) != "[clear:denied]" {
		t.Fatalf("triage events=%v, want [clear:denied]", events)
	}
}

func TestPrivacyTriageEnabledRegistersOnlySpecifiedOperations(t *testing.T) {
	source := &stubPrivacyTriage{mappings: map[string][]PrivacyMappingRow{"run-1": {}}}
	h := Handler(Deps{
		PrivacyTriage:        source,
		PrivacyTriageToken:   privacyTriageTestToken,
		PrivacyTriageEnabled: true,
	})

	for _, request := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/privacy/scopes"},
		{http.MethodGet, "/api/privacy/scopes/run-1/mapping"},
	} {
		rec := privacyTriageRequest(t, h, request.method, request.path, "127.0.0.1:43120", privacyTriageTestToken)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s=%d, want 200; body=%s", request.method, request.path, rec.Code, rec.Body.String())
		}
		assertPrivacyTriageHeaders(t, rec)
	}

	for _, path := range []string{
		"/api/privacy",
		"/api/privacy/scopes/run-1",
		"/api/privacy/scopes/run-1/extra",
		"/api/privacy/mapping",
	} {
		rec := privacyTriageRequest(t, h, http.MethodGet, path, "127.0.0.1:43120", privacyTriageTestToken)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s=%d, want 404 or 405", path, rec.Code)
		}
	}
}

func TestPrivacyTriageInspectUnescapesAndValidatesScopeOnce(t *testing.T) {
	now := time.Date(2026, 8, 1, 15, 0, 0, 0, time.UTC)
	want := []PrivacyMappingRow{{
		Entity: "IPv4", Original: "10.23.45.67", Synthetic: "192.0.2.44",
		Provenance: "input", CreatedAt: now,
	}}
	source := &stubPrivacyTriage{mappings: map[string][]PrivacyMappingRow{"run-1": want}}
	h := Handler(Deps{PrivacyTriage: source, PrivacyTriageEnabled: true, PrivacyTriageToken: privacyTriageTestToken})

	rec := privacyTriageRequest(t, h, http.MethodGet, "/api/privacy/scopes/run%2D1/mapping", "127.0.0.1:43120", privacyTriageTestToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []PrivacyMappingRow
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode inspect response: %v", err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("inspect response=%+v, want %+v", got, want)
	}
	if fmt.Sprint(source.inspectedIDs) != "[run-1]" {
		t.Fatalf("inspected IDs=%v, want [run-1]", source.inspectedIDs)
	}

	for _, path := range []string{
		"/api/privacy/scopes/%252F/mapping",
		"/api/privacy/scopes/run%252F1/mapping",
		"/api/privacy/scopes/run%25251/mapping",
	} {
		rec = privacyTriageRequest(t, h, http.MethodGet, path, "127.0.0.1:43120", privacyTriageTestToken)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != "{\"error\":\"invalid_scope\"}\n" {
			t.Errorf("GET %s = (%d,%q), want stable invalid-scope 400", path, rec.Code, rec.Body.String())
		}
		assertPrivacyTriageHeaders(t, rec)
	}
	if len(source.inspectedIDs) != 1 {
		t.Fatalf("invalid scope reached source: inspected IDs=%v", source.inspectedIDs)
	}
}

func TestPrivacyTriageClearLifecycleAndConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     PrivacyClearResult
		err        error
		wantStatus int
		wantBody   string
	}{
		{"inactive", PrivacyClearResult{State: "wiped"}, nil, http.StatusNoContent, ""},
		{"active", PrivacyClearResult{State: "closing"}, nil, http.StatusAccepted, "{\"state\":\"closing\"}\n"},
		{"missing", PrivacyClearResult{}, errors.New("scope-private-canary missing"), http.StatusNotFound, "{\"error\":\"scope_not_found\"}\n"},
		{"repeated", PrivacyClearResult{State: "wiped"}, nil, http.StatusNoContent, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &stubPrivacyTriage{clear: map[string]PrivacyClearResult{"run-1": tc.result}, clearErr: tc.err}
			h := Handler(Deps{PrivacyTriage: source, PrivacyTriageEnabled: true, PrivacyTriageToken: privacyTriageTestToken})
			rec := privacyTriageRequest(t, h, http.MethodDelete, "/api/privacy/scopes/run-1", "127.0.0.1:43120", privacyTriageTestToken)
			if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
				t.Fatalf("clear=(%d,%q), want (%d,%q)", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			assertPrivacyTriageHeaders(t, rec)
		})
	}

	for _, tc := range []struct {
		name       string
		confirm    string
		result     PrivacyClearResult
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{"missing-confirm", "", PrivacyClearResult{State: "wiped"}, http.StatusBadRequest, "{\"error\":\"confirmation_required\"}\n", 0},
		{"inexact-confirm", "CLEAR-ALL", PrivacyClearResult{State: "wiped"}, http.StatusBadRequest, "{\"error\":\"confirmation_required\"}\n", 0},
		{"wiped", "clear-all", PrivacyClearResult{State: "wiped"}, http.StatusNoContent, "", 1},
		{"closing", "clear-all", PrivacyClearResult{State: "closing"}, http.StatusAccepted, "{\"state\":\"closing\"}\n", 1},
	} {
		t.Run("clear-all-"+tc.name, func(t *testing.T) {
			source := &stubPrivacyTriage{clearAll: tc.result}
			h := Handler(Deps{PrivacyTriage: source, PrivacyTriageEnabled: true, PrivacyTriageToken: privacyTriageTestToken})
			req := httptest.NewRequestWithContext(context.Background(), http.MethodDelete, "/api/privacy/scopes", nil)
			req.RemoteAddr = "127.0.0.1:43120"
			req.Header.Set("Authorization", "Bearer "+privacyTriageTestToken)
			if tc.confirm != "" {
				req.Header.Set("X-GW-Privacy-Confirm", tc.confirm)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || rec.Body.String() != tc.wantBody {
				t.Fatalf("clear all=(%d,%q), want (%d,%q)", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if source.clearAllCalls != tc.wantCalls {
				t.Fatalf("ClearAll calls=%d, want %d", source.clearAllCalls, tc.wantCalls)
			}
			assertPrivacyTriageHeaders(t, rec)
		})
	}
}

func TestPrivacyTriageFailureAuditAndMetricsDoNotLeakCanaries(t *testing.T) {
	const (
		scopeCanary     = "private-scope-canary"
		originalCanary  = "original-value-canary"
		syntheticCanary = "synthetic-value-canary"
		tokenCanary     = "bearer-token-canary"
	)
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	var metricEvents []string
	source := &stubPrivacyTriage{inspect: errors.New(originalCanary + " " + syntheticCanary)}
	h := Handler(Deps{
		Logger: logger, PrivacyTriage: source, PrivacyTriageEnabled: true, PrivacyTriageToken: tokenCanary,
		PrivacyTriageObserver: func(operation, result string) {
			metricEvents = append(metricEvents, operation+":"+result)
		},
	})

	rec := privacyTriageRequest(t, h, http.MethodGet, "/api/privacy/scopes/"+scopeCanary+"/mapping", "127.0.0.1:43120", tokenCanary)
	if rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"scope_not_found\"}\n" {
		t.Fatalf("inspect failure=(%d,%q), want stable 404", rec.Code, rec.Body.String())
	}
	combined := rec.Body.String() + logs.String() + strings.Join(metricEvents, ",")
	for _, canary := range []string{scopeCanary, originalCanary, syntheticCanary, tokenCanary} {
		if strings.Contains(combined, canary) {
			t.Errorf("ordinary triage output leaked %q: %s", canary, combined)
		}
	}
	if !strings.Contains(logs.String(), `"operation":"inspect"`) ||
		!strings.Contains(logs.String(), `"result":"failed"`) ||
		!strings.Contains(logs.String(), `"peer":"127.0.0.1"`) ||
		!strings.Contains(logs.String(), `"scope_hmac":"`) {
		t.Errorf("safe audit fields missing: %s", logs.String())
	}
	if fmt.Sprint(metricEvents) != "[inspect:failed]" {
		t.Errorf("metric events=%v, want [inspect:failed]", metricEvents)
	}
}

func TestPrivacyTriageBoundsJSONResponses(t *testing.T) {
	source := &stubPrivacyTriage{scopes: make([]PrivacyScopeRow, privacyTriageMaxScopes+1)}
	h := Handler(Deps{PrivacyTriage: source, PrivacyTriageEnabled: true, PrivacyTriageToken: privacyTriageTestToken})
	rec := privacyTriageRequest(t, h, http.MethodGet, "/api/privacy/scopes", "127.0.0.1:43120", privacyTriageTestToken)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"response_too_large\"}\n" {
		t.Fatalf("oversized list=(%d,%q), want bounded stable 500", rec.Code, rec.Body.String())
	}
}

func TestPrivacyTriageOversizedMappingRecordsOneFailedMetric(t *testing.T) {
	var events []string
	source := &stubPrivacyTriage{mappings: map[string][]PrivacyMappingRow{
		"run-1": {{Entity: "IPv4", Original: strings.Repeat("x", privacyTriageMaxJSON), Synthetic: "192.0.2.1"}},
	}}
	h := Handler(Deps{
		PrivacyTriage: source, PrivacyTriageEnabled: true, PrivacyTriageToken: privacyTriageTestToken,
		PrivacyTriageObserver: func(operation, result string) {
			events = append(events, operation+":"+result)
		},
	})
	rec := privacyTriageRequest(t, h, http.MethodGet, "/api/privacy/scopes/run-1/mapping", "127.0.0.1:43120", privacyTriageTestToken)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != "{\"error\":\"response_too_large\"}\n" {
		t.Fatalf("oversized mapping=(%d,%q), want bounded stable 500", rec.Code, rec.Body.String())
	}
	if fmt.Sprint(events) != "[inspect:failed]" {
		t.Fatalf("events=%v, want exactly one inspect:failed", events)
	}
}

var _ PrivacyTriageSource = (*stubPrivacyTriage)(nil)
