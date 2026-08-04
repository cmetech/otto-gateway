package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/go-cmp/cmp"

	"otto-gateway/internal/acp"
	"otto-gateway/internal/admin"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/config"
	gatewayembed "otto-gateway/internal/embed"
	"otto-gateway/internal/engine"
	"otto-gateway/internal/metrics"
	"otto-gateway/internal/plugin"
	"otto-gateway/internal/plugin/pii"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/privacy"
	"otto-gateway/internal/procstat"
	"otto-gateway/internal/registry"
	"otto-gateway/internal/testutil"
)

type fakeRecycleMetricsRecorder struct{}

func (*fakeRecycleMetricsRecorder) RecordWorkerRecycle(string, uint64, time.Duration) {}

func TestPoolDetailAdapter_SnapshotSlotFromPoolCopiesIdleRecycleActivity(t *testing.T) {
	sid := "session-17"
	spawned := time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC)
	released := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	input := pool.AgentSlot{
		Label:                  "slot-3",
		Alive:                  true,
		Busy:                   true,
		CheckedOut:             true,
		CurrentSessionID:       &sid,
		Turns:                  9,
		SpawnedAt:              &spawned,
		Pid:                    43210,
		UserRequestsSinceSpawn: 7,
		LastUserReleaseAt:      &released,
	}
	want := admin.SnapshotSlot{
		Label:                  "slot-3",
		Alive:                  true,
		Busy:                   true,
		CheckedOut:             true,
		CurrentSessionID:       &sid,
		Turns:                  9,
		SpawnedAt:              &spawned,
		Pid:                    43210,
		UserRequestsSinceSpawn: 7,
		LastUserReleaseAt:      &released,
	}

	if diff := cmp.Diff(want, snapshotSlotFromPool(input)); diff != "" {
		t.Fatalf("pool-to-admin slot projection mismatch (-want +got):\n%s", diff)
	}
}

func TestApplyIdleMemoryRecyclePoolConfig_IdleRecyclePolicy(t *testing.T) {
	recorder := &fakeRecycleMetricsRecorder{}
	cfg := config.Config{
		KiroWorkerIdleRecycleAfter:    37 * time.Minute,
		KiroWorkerIdleRecycleMemoryMB: 613,
	}
	var got pool.Config

	applyIdleMemoryRecyclePoolConfig(&got, cfg, recorder)

	if got.IdleRecycleAfter != 37*time.Minute {
		t.Fatalf("IdleRecycleAfter = %v, want 37m", got.IdleRecycleAfter)
	}
	if got.IdleRecycleMemoryBytes != 613<<20 {
		t.Fatalf("IdleRecycleMemoryBytes = %d, want %d", got.IdleRecycleMemoryBytes, uint64(613<<20))
	}
	if got.WorkerMemorySupported != procstat.Supported() {
		t.Fatalf("WorkerMemorySupported = %t, want %t", got.WorkerMemorySupported, procstat.Supported())
	}
	if got.ReadWorkerMemory == nil {
		t.Fatal("ReadWorkerMemory = nil")
	}
	wantSample := procstat.Read(os.Getpid())
	rssBytes, ok := got.ReadWorkerMemory(os.Getpid())
	if rssBytes != wantSample.RSSBytes || ok != wantSample.OK {
		t.Fatalf("ReadWorkerMemory(self) = (%d, %t), want (%d, %t)", rssBytes, ok, wantSample.RSSBytes, wantSample.OK)
	}
	if got.RecycleMetrics != recorder {
		t.Fatalf("RecycleMetrics identity = %T %p, want %T %p", got.RecycleMetrics, got.RecycleMetrics, recorder, recorder)
	}
}

func TestApplyIdleMemoryRecyclePoolConfig_NegativeMemoryIsDisabled(t *testing.T) {
	cfg := config.Config{KiroWorkerIdleRecycleMemoryMB: -1}
	var got pool.Config

	applyIdleMemoryRecyclePoolConfig(&got, cfg, nil)

	if got.IdleRecycleMemoryBytes != 0 {
		t.Fatalf("IdleRecycleMemoryBytes = %d, want 0", got.IdleRecycleMemoryBytes)
	}
}

func TestIdleRecycleMetricsWorkerProcFromPoolCopiesActivity(t *testing.T) {
	input := pool.WorkerProc{
		Label:                  "slot-2",
		Pid:                    24680,
		UserRequestsSinceSpawn: 11,
		IdleSeconds:            905.25,
	}
	want := metrics.WorkerProc{
		Slot:                   "slot-2",
		Pid:                    24680,
		UserRequestsSinceSpawn: 11,
		IdleSeconds:            905.25,
	}

	if diff := cmp.Diff(want, metricsWorkerProcFromPool(input)); diff != "" {
		t.Fatalf("pool-to-metrics worker projection mismatch (-want +got):\n%s", diff)
	}
}

func TestApp_IdleMemoryRecycleAdminPolicyFromConfig_IdleRecycleSnapshot(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.KiroCmd = ""
	cfg.KiroWorkerIdleRecycleAfter = 23*time.Minute + 17*time.Second
	cfg.KiroWorkerIdleRecycleMemoryMB = 731

	a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/api/snapshot", nil)
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin/api/snapshot: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var snap admin.Snapshot
	if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Pool.IdleRecycleMS != 1_397_000 {
		t.Fatalf("idle_recycle_ms = %d, want 1397000", snap.Pool.IdleRecycleMS)
	}
	if snap.Pool.IdleRecycleMemoryBytes != 731<<20 {
		t.Fatalf("idle_recycle_memory_bytes = %d, want %d", snap.Pool.IdleRecycleMemoryBytes, uint64(731<<20))
	}
	if snap.Pool.IdleRecycleSupported != procstat.Supported() {
		t.Fatalf("idle_recycle_supported = %t, want %t", snap.Pool.IdleRecycleSupported, procstat.Supported())
	}
}

func TestRedactSupportUsesSharedSecretClassifierBeforeStartup(t *testing.T) {
	const input = "Authorization: Bearer bearer-secret-7788\nclient_secret=client-secret-8899\npostgres://dbuser:dbpass@db.internal/app\nghp_abcdefghijklmnopqrstuvwxyzABCDE12345\nmonkey=value\n"
	const want = "[REDACTED]\nclient_secret=[REDACTED]\n[REDACTED]\n[REDACTED]\nmonkey=value\n"

	var out bytes.Buffer
	handled, err := runUtility([]string{"redact-support"}, strings.NewReader(input), &out)
	if err != nil {
		t.Fatalf("runUtility: %v", err)
	}
	if !handled {
		t.Fatal("redact-support was not handled before normal startup")
	}
	if got := out.String(); got != want {
		t.Fatalf("redact-support output:\n got: %q\nwant: %q", got, want)
	}
	for _, secret := range []string{"bearer-secret-7788", "client-secret-8899", "dbpass", "ghp_abcdefghijklmnopqrstuvwxyzABCDE12345"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("redact-support leaked %q", secret)
		}
	}
}

func TestRedactSupportRejectsInvalidOrUnboundedUTF8Records(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "invalid UTF-8", input: string([]byte{0xff, '\n'})},
		{name: "oversized record", input: strings.Repeat("a", 1<<20+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			handled, err := runUtility([]string{"redact-support"}, strings.NewReader(tc.input), &out)
			if !handled || err == nil {
				t.Fatalf("runUtility=(%t,%v), want handled error", handled, err)
			}
			if out.Len() != 0 {
				t.Fatalf("failure emitted partial artifact bytes: %q", out.String())
			}
			if !utf8.ValidString(err.Error()) || strings.Contains(err.Error(), tc.input) {
				t.Fatalf("error disclosed input content: %q", err)
			}
		})
	}
}

func strictPrivacyConfig() config.Config {
	return config.Config{
		HTTPAddr:                  ":0",
		KiroCmd:                   "",
		PoolSize:                  1,
		PingInterval:              time.Minute,
		OllamaPathPrefix:          "/api",
		OpenAIPathPrefix:          "/v1",
		AnthropicPathPrefix:       "/v1",
		PIIRedactionEnabled:       true,
		PIIRedactionMode:          "replace",
		PIINEREnabled:             false,
		PrivacyDefaultProfile:     "standard",
		PrivacyRequestProfiles:    []string{"standard", "strict"},
		PrivacyAliasKey:           "task-8-main-alias-key",
		PrivacySecretAction:       "replace",
		PrivacyTechnicalAction:    "pseudonymize",
		PrivacyScopeTTL:           time.Hour,
		PrivacyMaxScopes:          8,
		PrivacyMaxEntriesPerScope: 32,
		PrivacyMaxTotalEntries:    128,
	}
}

// TestPrivacyHealth_SafeProjection catches health wiring that either drops the
// privacy posture or leaks configured key/token values. Expectations are
// literal and exercise the real /health/hooks boundary.
func TestPrivacyHealth_SafeProjection(t *testing.T) {
	const aliasCanary = "privacy-alias-key-MUST-NOT-LEAK"
	const triageCanary = "privacy-triage-token-MUST-NOT-LEAK"
	cfg := strictPrivacyConfig()
	cfg.PrivacyAliasKey = aliasCanary
	cfg.PrivacyTriageEnabled = true
	cfg.PrivacyTriageToken = triageCanary

	a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	rec := httptest.NewRecorder()
	a.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /health/hooks: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	for _, canary := range []string{aliasCanary, triageCanary} {
		if strings.Contains(rec.Body.String(), canary) {
			t.Fatalf("health response leaked protected canary %q: %s", canary, rec.Body.String())
		}
	}

	var body struct {
		Hooks []struct {
			Name   string         `json:"name"`
			Config map[string]any `json:"config"`
		} `json:"hooks"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health/hooks: %v", err)
	}
	for _, hook := range body.Hooks {
		if hook.Name != "PIIRedactionHook" {
			continue
		}
		wantRecognizers := []any{
			"Email", "IPv4", "IPv6", "SSN", "CreditCard", "USPhone", "SIP_URI", "IMEI",
			"IMSI", "MSISDN", "MAC_ADDRESS", "COORDINATES", "SITE", "USAddress", "USState", "USZIP",
		}
		wantPrivacy := map[string]any{
			"default_profile":          "standard",
			"request_profiles":         []any{"standard", "strict"},
			"strict_available":         true,
			"triage_enabled":           true,
			"alias_key_present":        true,
			"triage_token_present":     true,
			"pii_enabled":              true,
			"ner_enabled":              false,
			"secret_action":            "replace",
			"technical_action":         "pseudonymize",
			"pii_mode":                 "replace",
			"recognizers":              wantRecognizers,
			"entity_actions":           map[string]any{},
			"strict_full_buffering":    true,
			"receipt_version":          float64(1),
			"scopes_active":            float64(0),
			"requests_in_flight":       float64(0),
			"entries":                  float64(0),
			"max_scopes":               float64(8),
			"max_entries_per_scope":    float64(32),
			"max_total_entries":        float64(128),
			"scope_ttl_seconds":        float64(3600),
			"oldest_scope_age_seconds": float64(0),
			"requests_protected":       float64(0),
			"requests_blocked":         float64(0),
		}
		wantConfig := map[string]any{
			"enabled":        true,
			"mode":           "replace",
			"entities":       wantRecognizers,
			"decrypt_active": false,
			"entity_actions": map[string]any{},
			"privacy":        wantPrivacy,
		}
		if diff := cmp.Diff(wantConfig, hook.Config); diff != "" {
			t.Fatalf("PIIRedactionHook health config mismatch (-want +got):\n%s", diff)
		}
		return
	}
	t.Fatal("/health/hooks omitted compatibility hook PIIRedactionHook")
}

func TestNewApp_RegistryLoadFailureClosesPrivacyService(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.EnabledSurfaces = []string{"openai"}
	var constructed *privacy.Service
	wantErr := errors.New("registry fixture failed")

	a, cleanup, err := newAppWithRegistryLoader(context.Background(), cfg, testutil.Logger(t), func(service *privacy.Service) (*registry.Registry, error) {
		constructed = service
		return nil, wantErr
	})
	if a != nil || err == nil || !errors.Is(err, wantErr) {
		t.Fatalf("newAppWithRegistryLoader = (%v, _, %v), want nil app and wrapped loader error", a, err)
	}
	cleanup()
	if constructed == nil {
		t.Fatal("registry loader did not observe constructed privacy service")
	}
	state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "after-registry-failure"})
	_, acquireErr := constructed.Before(privacy.WithRequestState(context.Background(), state), &canonical.ChatRequest{})
	var privacyErr *privacy.Error
	if !errors.As(acquireErr, &privacyErr) || privacyErr.Code != privacy.CodeScopeClosed || privacyErr.Stage != "scope" {
		t.Fatalf("strict acquisition after startup failure = %v, want closed-scope privacy error", acquireErr)
	}
}

func namedHook(hook any) string {
	if named, ok := hook.(interface{ Name() string }); ok {
		return named.Name()
	}
	typ := reflect.TypeOf(hook)
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	return typ.Name()
}

func preHookNames(hooks []engine.PreHook) []string {
	names := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		names = append(names, namedHook(hook))
	}
	return names
}

func postHookNames(hooks []engine.PostHook) []string {
	names := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		names = append(names, namedHook(hook))
	}
	return names
}

func TestPrivacyServiceConstructedOnceAndShared(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.ChatTrace = true
	cfg.ChatTraceFile = filepath.Join(t.TempDir(), "chat-trace.log")
	a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	if a.privacyService == nil || a.privacySnapshot == nil {
		t.Fatal("privacy service and safe snapshot projection must be available")
	}

	var privacyHook *pii.PIIRedactionHook
	var traceHook *plugin.ChatTraceHook
	for _, hook := range a.hooks.Pre {
		switch typed := hook.(type) {
		case *pii.PIIRedactionHook:
			privacyHook = typed
		case *plugin.ChatTraceHook:
			traceHook = typed
		}
	}
	if privacyHook == nil || traceHook == nil {
		t.Fatalf("privacy/trace hook missing from Pre chain: %v", preHookNames(a.hooks.Pre))
	}
	if privacyHook.Service != a.privacyService {
		t.Fatal("PIIRedactionHook does not share app privacy service")
	}
	if traceHook.Privacy != a.privacyService {
		t.Fatal("ChatTraceHook does not share app privacy service")
	}
	if a.privacySnapshot != a.privacyService {
		t.Fatal("safe snapshot projection does not share app privacy service")
	}

	cleanup()
	state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "after-cleanup"})
	ctx := privacy.WithRequestState(context.Background(), state)
	_, err = a.privacyService.Before(ctx, &canonical.ChatRequest{})
	status, code, ok := privacy.ErrorInfo(err)
	if !ok || status != http.StatusConflict || code != privacy.CodeScopeClosed {
		t.Fatalf("privacy service remained open after cleanup: ErrorInfo=(%d,%q,%t), err=%v", status, code, ok, err)
	}
}

func TestPrivacyServiceTraceSummaryLifecycle(t *testing.T) {
	a, cleanup, err := newApp(context.Background(), strictPrivacyConfig(), testutil.Logger(t))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()
	state := privacy.NewRequestState(privacy.RequestMetadata{
		RequestedProfile: "strict",
		ScopeID:          "trace-summary",
		Surface:          "anthropic",
		Workload:         "network-hardening",
	})
	ctx := privacy.WithRequestState(context.Background(), state)
	wantUnresolved := map[string]any{
		"surface": "anthropic", "workload": "network-hardening", "profile": "strict",
		"coverage": "unresolved", "result": "unresolved",
		"transformed": 0, "restored": 0, "blocked": 0,
	}
	if diff := cmp.Diff(wantUnresolved, a.privacyService.TraceSummary(ctx)); diff != "" {
		t.Fatalf("unresolved trace summary mismatch (-want +got):\n%s", diff)
	}

	req := &canonical.ChatRequest{Messages: []canonical.Message{{
		Role: canonical.RoleUser, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ordinary request"}},
	}}}
	if response, err := a.privacyService.Before(ctx, req); err != nil || response != nil {
		t.Fatalf("Before=(%v,%v), want (nil,nil)", response, err)
	}
	resp := &canonical.ChatResponse{Message: canonical.Message{
		Role: canonical.RoleAssistant, Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "ordinary response"}},
	}}
	if err := a.privacyService.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}
	wantPass := map[string]any{
		"surface": "anthropic", "workload": "network-hardening", "profile": "strict",
		"coverage": "full", "result": "pass",
		"transformed": 0, "restored": 0, "blocked": 0,
	}
	if diff := cmp.Diff(wantPass, a.privacyService.TraceSummary(ctx)); diff != "" {
		t.Fatalf("validated trace summary mismatch (-want +got):\n%s", diff)
	}
}

// TestPrivacyMetricsWiredAndNoLeak catches a metrics registry that is not
// connected to the one app privacy service, stale push-style gauges, or any
// accidental serialization of protected request content into a scrape.
func TestPrivacyMetricsWiredAndNoLeak(t *testing.T) {
	a, cleanup, err := newApp(context.Background(), strictPrivacyConfig(), testutil.Logger(t))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	const protectedCanary = "10.23.45.67"
	state := privacy.NewRequestState(privacy.RequestMetadata{
		RequestedProfile: "strict",
		ScopeID:          "task12-metrics",
		Surface:          "openai",
		Workload:         "network-hardening",
	})
	ctx := privacy.WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Messages: []canonical.Message{{
		Role: canonical.RoleUser,
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: "peer " + protectedCanary,
		}},
	}}}
	if response, err := a.privacyService.Before(ctx, req); err != nil || response != nil {
		t.Fatalf("Before = (%v, %v), want (nil, nil)", response, err)
	}
	resp := &canonical.ChatResponse{Message: canonical.Message{
		Role: canonical.RoleAssistant,
		Content: []canonical.ContentPart{{
			Kind: canonical.ContentKindText,
			Text: req.Messages[0].Content[0].Text,
		}},
	}}
	if err := a.privacyService.After(ctx, req, resp); err != nil {
		t.Fatalf("After: %v", err)
	}

	recorder := httptest.NewRecorder()
	a.srv.ServeHTTP(recorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want 200", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`gw_privacy_requests_total{gateway_id=`,
		`profile="strict",result="pass",surface="openai",workload="network-hardening"} 1`,
		`gw_privacy_receipts_total{gateway_id=`,
		`profile="strict",result="pass"} 1`,
		`gw_privacy_transformations_total{action="pseudonymize",entity="IPv4",gateway_id=`,
		`gw_privacy_restorations_total{entity="IPv4",gateway_id=`,
		`profile="strict",result="pass"} 1`,
		`gw_privacy_mapping_operations_total{gateway_id=`,
		`operation="lookup",result="miss"} 1`,
		`operation="insert",result="pass"} 1`,
		`operation="restore",result="pass"} 1`,
		`gw_privacy_scope_events_total{event="created",gateway_id=`,
		`gw_privacy_processing_duration_seconds_count{gateway_id=`,
		`gw_privacy_scopes_active{gateway_id=`,
		`gw_privacy_mapping_entries{gateway_id=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("privacy scrape missing %q\n%s", want, body)
		}
	}
	for _, forbidden := range []string{protectedCanary, "task12-metrics"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("privacy scrape leaked protected value %q", forbidden)
		}
	}

	triage := httptest.NewRecorder()
	a.srv.ServeHTTP(triage, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/api/privacy/scopes", nil))
	if triage.Code != http.StatusNotFound {
		t.Fatalf("Task 12 introduced privacy triage policy/route: status=%d", triage.Code)
	}
}

func TestPrivacyTriageLiveWiringUsesSharedServiceAndMetrics(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.PrivacyTriageEnabled = true
	cfg.PrivacyTriageToken = "task-13-live-token"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	a, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", ScopeID: "task-13-live"})
	ctx := privacy.WithRequestState(context.Background(), state)
	req := &canonical.ChatRequest{Messages: []canonical.Message{{
		Role:    canonical.RoleUser,
		Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "peer 10.23.45.67"}},
	}}}
	if response, beforeErr := a.privacyService.Before(ctx, req); beforeErr != nil || response != nil {
		t.Fatalf("Before=(%v,%v), want nil,nil", response, beforeErr)
	}

	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequestWithContext(context.Background(), method, path, nil)
		r.RemoteAddr = "127.0.0.1:43120"
		r.Header.Set("Authorization", "Bearer "+cfg.PrivacyTriageToken)
		recorder := httptest.NewRecorder()
		a.srv.ServeHTTP(recorder, r)
		return recorder
	}

	list := request(http.MethodGet, "/admin/api/privacy/scopes")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"task-13-live"`) {
		t.Fatalf("list=(%d,%q), want shared live scope", list.Code, list.Body.String())
	}
	inspect := request(http.MethodGet, "/admin/api/privacy/scopes/task-13-live/mapping")
	if inspect.Code != http.StatusOK || !strings.Contains(inspect.Body.String(), `"original":"10.23.45.67"`) {
		t.Fatalf("inspect=(%d,%q), want shared live mapping", inspect.Code, inspect.Body.String())
	}
	clearResp := request(http.MethodDelete, "/admin/api/privacy/scopes/task-13-live")
	if clearResp.Code != http.StatusAccepted || clearResp.Body.String() != "{\"state\":\"closing\"}\n" {
		t.Fatalf("active clear=(%d,%q), want 202 closing", clearResp.Code, clearResp.Body.String())
	}

	resp := &canonical.ChatResponse{Message: canonical.Message{
		Role:    canonical.RoleAssistant,
		Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: req.Messages[0].Content[0].Text}},
	}}
	if afterErr := a.privacyService.After(ctx, req, resp); afterErr != nil {
		t.Fatalf("After while scope closing: %v", afterErr)
	}
	clearResp = request(http.MethodDelete, "/admin/api/privacy/scopes/task-13-live")
	if clearResp.Code != http.StatusNoContent || clearResp.Body.Len() != 0 {
		t.Fatalf("repeated clear=(%d,%q), want 204", clearResp.Code, clearResp.Body.String())
	}

	metricsRecorder := httptest.NewRecorder()
	a.srv.ServeHTTP(metricsRecorder, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	metricsBody := metricsRecorder.Body.String()
	for _, want := range []string{
		`operation="list",result="completed"`,
		`operation="inspect",result="completed"`,
		`operation="clear",result="closing"`,
		`operation="clear",result="completed"`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
	for _, forbidden := range []string{"task-13-live", "10.23.45.67", cfg.PrivacyTriageToken} {
		if strings.Contains(metricsBody, forbidden) {
			t.Errorf("metrics leaked %q", forbidden)
		}
		if strings.Contains(logs.String(), forbidden) {
			t.Errorf("ordinary logs leaked %q: %s", forbidden, logs.String())
		}
	}
}

func TestPrivacyTriageDeniedParameterizedPathsRedactedFromAccessLog(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.PrivacyTriageEnabled = true
	cfg.PrivacyTriageToken = "task-13-denied-token"
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	a, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	const (
		rawScope     = "denied-scope-canary"
		encodedScope = "denied%2Dscope%2Dcanary"
	)
	operations := []struct {
		name        string
		method      string
		suffix      string
		wantLogPath string
	}{
		{
			name: "inspect", method: http.MethodGet, suffix: "/mapping",
			wantLogPath: `"path":"/admin/api/privacy/scopes/{scope-id}/mapping"`,
		},
		{
			name: "single-clear", method: http.MethodDelete,
			wantLogPath: `"path":"/admin/api/privacy/scopes/{scope-id}"`,
		},
	}
	forms := []struct {
		name  string
		scope string
	}{
		{name: "raw", scope: rawScope},
		{name: "encoded", scope: encodedScope},
	}
	denials := []struct {
		name       string
		remoteAddr string
		token      string
		wantStatus int
	}{
		{name: "missing-token", remoteAddr: "127.0.0.1:43120", wantStatus: http.StatusUnauthorized},
		{name: "wrong-token", remoteAddr: "127.0.0.1:43120", token: "wrong-token", wantStatus: http.StatusUnauthorized},
		{name: "non-loopback", remoteAddr: "192.0.2.10:43120", token: cfg.PrivacyTriageToken, wantStatus: http.StatusForbidden},
	}

	for _, operation := range operations {
		for _, form := range forms {
			for _, denial := range denials {
				name := operation.name + "/" + form.name + "/" + denial.name
				t.Run(name, func(t *testing.T) {
					logs.Reset()
					path := "/admin/api/privacy/scopes/" + form.scope + operation.suffix
					req := httptest.NewRequestWithContext(context.Background(), operation.method, path, nil)
					req.RemoteAddr = denial.remoteAddr
					req.Header.Set("Forwarded", "for=127.0.0.1")
					req.Header.Set("X-Forwarded-For", "127.0.0.1")
					if denial.token != "" {
						req.Header.Set("Authorization", "Bearer "+denial.token)
					}
					recorder := httptest.NewRecorder()
					a.srv.ServeHTTP(recorder, req)

					if recorder.Code != denial.wantStatus {
						t.Fatalf("status=%d, want %d; body=%s", recorder.Code, denial.wantStatus, recorder.Body.String())
					}
					if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
						t.Errorf("Cache-Control=%q, want no-store", got)
					}
					if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "" {
						t.Errorf("Access-Control-Allow-Origin=%q, want absent", got)
					}
					accessLog := logs.String()
					for _, forbidden := range []string{rawScope, encodedScope} {
						if strings.Contains(accessLog, forbidden) {
							t.Errorf("access log leaked scope form %q: %s", forbidden, accessLog)
						}
					}
					if !strings.Contains(accessLog, operation.wantLogPath) {
						t.Errorf("access log path not sanitized to matched route: %s", accessLog)
					}
				})
			}
		}
	}
}

func TestHookOrderPrivacyBoundary(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.ChatTrace = true
	cfg.ChatTraceFile = filepath.Join(t.TempDir(), "chat-trace.log")
	a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	wantPre := []string{
		"ChatTraceHook", "RequestIDHook", "AuthHook", "JSONFormatSteeringHook",
		"CompressionHook", "PIIRedactionHook", "LoggingHook",
	}
	if diff := cmp.Diff(wantPre, preHookNames(a.hooks.Pre)); diff != "" {
		t.Fatalf("Pre hook order mismatch (-want +got):\n%s", diff)
	}
	wantPost := []string{"PIIRedactionHook", "LoggingHook", "ChatTraceHook"}
	if diff := cmp.Diff(wantPost, postHookNames(a.hooks.Post)); diff != "" {
		t.Fatalf("Post hook order mismatch (-want +got):\n%s", diff)
	}

	trace, ok := a.hooks.Pre[0].(*plugin.ChatTraceHook)
	if !ok {
		t.Fatalf("first Pre hook = %T, want *plugin.ChatTraceHook", a.hooks.Pre[0])
	}
	req := &canonical.ChatRequest{
		Model:  "auto",
		System: "system canary",
		Messages: []canonical.Message{{
			Role:    canonical.RoleUser,
			Content: []canonical.ContentPart{{Kind: canonical.ContentKindText, Text: "request canary"}},
		}},
	}
	want := *req
	want.Messages = append([]canonical.Message(nil), req.Messages...)
	want.Messages[0].Content = append([]canonical.ContentPart(nil), req.Messages[0].Content...)
	state := privacy.NewRequestState(privacy.RequestMetadata{RequestedProfile: "strict", Surface: "openai"})
	ctx := privacy.WithRequestState(plugin.WithRequestID(context.Background(), "trace-nonmutating"), state)
	if response, err := trace.Before(ctx, req); err != nil || response != nil {
		t.Fatalf("ChatTrace.Before = (%v,%v), want (nil,nil)", response, err)
	}
	if diff := cmp.Diff(want, *req); diff != "" {
		t.Fatalf("ChatTrace mutated canonical request (-want +got):\n%s", diff)
	}
}

func TestStrictRequiresHookAfterFiltering(t *testing.T) {
	cfg := strictPrivacyConfig()
	cfg.EnabledHooks = []string{
		"RequestIDHook", "AuthHook", "JSONFormatSteeringHook", "CompressionHook", "LoggingHook",
	}
	a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
	cleanup()
	if err == nil {
		t.Fatalf("newApp = %+v, nil error; strict startup must reject missing PIIRedactionHook", a)
	}
	if !strings.Contains(err.Error(), "strict") || !strings.Contains(err.Error(), "PIIRedactionHook") {
		t.Fatalf("startup error = %q, want strict + PIIRedactionHook", err)
	}
}

func TestBuildAdminLogSourcesIncludesKiroWithFriendlyLabels(t *testing.T) {
	paths, order, labels := buildAdminLogSources("127.0.0.1:18080", "gateway.log", "boot.log", "kiro.log", "trace.log", true)
	if diff := cmp.Diff([]string{"main", "boot-err", "kiro", "chat-trace"}, order); diff != "" {
		t.Fatalf("log source order mismatch (-want +got):\n%s", diff)
	}
	if paths["kiro"] != "kiro.log" || labels["kiro"] != "Kiro" {
		t.Fatalf("Kiro source = %q, label = %q", paths["kiro"], labels["kiro"])
	}
	if _, ok := paths["co-worker"]; ok {
		t.Fatal("Co-worker must not be exposed")
	}
	if diff := cmp.Diff(map[string]string{
		"main":       "Gateway",
		"boot-err":   "Gateway boot/errors",
		"kiro":       "Kiro",
		"chat-trace": "Gateway chat trace",
	}, labels); diff != "" {
		t.Fatalf("log source labels mismatch (-want +got):\n%s", diff)
	}
}

func TestBuildAdminLogSourcesOmitsChatTraceWhenDisabled(t *testing.T) {
	paths, order, labels := buildAdminLogSources("127.0.0.1:18080", "gateway.log", "boot.log", "kiro.log", "trace.log", false)
	if diff := cmp.Diff([]string{"main", "boot-err", "kiro"}, order); diff != "" {
		t.Fatalf("disabled log source order mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]string{
		"main":     "gateway.log",
		"boot-err": "boot.log",
		"kiro":     "kiro.log",
	}, paths); diff != "" {
		t.Fatalf("disabled log source paths mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(map[string]string{
		"main":     "Gateway",
		"boot-err": "Gateway boot/errors",
		"kiro":     "Kiro",
	}, labels); diff != "" {
		t.Fatalf("disabled log source labels mismatch (-want +got):\n%s", diff)
	}
	for _, sources := range []map[string]string{paths, labels} {
		if _, ok := sources["chat-trace"]; ok {
			t.Fatal("Gateway chat trace must be absent when disabled")
		}
		if _, ok := sources["co-worker"]; ok {
			t.Fatal("Co-worker must not be exposed")
		}
	}
}

// TestIsStrictLoopbackHTTPListener catches wildcard, private-network, public,
// or arbitrary hostname listeners being treated as local merely because the
// admin handler is also reachable through loopback. Kiro may be registered
// only when the effective listener itself is strictly loopback-bound.
func TestIsStrictLoopbackHTTPListener(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{addr: "127.0.0.1:18080", want: true},
		{addr: "127.99.4.2:0", want: true},
		{addr: "[::1]:18080", want: true},
		{addr: "localhost:18080", want: true},
		{addr: "LOCALHOST:18080", want: true},
		{addr: "localhost.:18080", want: true},
		{addr: ":18080", want: false},
		{addr: "0.0.0.0:18080", want: false},
		{addr: "[::]:18080", want: false},
		{addr: "192.168.1.20:18080", want: false},
		{addr: "10.0.0.20:18080", want: false},
		{addr: "203.0.113.20:18080", want: false},
		{addr: "gateway.local:18080", want: false},
		{addr: "localhost.example:18080", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.addr, func(t *testing.T) {
			if got := isStrictLoopbackHTTPListener(tc.addr); got != tc.want {
				t.Fatalf("isStrictLoopbackHTTPListener(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// TestBuildAdminLogSourcesGatesKiroByEffectiveListener is the wiring-level
// contract: every listener keeps Gateway-owned sources, while only a strict
// loopback listener receives the sensitive Kiro source. Co-worker is never
// registered.
func TestBuildAdminLogSourcesGatesKiroByEffectiveListener(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantKiro bool
	}{
		{name: "default IPv4 loopback", addr: "127.0.0.1:18080", wantKiro: true},
		{name: "IPv6 loopback", addr: "[::1]:18080", wantKiro: true},
		{name: "localhost", addr: "localhost:18080", wantKiro: true},
		{name: "empty-host wildcard", addr: ":18080", wantKiro: false},
		{name: "IPv4 wildcard", addr: "0.0.0.0:18080", wantKiro: false},
		{name: "IPv6 wildcard", addr: "[::]:18080", wantKiro: false},
		{name: "private non-loopback", addr: "192.168.50.8:18080", wantKiro: false},
		{name: "public non-loopback", addr: "203.0.113.8:18080", wantKiro: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paths, order, labels := buildAdminLogSources(tc.addr, "gateway.log", "boot.log", "kiro.log", "trace.log", true)
			_, pathHasKiro := paths["kiro"]
			_, labelHasKiro := labels["kiro"]
			if pathHasKiro != tc.wantKiro || labelHasKiro != tc.wantKiro {
				t.Fatalf("Kiro registration for %q: path=%v label=%v, want %v; paths=%v labels=%v", tc.addr, pathHasKiro, labelHasKiro, tc.wantKiro, paths, labels)
			}
			wantOrder := []string{"main", "boot-err", "chat-trace"}
			if tc.wantKiro {
				wantOrder = []string{"main", "boot-err", "kiro", "chat-trace"}
			}
			if diff := cmp.Diff(wantOrder, order); diff != "" {
				t.Fatalf("source order mismatch (-want +got):\n%s", diff)
			}
			for _, sources := range []map[string]string{paths, labels} {
				if _, ok := sources["co-worker"]; ok {
					t.Fatal("Co-worker must never be exposed")
				}
				for _, id := range []string{"main", "boot-err", "chat-trace"} {
					if _, ok := sources[id]; !ok {
						t.Fatalf("Gateway source %q omitted for listener %q", id, tc.addr)
					}
				}
			}
		})
	}
}

func TestPrepareKiroLaunchMaterializesAndLogsDefaultAgent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	cfg := config.Config{
		KiroCmd:          "kiro-cli",
		KiroArgs:         []string{"acp", "--agent", "acp_proxy"},
		KiroCWD:          root,
		KiroChatLogFile:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroChatLogPath:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroCWDIsDefault: true,
	}

	if err := prepareKiroLaunch(cfg, logger); err != nil {
		t.Fatalf("prepareKiroLaunch: %v", err)
	}
	if _, err := os.Stat(gatewayembed.ACPProxyPath(root)); err != nil {
		t.Fatalf("agent file: %v", err)
	}
	text := logs.String()
	for _, want := range []string{
		"kiro launch configured",
		"kiro-cli",
		"acp_proxy",
		root,
		filepath.Join(root, "logs", "kiro-chat.log"),
		gatewayembed.ACPProxyPath(root),
		"created",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("log missing %q: %s", want, text)
		}
	}
}

func TestPrepareKiroLaunchPreservesDefaultAgent(t *testing.T) {
	root := t.TempDir()
	path := gatewayembed.ACPProxyPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const custom = `{"name":"custom"}`
	if err := os.WriteFile(path, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		KiroCmd:          "kiro-cli",
		KiroArgs:         []string{"acp", "--agent", "acp_proxy"},
		KiroCWD:          root,
		KiroChatLogFile:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroChatLogPath:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroCWDIsDefault: true,
	}

	if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != custom {
		t.Fatalf("existing agent was overwritten: %q", body)
	}
}

func TestPrepareKiroLaunchDoesNotModifyCustomCWD(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{
		KiroCmd:          "kiro-cli",
		KiroArgs:         []string{"acp"},
		KiroCWD:          root,
		KiroChatLogFile:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroChatLogPath:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroCWDIsDefault: false,
	}

	if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".kiro")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("custom cwd was modified: %v", err)
	}
}

func TestPrepareKiroLaunchReturnsMaterializationError(t *testing.T) {
	root := t.TempDir()
	path := gatewayembed.ACPProxyPath(root)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		KiroCmd:          "kiro-cli",
		KiroCWD:          root,
		KiroChatLogFile:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroChatLogPath:  filepath.Join(root, "logs", "kiro-chat.log"),
		KiroCWDIsDefault: true,
	}

	err := prepareKiroLaunch(cfg, testutil.Logger(t))
	if err == nil || !strings.Contains(err.Error(), "prepare acp_proxy agent") {
		t.Fatalf("error = %v, want prepare acp_proxy agent failure", err)
	}
}

func TestPrepareKiroLaunchPreparesNativeLogDir(t *testing.T) {
	root := t.TempDir()
	logFile := filepath.Join(root, "logs", "kiro-chat.log")
	cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: root, KiroChatLogFile: logFile, KiroChatLogPath: logFile}
	if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(logFile)); err != nil || !info.IsDir() {
		t.Fatalf("info=%v err=%v", info, err)
	}
}

func TestPrepareKiroLaunchPreparesRelativeLogDirFromKiroCWD(t *testing.T) {
	kiroCWD := t.TempDir()
	relative := filepath.Join("relative-"+filepath.Base(kiroCWD), "native", "kiro.log")
	parentPath := filepath.Join(kiroCWD, relative)
	wrongParent, err := filepath.Abs(filepath.Dir(relative))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(wrongParent)) })
	cfg := config.Config{
		KiroCmd:         "kiro-cli",
		KiroCWD:         kiroCWD,
		KiroChatLogFile: relative,
		KiroChatLogPath: parentPath,
	}

	if err := prepareKiroLaunch(cfg, testutil.Logger(t)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(parentPath)); err != nil || !info.IsDir() {
		t.Fatalf("parent-visible log directory info=%v err=%v", info, err)
	}
}

func TestNewAppDashboardTailsRelativeKiroLogFromKiroCWD(t *testing.T) {
	kiroCWD := t.TempDir()
	relative := filepath.Join("relative-"+filepath.Base(kiroCWD), "kiro.log")
	parentPath := filepath.Join(kiroCWD, relative)
	if err := os.MkdirAll(filepath.Dir(parentPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parentPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wrongPath, err := filepath.Abs(relative)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(wrongPath)) })

	cfg := config.Config{
		HTTPAddr:         "127.0.0.1:0",
		KiroCmd:          "",
		KiroCWD:          kiroCWD,
		KiroChatLogFile:  relative,
		KiroChatLogPath:  parentPath,
		PoolSize:         1,
		PingInterval:     time.Minute,
		OllamaPathPrefix: "/api",
	}
	a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/admin/logs/stream?source=kiro", nil)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.srv.ServeHTTP(rec, req)
	}()
	time.Sleep(400 * time.Millisecond)
	const line = "relative Kiro log reached the dashboard"
	if err := os.WriteFile(parentPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(800 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dashboard log stream did not stop after cancellation")
	}
	if body := rec.Body.String(); !strings.Contains(body, "data: "+line) {
		t.Fatalf("dashboard did not tail parent-visible Kiro path %q: %s", parentPath, body)
	}
}

func TestKiroProcessEnvironmentComposition(t *testing.T) {
	t.Setenv("KIRO_LOG_LEVEL", "debug")
	t.Setenv("KIRO_CHAT_LOG_FILE", "parent.log")
	output := filepath.Join(t.TempDir(), "kiro-env.json")
	const childLogFile = "  child.log  "
	childEnv := kiroProcessEnv(config.Config{KiroChatLogFile: childLogFile})
	for _, entry := range childEnv {
		if strings.HasPrefix(entry, "KIRO_LOG_LEVEL=") {
			t.Fatalf("gateway must not inject a Kiro log level: %q", entry)
		}
	}
	env := append(
		childEnv,
		"GW_TEST_KIRO_ENV_OUTPUT="+output,
	)
	client, err := acp.New(acp.Config{
		Logger:       testutil.Logger(t),
		Command:      os.Args[0],
		Args:         []string{"-test.run=^TestKiroEnvironmentHelperProcess$"},
		Env:          env,
		PingInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	deadline := time.Now().Add(3 * time.Second)
	var body []byte
	for time.Now().Before(deadline) {
		body, err = os.ReadFile(output)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read helper environment: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"KIRO_LOG_LEVEL":     "debug",
		"KIRO_CHAT_LOG_FILE": childLogFile,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("spawned Kiro environment mismatch (-want +got):\n%s", diff)
	}
}

func TestKiroEnvironmentHelperProcess(t *testing.T) {
	output := os.Getenv("GW_TEST_KIRO_ENV_OUTPUT")
	if output == "" {
		return
	}
	body, err := json.Marshal(map[string]string{
		"KIRO_LOG_LEVEL":     os.Getenv("KIRO_LOG_LEVEL"),
		"KIRO_CHAT_LOG_FILE": os.Getenv("KIRO_CHAT_LOG_FILE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareKiroLaunchReportsNativeLogDirFailure(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(blocker, "kiro.log")
	cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: root, KiroChatLogFile: logFile, KiroChatLogPath: logFile}
	err := prepareKiroLaunch(cfg, testutil.Logger(t))
	if err == nil || !strings.Contains(err.Error(), "Kiro log directory") {
		t.Fatalf("error=%v", err)
	}
}

func TestPrepareKiroLaunchRejectsEmptyNativeLogPath(t *testing.T) {
	cfg := config.Config{KiroCmd: "kiro-cli", KiroCWD: t.TempDir()}
	err := prepareKiroLaunch(cfg, testutil.Logger(t))
	if err == nil || !strings.Contains(err.Error(), "Kiro log path is empty") {
		t.Fatalf("error=%v", err)
	}
}

// TestApp_NoKiroCmd_StartsHealthOnly — when KIRO_CMD is empty, newApp
// succeeds with pool == nil and the server is constructable. /health
// serves 200 with the zero PoolStats envelope. Proves the Phase 1
// review-fix posture (gateway boots without kiro-cli installed).
func TestApp_NoKiroCmd_StartsHealthOnly(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:         ":0",
		KiroCmd:          "", // explicit — Phase 1 review-fix branch
		PoolSize:         1,
		PingInterval:     60 * time.Second,
		OllamaPathPrefix: "/api",
	}
	logger := testutil.Logger(t)

	a, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()

	if a.pool != nil {
		t.Errorf("a.pool: got non-nil, want nil (KIRO_CMD unset)")
	}
	if a.engine != nil {
		t.Errorf("a.engine: got non-nil, want nil (no pool means no engine)")
	}
	if a.srv == nil {
		t.Fatal("a.srv: nil — server must be constructable in degraded mode")
	}

	// /health must serve 200 even without a pool.
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	a.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("/health status: got %d, want 200 (degraded mode)", w.Code)
	}
}

// TestApp_ZeroPoolSizeSkipsWarmPool proves the application preserves the
// resolved zero value instead of forwarding it to pool.New, whose package-level
// defensive default intentionally turns Config{Size: 0} into one slot.
func TestApp_ZeroPoolSizeSkipsWarmPool(t *testing.T) {
	tests := []struct {
		name     string
		envValue string
		args     []string
	}{
		{name: "environment", envValue: "0"},
		{name: "explicit CLI", envValue: "2", args: []string{"--pool-size", "0"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HTTP_ADDR", "127.0.0.1:0")
			t.Setenv("KIRO_CMD", "/usr/bin/true")
			t.Setenv("KIRO_CWD", root)
			t.Setenv("KIRO_CHAT_LOG_FILE", filepath.Join(root, "logs", "kiro-chat.log"))
			t.Setenv("POOL_SIZE", tc.envValue)

			cfg, err := config.LoadArgs(tc.args)
			if err != nil {
				t.Fatalf("config.LoadArgs: %v", err)
			}
			if cfg.PoolSize != 0 {
				t.Fatalf("resolved PoolSize = %d, want 0", cfg.PoolSize)
			}

			a, cleanup, err := newApp(context.Background(), cfg, testutil.Logger(t))
			if err != nil {
				t.Fatalf("newApp: %v", err)
			}
			defer cleanup()
			if a.pool != nil {
				t.Fatal("a.pool is non-nil; zero must disable the warm pool")
			}
			if a.engine != nil {
				t.Fatal("a.engine is non-nil; no warm pool means no pooled engine")
			}
			if a.registry == nil {
				t.Fatal("a.registry is nil; zero disables only the warm pool")
			}

			req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/admin/api/snapshot", nil)
			rec := httptest.NewRecorder()
			a.srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /admin/api/snapshot: got %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var snap admin.Snapshot
			if err := json.NewDecoder(rec.Body).Decode(&snap); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if snap.Pool.Size != 0 || len(snap.Pool.Slots) != 0 {
				t.Fatalf("snapshot pool = size %d, %d slots; want empty", snap.Pool.Size, len(snap.Pool.Slots))
			}
		})
	}
}

// TestNewApp_SurfaceGating — Phase 3.1 Plan 04 Task 1 (B3 closure).
//
// Verifies that ENABLED_SURFACES controls which adapter routes the
// gateway mounts. Three env permutations are exercised under the
// degraded `KIRO_CMD=""` posture (pool + engine are nil, warmup is
// skipped entirely — mirrors TestApp_NoKiroCmd_StartsHealthOnly):
//
//   - Default (ENABLED_SURFACES unset → ollama,anthropic): both
//     /api/chat AND /v1/messages MUST be mounted (probe returns
//     non-404 — in degraded mode, the nil-engine guard returns 503).
//   - OllamaOnly (ENABLED_SURFACES=ollama): /api/chat mounted,
//     /v1/messages route is absent and chi returns 404.
//   - AnthropicOnly (ENABLED_SURFACES=anthropic): /v1/messages
//     mounted, /api/chat absent (404).
//
// The test uses t.Setenv → config.Load → newApp → a.srv.ServeHTTP so
// the env-resolved cfg drives the wiring. AUTH_TOKEN is cleared so
// auth-protected routes return 503 (nil-engine) instead of 401
// (auth-fail), which would also be non-404 but would obscure whether
// the route was actually mounted.
//
// Threat mitigation: T-3.1-WIRE — closes the verification gap that
// previously would only have surfaced in HUMAN-UAT.
func TestNewApp_SurfaceGating(t *testing.T) {
	subtests := []struct {
		name                 string
		enabledSurfaces      string // "" sentinel meaning "do not set ENABLED_SURFACES"
		expectOllamaRoute    bool
		expectAnthropicRoute bool
	}{
		{"Default", "", true, true},
		{"OllamaOnly", "ollama", true, false},
		{"AnthropicOnly", "anthropic", false, true},
	}

	for _, tc := range subtests {
		t.Run(tc.name, func(t *testing.T) {
			// Degraded mode: pool + engine stay nil. Warmup is skipped.
			// KIRO_CMD must resolve to a real executable so config.Load's
			// REL-CFG-06 exec.LookPath check passes on hosts without
			// kiro-cli on PATH (e.g. CI runners). The value is otherwise
			// irrelevant here — cfg.KiroCmd is zeroed after Load (below)
			// to force degraded mode. /usr/bin/true matches the
			// resolvable-binary convention used by TestApp_WarmupBeforeListen.
			t.Setenv("KIRO_CMD", "/usr/bin/true")
			// Defensive — ephemeral port; the test never actually listens.
			t.Setenv("HTTP_ADDR", ":0")
			// No-auth so route probes are not blocked by 401 before
			// reaching the chi router's 404 handler.
			t.Setenv("AUTH_TOKEN", "")
			if tc.enabledSurfaces != "" {
				t.Setenv("ENABLED_SURFACES", tc.enabledSurfaces)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			// Force degraded mode AFTER config.Load: an empty KIRO_CMD
			// falls back to the "kiro-cli" default (which config.Load's
			// REL-CFG-06 PATH validation now rejects on hosts lacking it),
			// so we cannot disable pool construction via env alone.
			// Overriding the resolved Config field is the supported
			// degraded-mode entrypoint (mirrors how
			// TestApp_NoKiroCmd_StartsHealthOnly builds its cfg literal).
			cfg.KiroCmd = ""
			logger := testutil.Logger(t)

			a, cleanup, err := newApp(context.Background(), cfg, logger)
			if err != nil {
				t.Fatalf("newApp: %v", err)
			}
			defer cleanup()
			if a == nil || a.srv == nil {
				t.Fatalf("newApp: nil app or srv")
			}

			probes := []struct {
				path     string
				expected bool // true = mounted (non-404), false = absent (404)
			}{
				{"/api/chat", tc.expectOllamaRoute},
				{"/v1/messages", tc.expectAnthropicRoute},
			}
			for _, p := range probes {
				req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, p.path, nil)
				w := httptest.NewRecorder()
				a.srv.ServeHTTP(w, req)

				if p.expected {
					// Route mounted — any non-404 proves the path
					// is registered (503 nil-engine, 405 method-not-
					// allowed on GET, etc. all qualify).
					if w.Code == http.StatusNotFound {
						t.Errorf("path %s: got 404, want non-404 (route should be mounted under %s)",
							p.path, tc.name)
					}
				} else {
					// Route absent — chi returns 404 for unmatched paths.
					if w.Code != http.StatusNotFound {
						t.Errorf("path %s: got %d, want 404 (route should NOT be mounted under %s)",
							p.path, w.Code, tc.name)
					}
				}
			}
		})
	}
}

// TestApp_WarmupBeforeListen — when KIRO_CMD is set to a binary that
// CANNOT speak ACP (e.g., /bin/true exits 0 immediately), Warmup MUST
// fail and newApp MUST return an error WITHOUT constructing the
// server. Proves POOL-02 ordering AND the warmup-deadline guard
// (threat T-02-36).
func TestApp_WarmupBeforeListen(t *testing.T) {
	if _, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil); err != nil {
		// Defensive — should not happen, but keep the test honest.
		t.Fatalf("http.NewRequestWithContext: %v", err)
	}

	logFile := filepath.Join(t.TempDir(), "logs", "kiro-chat.log")
	cfg := config.Config{
		HTTPAddr:         ":0",
		KiroCmd:          "/usr/bin/true", // exists on macOS + Linux; speaks no ACP
		KiroArgs:         []string{},
		KiroChatLogFile:  logFile,
		KiroChatLogPath:  logFile,
		PoolSize:         1,
		PingInterval:     60 * time.Second,
		OllamaPathPrefix: "/api",
	}
	logger := testutil.Logger(t)

	// Use a short ctx (5s) so the test bounds itself even if a
	// pathological /usr/bin/true variant somehow accepts the stdin
	// pipe — Initialize will still fail (no agentInitialize response
	// will come back) and pool.Warmup will return an error.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a, cleanup, err := newApp(ctx, cfg, logger)
	defer cleanup()
	if err == nil {
		t.Fatal("newApp returned nil error; expected pool.Warmup to fail against /usr/bin/true (POOL-02 ordering broken)")
	}
	if a != nil {
		t.Error("newApp returned non-nil app on Warmup failure; the server MUST NOT be constructed when warmup fails")
	}
}

// TestApp_DefaultHookChain_AllSixHooksPresent — regression guard for
// the v1.8.2 install-template bug where ENABLED_HOOKS=RequestIDHook,
// AuthHook,PIIRedactionHook,LoggingHook silently filtered
// JSONFormatSteeringHook out of the chain, breaking LangFlow Ollama
// JSON-format steering.
//
// Default cfg (no ENABLED_HOOKS override) MUST yield a chain with all
// six registered hooks at /health/hooks, in registration order:
//
//	RequestIDHook, AuthHook, JSONFormatSteeringHook,
//	CompressionHook, PIIRedactionHook, LoggingHook.
//
// CompressionHook (context-compression feature) joined the default
// chain after JSONFormatSteeringHook did; it is subject to the same
// two-knob model — an explicit ENABLED_HOOKS allowlist that predates
// CompressionHook (e.g. the five legacy names) silently drops it from
// the chain, mirroring the original JSONFormatSteeringHook bug this
// test guards against. See TestApp_ExplicitAllowlist_OmitsCompressionHook
// below for that case.
//
// Runs under the degraded KIRO_CMD="" posture (no pool / no engine);
// /health/hooks is wired off the chain directly so it serves the
// full picture even without an upstream worker.
func TestApp_DefaultHookChain_AllSixHooksPresent(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:                  ":0",
		KiroCmd:                   "", // degraded mode — same posture as TestApp_NoKiroCmd_StartsHealthOnly
		PoolSize:                  1,
		PingInterval:              60 * time.Second,
		OllamaPathPrefix:          "/api",
		OpenAIPathPrefix:          "/v1",
		AnthropicPathPrefix:       "/v1",
		JSONFormatSteeringEnabled: true, // hook is in the chain regardless; enabled controls work-doing
		// ENABLED_HOOKS intentionally empty — proves the empty-means-all
		// semantics is the active default for fresh installs.
	}
	logger := testutil.Logger(t)

	a, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()
	if a.srv == nil {
		t.Fatal("a.srv: nil — server must be constructable in degraded mode")
	}

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	a.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/health/hooks status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Hooks []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"hooks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health/hooks: %v", err)
	}

	wantOrder := []string{
		"RequestIDHook",
		"AuthHook",
		"JSONFormatSteeringHook",
		"CompressionHook",
		"PIIRedactionHook",
		"LoggingHook",
	}
	if len(body.Hooks) < len(wantOrder) {
		t.Fatalf("hooks count: got %d, want >= %d (%v); body=%+v",
			len(body.Hooks), len(wantOrder), wantOrder, body.Hooks)
	}
	for i, want := range wantOrder {
		if body.Hooks[i].Name != want {
			t.Errorf("hooks[%d].name: got %q, want %q (registration order)",
				i, body.Hooks[i].Name, want)
		}
	}
}

// TestApp_ExplicitAllowlist_OmitsCompressionHook — the two-knob-model
// case TestApp_DefaultHookChain_AllSixHooksPresent's doc comment
// references: an explicit ENABLED_HOOKS allowlist listing only the five
// legacy hook names (predating CompressionHook) must yield a chain
// WITHOUT CompressionHook. ENABLED_HOOKS is the hard kill switch — an
// allowlist that does not name a hook omits it, silently, by design.
func TestApp_ExplicitAllowlist_OmitsCompressionHook(t *testing.T) {
	cfg := config.Config{
		HTTPAddr:                  ":0",
		KiroCmd:                   "", // degraded mode — same posture as TestApp_NoKiroCmd_StartsHealthOnly
		PoolSize:                  1,
		PingInterval:              60 * time.Second,
		OllamaPathPrefix:          "/api",
		OpenAIPathPrefix:          "/v1",
		AnthropicPathPrefix:       "/v1",
		JSONFormatSteeringEnabled: true, // hook is in the chain regardless; enabled controls work-doing
		EnabledHooks: []string{
			"RequestIDHook",
			"AuthHook",
			"JSONFormatSteeringHook",
			"PIIRedactionHook",
			"LoggingHook",
		},
	}
	logger := testutil.Logger(t)

	a, cleanup, err := newApp(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	defer cleanup()
	if a.srv == nil {
		t.Fatal("a.srv: nil — server must be constructable in degraded mode")
	}

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/health/hooks", nil)
	w := httptest.NewRecorder()
	a.srv.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("/health/hooks status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Hooks []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"hooks"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health/hooks: %v", err)
	}

	for _, h := range body.Hooks {
		if h.Name == "CompressionHook" {
			t.Fatalf("CompressionHook present in chain despite explicit ENABLED_HOOKS allowlist that omits it; hooks=%+v", body.Hooks)
		}
	}
}
