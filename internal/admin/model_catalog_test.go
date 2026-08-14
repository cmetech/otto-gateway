package admin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeModelCatalogSource struct {
	view         ModelCatalogView
	result       ModelCatalogActionResult
	refreshCalls *int
}

func (f fakeModelCatalogSource) Snapshot() ModelCatalogView { return f.view }

func (f fakeModelCatalogSource) Refresh(context.Context) ModelCatalogActionResult {
	if f.refreshCalls != nil {
		*f.refreshCalls = *f.refreshCalls + 1
	}
	return f.result
}

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

func TestModelCatalogAPI_GETReservesOnlyExactAutoAndForcesExplicitRows(t *testing.T) {
	src := fakeModelCatalogSource{view: ModelCatalogView{
		State: "ready",
		Models: []ModelCatalogModel{
			{ID: "auto", Name: "untrusted synthetic duplicate", SelectionMode: "explicit"},
			{ID: "Auto", Name: "Case-sensitive upstream model", SelectionMode: "automatic"},
			{ID: "Auto", Name: "exact duplicate loses", SelectionMode: "automatic"},
			{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol", SelectionMode: "automatic"},
		},
		Refresh: ModelCatalogRefreshView{LastOutcome: "unchanged"},
	}}

	rec := serveModelCatalog(t, src, http.MethodGet, "/api/model-catalog", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var body ModelCatalogView
	decodeModelCatalogJSON(t, rec, &body)
	wantIDs := []string{"auto", "Auto", "gpt-5.6-sol"}
	if body.Count != len(wantIDs) || len(body.Models) != len(wantIDs) {
		t.Fatalf("count/models = %d/%d; want %d", body.Count, len(body.Models), len(wantIDs))
	}
	for index, wantID := range wantIDs {
		if got := body.Models[index].ID; got != wantID {
			t.Fatalf("models[%d].id = %q; want %q", index, got, wantID)
		}
		wantMode := "explicit"
		if wantID == "auto" {
			wantMode = "automatic"
		}
		if got := body.Models[index].SelectionMode; got != wantMode {
			t.Fatalf("models[%d].selection_mode = %q; want %q", index, got, wantMode)
		}
	}
}

func TestModelCatalogAPI_POSTMapsRefreshResults(t *testing.T) {
	cases := []struct {
		code        string
		want        int
		wantMessage string
	}{
		{code: "", want: http.StatusOK},
		{code: "catalog_refresh_in_progress", want: http.StatusConflict},
		{code: "catalog_refresh_cooldown", want: http.StatusTooManyRequests},
		{
			code:        "catalog_refresh_busy",
			want:        http.StatusServiceUnavailable,
			wantMessage: "No idle gateway worker is available for a model catalog refresh. The current catalog remains in use.",
		},
		{
			code:        "catalog_refresh_failed",
			want:        http.StatusBadGateway,
			wantMessage: "Model catalog refresh failed. The current catalog remains in use.",
		},
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
			if tc.wantMessage != "" && body["message"] != tc.wantMessage {
				t.Fatalf("POST message = %q; want %q", body["message"], tc.wantMessage)
			}
			if retry := rec.Header().Get("Retry-After"); retry != "30" {
				t.Fatalf("Retry-After = %q; want bounded 30 seconds", retry)
			}
			if retry := int(body["retry_after_seconds"].(float64)); retry != 30 {
				t.Fatalf("retry_after_seconds = %d; want same bounded 30 seconds", retry)
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
		{name: "exact same-origin fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://public.example"}, want: http.StatusOK},
		{name: "exact same-origin metadata without origin", headers: map[string]string{"Sec-Fetch-Site": "same-origin"}, want: http.StatusOK},
		{name: "same-origin with malformed origin", headers: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "not an origin"}, want: http.StatusForbidden},
		{name: "same-site fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "same-site"}, want: http.StatusForbidden},
		{name: "same-site overrides matching origin", headers: map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://example.com"}, want: http.StatusForbidden},
		{name: "cross site fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, want: http.StatusForbidden},
		{name: "unknown fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "none"}, want: http.StatusForbidden},
		{name: "empty fetch metadata", headers: map[string]string{"Sec-Fetch-Site": ""}, want: http.StatusForbidden},
		{name: "non-exact fetch metadata", headers: map[string]string{"Sec-Fetch-Site": "Same-Origin"}, want: http.StatusForbidden},
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

func TestModelCatalogAPI_POSTRejectsRepeatedBrowserMetadata(t *testing.T) {
	calls := 0
	h := Handler(Deps{ModelCatalog: fakeModelCatalogSource{
		result:       ModelCatalogActionResult{Message: "refresh completed"},
		refreshCalls: &calls,
	}})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/model-catalog/refresh", nil)
	req.Header.Add("Sec-Fetch-Site", "same-origin")
	req.Header.Add("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("POST repeated Fetch Metadata status/calls = %d/%d; want 403/0", rec.Code, calls)
	}
}

func TestModelCatalogAPI_POSTAllowsSameOriginBrowserThroughTLSHostRewritingProxy(t *testing.T) {
	calls := 0
	backend := httptest.NewServer(Handler(Deps{ModelCatalog: fakeModelCatalogSource{
		result:       ModelCatalogActionResult{Message: "refresh completed"},
		refreshCalls: &calls,
	}}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backendURL)
	director := proxy.Director
	proxy.Director = func(request *http.Request) {
		director(request)
		request.Host = backendURL.Host
		request.Header.Del("X-Forwarded-Host")
		request.Header.Del("X-Forwarded-Proto")
	}
	frontend := httptest.NewTLSServer(proxy)
	t.Cleanup(frontend.Close)

	browserRequest, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		frontend.URL+"/api/model-catalog/refresh",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	browserRequest.Header.Set("Origin", frontend.URL)
	browserRequest.Header.Set("Sec-Fetch-Site", "same-origin")
	browserRequest.Header.Set("X-Forwarded-Host", "untrusted.invalid")
	browserRequest.Header.Set("X-Forwarded-Proto", "http")
	response, err := frontend.Client().Do(browserRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || calls != 1 {
		t.Fatalf("proxied same-origin POST status/calls = %d/%d; want 200/1", response.StatusCode, calls)
	}

	fallbackRequest, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		frontend.URL+"/api/model-catalog/refresh",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fallbackRequest.Header.Set("Origin", frontend.URL)
	fallbackRequest.Header.Set("X-Forwarded-Host", strings.TrimPrefix(frontend.URL, "https://"))
	fallbackRequest.Header.Set("X-Forwarded-Proto", "https")
	response, err = frontend.Client().Do(fallbackRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden || calls != 1 {
		t.Fatalf("Origin-only proxied POST status/calls = %d/%d; want strict fallback 403/1", response.StatusCode, calls)
	}
}

func TestModelCatalogAPI_POSTStrictlyValidatesOriginBeforeRefresh(t *testing.T) {
	cases := []struct {
		name      string
		origin    string
		host      string
		https     bool
		want      int
		wantCalls int
	}{
		{name: "matching origin", origin: "http://example.com", host: "example.com", want: http.StatusOK, wantCalls: 1},
		{name: "host case is canonical", origin: "HTTP://EXAMPLE.COM", host: "example.com", want: http.StatusOK, wantCalls: 1},
		{name: "default http port is equivalent", origin: "http://example.com:80", host: "example.com", want: http.StatusOK, wantCalls: 1},
		{name: "default https port is equivalent", origin: "https://example.com", host: "EXAMPLE.COM:443", https: true, want: http.StatusOK, wantCalls: 1},
		{name: "non-default port", origin: "http://example.com:8080", host: "example.com", want: http.StatusForbidden},
		{name: "same host different scheme", origin: "https://example.com", host: "example.com", want: http.StatusForbidden},
		{name: "path", origin: "http://example.com/not-an-origin", host: "example.com", want: http.StatusForbidden},
		{name: "bare trailing slash", origin: "http://example.com/", host: "example.com", want: http.StatusForbidden},
		{name: "query", origin: "http://example.com?not-an-origin", host: "example.com", want: http.StatusForbidden},
		{name: "userinfo", origin: "http://operator@example.com", host: "example.com", want: http.StatusForbidden},
		{name: "fragment", origin: "http://example.com#not-an-origin", host: "example.com", want: http.StatusForbidden},
		{name: "bare fragment delimiter", origin: "http://example.com#", host: "example.com", want: http.StatusForbidden},
		{name: "bare query delimiter", origin: "http://example.com?", host: "example.com", want: http.StatusForbidden},
		{name: "encoded fragment delimiter", origin: "http://example.com%23", host: "example.com", want: http.StatusForbidden},
		{name: "encoded query delimiter", origin: "http://example.com%3F", host: "example.com", want: http.StatusForbidden},
		{name: "opaque", origin: "http:example.com", host: "example.com", want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			src := fakeModelCatalogSource{
				result:       ModelCatalogActionResult{Message: "refresh completed"},
				refreshCalls: &calls,
			}
			h := Handler(Deps{ModelCatalog: src})
			req := httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/api/model-catalog/refresh",
				nil,
			)
			req.Host = tc.host
			if tc.https {
				req.TLS = &tls.ConnectionState{}
			}
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("POST origin=%q host=%q https=%t status = %d; want %d; body=%s", tc.origin, tc.host, tc.https, rec.Code, tc.want, rec.Body.String())
			}
			if calls != tc.wantCalls {
				t.Fatalf("Refresh calls = %d; want %d", calls, tc.wantCalls)
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
	req := httptest.NewRequestWithContext(context.Background(), method, path, nil)
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
