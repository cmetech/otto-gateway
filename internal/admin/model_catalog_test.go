package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeModelCatalogSource struct {
	view   ModelCatalogView
	result ModelCatalogActionResult
}

func (f fakeModelCatalogSource) Snapshot() ModelCatalogView { return f.view }

func (f fakeModelCatalogSource) Refresh(context.Context) ModelCatalogActionResult { return f.result }

func TestModelCatalogAPI_GETSanitizesView(t *testing.T) {
	local := time.Date(2026, 8, 13, 15, 4, 5, 0, time.FixedZone("operator", -4*60*60))
	src := fakeModelCatalogSource{view: ModelCatalogView{
		State:      "unbounded-private-state",
		Count:      99,
		Generation: 7,
		Models: []ModelCatalogModel{
			{ID: "auto", Name: "untrusted auto", SelectionMode: "explicit", Capabilities: map[string]string{"completion": "unsafe"}},
			{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol", SelectionMode: "explicit", Capabilities: map[string]string{"completion": "supported", "tools": "not-a-state", "vision": "unsupported", "reasoning": "unknown", "evidence": "https://private.example"}},
		},
		Refresh: ModelCatalogRefreshView{
			LastAttemptAt:   local.Format(time.RFC3339),
			LastSuccessAt:   "not-a-timestamp",
			LastUpdatedAt:   "2026-08-13T20:04:05Z",
			NextAttemptAt:   "",
			LastOutcome:     "internal-debug-state",
			PendingRemovals: -1,
		},
	}}

	rec := serveModelCatalog(t, src, http.MethodGet, "/api/model-catalog", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q; want application/json", contentType)
	}

	var body map[string]any
	decodeModelCatalogJSON(t, rec, &body)
	models, ok := body["models"].([]any)
	if !ok {
		t.Fatalf("models = %#v; want JSON array", body["models"])
	}
	if got, want := int(body["count"].(float64)), len(models); got != want {
		t.Fatalf("count = %d; want displayed model count %d", got, want)
	}
	autoCount := 0
	for _, raw := range models {
		model := raw.(map[string]any)
		if model["id"] == "auto" {
			autoCount++
			if model["name"] != "Automatic" || model["selection_mode"] != "automatic" {
				t.Fatalf("auto = %#v; want normalized synthetic model", model)
			}
		}
		caps, ok := model["capabilities"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities = %#v; want JSON object", model["capabilities"])
		}
		for key, rawValue := range caps {
			value, ok := rawValue.(string)
			if !ok || (value != "supported" && value != "unsupported" && value != "unknown") {
				t.Fatalf("capability %s = %#v; want bounded capability state", key, rawValue)
			}
		}
		if _, leaked := caps["evidence"]; leaked {
			t.Fatalf("capabilities leaked evidence: %#v", caps)
		}
	}
	if autoCount != 1 {
		t.Fatalf("auto entries = %d; want exactly one", autoCount)
	}
	refresh := body["refresh"].(map[string]any)
	if got, want := refresh["last_attempt_at"], "2026-08-13T19:04:05Z"; got != want {
		t.Fatalf("last_attempt_at = %q; want UTC RFC3339 %q", got, want)
	}
	if _, present := refresh["last_success_at"]; present {
		t.Fatalf("last_success_at = %q; want invalid timestamp omitted", refresh["last_success_at"])
	}
	if _, leaked := body["evidence"]; leaked {
		t.Fatalf("response leaked evidence: %#v", body)
	}
	if _, leaked := body["upstream"]; leaked {
		t.Fatalf("response leaked upstream detail: %#v", body)
	}
}

func TestModelCatalogAPI_GETNilSourceIsDisabled(t *testing.T) {
	rec := serveModelCatalog(t, nil, http.MethodGet, "/api/model-catalog", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET nil source status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	decodeModelCatalogJSON(t, rec, &body)
	if got := body["state"]; got != "disabled" {
		t.Fatalf("state = %q; want disabled", got)
	}
	models := body["models"].([]any)
	if len(models) != 1 || models[0].(map[string]any)["id"] != "auto" {
		t.Fatalf("models = %#v; want synthetic auto only", models)
	}
}

func TestModelCatalogAPI_POSTMapsRefreshResults(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{code: "", want: http.StatusOK},
		{code: "catalog_refresh_in_progress", want: http.StatusConflict},
		{code: "catalog_refresh_cooldown", want: http.StatusTooManyRequests},
		{code: "catalog_refresh_busy", want: http.StatusServiceUnavailable},
		{code: "catalog_refresh_failed", want: http.StatusBadGateway},
		{code: "catalog_refresh_unavailable", want: http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			src := fakeModelCatalogSource{result: ModelCatalogActionResult{
				Outcome:           "unbounded internal outcome",
				Code:              tc.code,
				Message:           "upstream secret at /private/path",
				RetryAfterSeconds: 999999,
			}}
			rec := serveModelCatalog(t, src, http.MethodPost, "/api/model-catalog/refresh", nil)
			if rec.Code != tc.want {
				t.Fatalf("POST code=%q status = %d; want %d; body=%s", tc.code, rec.Code, tc.want, rec.Body.String())
			}
			var body map[string]any
			decodeModelCatalogJSON(t, rec, &body)
			if strings.Contains(rec.Body.String(), "upstream secret") || strings.Contains(rec.Body.String(), "/private/path") {
				t.Fatalf("POST leaked raw source message: %s", rec.Body.String())
			}
			if tc.code != "" && body["code"] != tc.code {
				t.Fatalf("POST code = %q; want %q", body["code"], tc.code)
			}
			if retry := rec.Header().Get("Retry-After"); retry != "" {
				if retry != "30" {
					t.Fatalf("Retry-After = %q; want bounded 30 seconds", retry)
				}
			}
		})
	}
}

func TestModelCatalogAPI_POSTRejectsExplicitCrossOriginBrowserRequests(t *testing.T) {
	src := fakeModelCatalogSource{result: ModelCatalogActionResult{Message: "refresh completed"}}
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{name: "matching origin", headers: map[string]string{"Origin": "http://example.com"}, want: http.StatusOK},
		{name: "nonmatching origin", headers: map[string]string{"Origin": "https://evil.example"}, want: http.StatusForbidden},
		{name: "cross site fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, want: http.StatusForbidden},
		{name: "operator without browser headers", headers: nil, want: http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveModelCatalog(t, src, http.MethodPost, "/api/model-catalog/refresh", tc.headers)
			if rec.Code != tc.want {
				t.Fatalf("POST status = %d; want %d; body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.want == http.StatusForbidden && strings.Contains(rec.Body.String(), "refresh completed") {
				t.Fatalf("forbidden response called or exposed source: %s", rec.Body.String())
			}
		})
	}
}

func TestModelCatalogAPI_POSTNilSourceIsUnavailable(t *testing.T) {
	rec := serveModelCatalog(t, nil, http.MethodPost, "/api/model-catalog/refresh", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST nil source status = %d; want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "catalog_refresh_unavailable") {
		t.Fatalf("POST nil source body = %s; want unavailable code", rec.Body.String())
	}
}

func serveModelCatalog(t *testing.T, source ModelCatalogSource, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	h := Handler(Deps{ModelCatalog: source})
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeModelCatalogJSON(t *testing.T, rec *httptest.ResponseRecorder, value any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(value); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
}
