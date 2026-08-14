package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/adapter/ollama"
	"otto-gateway/internal/adapter/openai"
	"otto-gateway/internal/admin"
	"otto-gateway/internal/canonical"
	"otto-gateway/internal/pool"
	"otto-gateway/internal/registry"
	"otto-gateway/internal/testutil"
)

type mutableModelCatalogRuntime struct {
	mu sync.Mutex

	snapshot       pool.ModelCatalogSnapshot
	refreshModels  []canonical.ModelInfo
	refreshResult  pool.CatalogRefreshResult
	refreshErr     error
	snapshotCalls  int
	refreshCalls   int
	modelReadCalls int
}

func (f *mutableModelCatalogRuntime) Models() []canonical.ModelInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.modelReadCalls++
	return cloneModelInfos(f.snapshot.Models)
}

func (f *mutableModelCatalogRuntime) CatalogSnapshot() pool.ModelCatalogSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotCalls++
	snapshot := f.snapshot
	snapshot.Models = cloneModelInfos(snapshot.Models)
	return snapshot
}

func (f *mutableModelCatalogRuntime) RefreshModelCatalog(context.Context) (pool.CatalogRefreshResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshCalls++
	if f.refreshErr != nil {
		return f.refreshResult, f.refreshErr
	}
	if f.refreshModels != nil {
		f.snapshot.Models = normalizeMutableRuntimeModels(f.refreshModels)
		f.snapshot.Generation++
		f.snapshot.LastOutcome = f.refreshResult.Outcome
	}
	return f.refreshResult, nil
}

func (f *mutableModelCatalogRuntime) callCounts() (snapshot, refresh, models int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshotCalls, f.refreshCalls, f.modelReadCalls
}

func cloneModelInfos(models []canonical.ModelInfo) []canonical.ModelInfo {
	return append([]canonical.ModelInfo(nil), models...)
}

func normalizeMutableRuntimeModels(models []canonical.ModelInfo) []canonical.ModelInfo {
	seen := make(map[string]struct{}, len(models))
	normalized := make([]canonical.ModelInfo, 0, len(models))
	for _, model := range models {
		if model.ID == "" || model.ID == "auto" {
			continue
		}
		if _, duplicate := seen[model.ID]; duplicate {
			continue
		}
		seen[model.ID] = struct{}{}
		normalized = append(normalized, model)
	}
	return normalized
}

func loadModelCapabilityRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	return reg
}

func TestAdminModelCatalogAdapter_SnapshotEnrichesOneCopiedSnapshot(t *testing.T) {
	location := time.FixedZone("UTC+05:30", 5*60*60+30*60)
	runtime := &mutableModelCatalogRuntime{snapshot: pool.ModelCatalogSnapshot{
		Models: []canonical.ModelInfo{
			{ID: "claude-sonnet-5", Name: "Live Sonnet"},
			{ID: "gpt-5.6-sol", Name: "Live Sol"},
			{ID: "qwen3-coder-next", Name: "Live Qwen"},
		},
		Generation:      7,
		RefreshInterval: 15 * time.Minute,
		LastAttemptAt:   time.Date(2026, 8, 13, 20, 1, 2, 0, location),
		LastSuccessAt:   time.Date(2026, 8, 13, 20, 1, 3, 0, location),
		LastUpdatedAt:   time.Date(2026, 8, 13, 20, 1, 4, 0, location),
		NextAttemptAt:   time.Date(2026, 8, 13, 20, 16, 2, 0, location),
		LastOutcome:     pool.CatalogExpanded,
	}}
	adapter := adminModelCatalogAdapter{
		source: runtime,
		reg:    loadModelCapabilityRegistry(t),
		now:    func() time.Time { return time.Date(2026, 8, 13, 15, 0, 0, 0, time.UTC) },
	}

	got := adapter.Snapshot()

	if snapshotCalls, refreshCalls, _ := runtime.callCounts(); snapshotCalls != 1 || refreshCalls != 0 {
		t.Fatalf("runtime calls = snapshot %d, refresh %d; want 1, 0", snapshotCalls, refreshCalls)
	}
	if got.State != "ready" || got.Generation != 7 {
		t.Fatalf("state/generation = %q/%d; want ready/7", got.State, got.Generation)
	}
	if got.Count != len(got.Models) || got.Count != 4 {
		t.Fatalf("count/models = %d/%d; want 4/4", got.Count, len(got.Models))
	}
	autoCount := 0
	for _, model := range got.Models {
		if model.ID == "auto" {
			autoCount++
		}
	}
	if autoCount != 1 || got.Models[0].ID != "auto" {
		t.Fatalf("auto count/first model = %d/%q; want 1/auto", autoCount, got.Models[0].ID)
	}

	wantCapabilities := map[string]map[string]string{
		"auto": {
			"completion": "unknown", "tools": "unknown", "vision": "unknown", "reasoning": "unknown",
		},
		"claude-sonnet-5": {
			"completion": "supported", "tools": "supported", "vision": "supported", "reasoning": "supported",
		},
		"gpt-5.6-sol": {
			"completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown",
		},
		"qwen3-coder-next": {
			"completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown",
		},
	}
	for _, model := range got.Models {
		if diff := reflect.DeepEqual(model.Capabilities, wantCapabilities[model.ID]); !diff {
			t.Errorf("capabilities[%s] = %#v; want %#v", model.ID, model.Capabilities, wantCapabilities[model.ID])
		}
	}

	wantRefresh := admin.ModelCatalogRefreshView{
		Enabled:         true,
		IntervalSeconds: 900,
		LastAttemptAt:   "2026-08-13T14:31:02Z",
		LastSuccessAt:   "2026-08-13T14:31:03Z",
		LastUpdatedAt:   "2026-08-13T14:31:04Z",
		NextAttemptAt:   "2026-08-13T14:46:02Z",
		LastOutcome:     "expanded",
	}
	if !reflect.DeepEqual(got.Refresh, wantRefresh) {
		t.Fatalf("refresh = %#v; want %#v", got.Refresh, wantRefresh)
	}
}

func TestAdminModelCatalogAdapter_StatePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		snapshot pool.ModelCatalogSnapshot
		want     string
	}{
		{
			name: "refreshing wins over every other state",
			snapshot: pool.ModelCatalogSnapshot{
				InProgress: true, PendingRemovals: 2,
			},
			want: "refreshing",
		},
		{
			name:     "pending shrink wins over degraded and disabled",
			snapshot: pool.ModelCatalogSnapshot{PendingRemovals: 2},
			want:     "pending_shrink",
		},
		{
			name:     "no explicit catalog is degraded",
			snapshot: pool.ModelCatalogSnapshot{},
			want:     "degraded",
		},
		{
			name: "healthy catalog with scheduler off is disabled",
			snapshot: pool.ModelCatalogSnapshot{
				Models: []canonical.ModelInfo{{ID: "claude-sonnet-5"}},
			},
			want: "disabled",
		},
		{
			name: "healthy scheduled catalog is ready",
			snapshot: pool.ModelCatalogSnapshot{
				Models: []canonical.ModelInfo{{ID: "claude-sonnet-5"}}, RefreshInterval: time.Minute,
			},
			want: "ready",
		},
	}

	reg := loadModelCapabilityRegistry(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &mutableModelCatalogRuntime{snapshot: tc.snapshot}
			got := (adminModelCatalogAdapter{source: runtime, reg: reg, now: time.Now}).Snapshot()
			if got.State != tc.want {
				t.Fatalf("state = %q; want %q", got.State, tc.want)
			}
			if snapshotCalls, refreshCalls, _ := runtime.callCounts(); snapshotCalls != 1 || refreshCalls != 0 {
				t.Fatalf("runtime calls = snapshot %d, refresh %d; want 1, 0", snapshotCalls, refreshCalls)
			}
		})
	}
}

func TestAdminModelCatalogAdapter_RefreshMapsBoundedResults(t *testing.T) {
	const rawSecret = "upstream path /Users/operator and AUTH_TOKEN=secret"
	tests := []struct {
		name        string
		result      pool.CatalogRefreshResult
		err         error
		wantCode    string
		wantResult  string
		wantRetry   int
		wantMessage string
	}{
		{
			name: "success", result: pool.CatalogRefreshResult{
				Outcome: pool.CatalogExpanded, RetryAfter: 29*time.Second + time.Nanosecond,
			}, wantResult: "expanded", wantRetry: 30,
		},
		{
			name: "in progress", err: fmt.Errorf("outer: %w", &pool.CatalogRefreshError{
				Kind: pool.ErrCatalogRefreshInProgress, RetryAfter: 1501 * time.Millisecond,
			}), wantCode: "catalog_refresh_in_progress", wantRetry: 2,
		},
		{
			name: "cooldown", err: fmt.Errorf("outer: %w", &pool.CatalogRefreshError{
				Kind: pool.ErrCatalogRefreshCooldown, RetryAfter: 29*time.Second + time.Nanosecond,
			}), wantCode: "catalog_refresh_cooldown", wantRetry: 30,
		},
		{
			name: "busy", err: fmt.Errorf("outer: %w", &pool.CatalogRefreshError{
				Kind: pool.ErrCatalogRefreshBusy, RetryAfter: 29*time.Second + time.Nanosecond,
			}), wantCode: "catalog_refresh_busy", wantRetry: 30,
			wantMessage: "No idle gateway worker is available for a model catalog refresh. The current catalog remains in use.",
		},
		{
			name: "unavailable", err: fmt.Errorf("outer: %w", &pool.CatalogRefreshError{
				Kind: pool.ErrCatalogRefreshUnavailable, RetryAfter: 30 * time.Second,
			}), wantCode: "catalog_refresh_unavailable", wantRetry: 30,
		},
		{
			name: "post-admission probe error", err: fmt.Errorf("outer: %w", &pool.CatalogRefreshError{
				Kind: errors.New(rawSecret), RetryAfter: 29*time.Second + time.Nanosecond,
			}), wantCode: "catalog_refresh_failed", wantRetry: 30,
			wantMessage: "Model catalog refresh failed. The current catalog remains in use.",
		},
		{
			name: "untyped pre-admission error", err: errors.New(rawSecret), wantCode: "catalog_refresh_failed",
			wantMessage: "Model catalog refresh failed. The current catalog remains in use.",
		},
	}

	reg := loadModelCapabilityRegistry(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtime := &mutableModelCatalogRuntime{refreshResult: tc.result, refreshErr: tc.err}
			got := (adminModelCatalogAdapter{source: runtime, reg: reg, now: time.Now}).Refresh(context.Background())
			if got.Code != tc.wantCode || got.Outcome != tc.wantResult || got.RetryAfterSeconds != tc.wantRetry {
				t.Fatalf("result = %#v; want code=%q outcome=%q retry=%d", got, tc.wantCode, tc.wantResult, tc.wantRetry)
			}
			if tc.wantMessage != "" && got.Message != tc.wantMessage {
				t.Fatalf("message = %q; want %q", got.Message, tc.wantMessage)
			}
			if snapshotCalls, refreshCalls, _ := runtime.callCounts(); snapshotCalls != 0 || refreshCalls != 1 {
				t.Fatalf("runtime calls = snapshot %d, refresh %d; want 0, 1", snapshotCalls, refreshCalls)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), rawSecret) {
				t.Fatalf("admin action leaked raw error: %s", raw)
			}
		})
	}
}

func TestLiveCatalogRefresh_ConvergesEveryModelSurface(t *testing.T) {
	initial := []canonical.ModelInfo{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"},
		{ID: "gpt-5.6-sol", Name: "GPT 5.6 Sol"},
	}
	expanded := append(cloneModelInfos(initial),
		canonical.ModelInfo{ID: "auto", Name: "Synthetic duplicate must be removed"},
		canonical.ModelInfo{ID: "Auto", Name: "Case-sensitive Auto"},
		canonical.ModelInfo{ID: "Auto", Name: "Exact duplicate loses"},
		canonical.ModelInfo{ID: "qwen3-coder-next", Name: "Qwen3 Coder Next"},
		canonical.ModelInfo{ID: "qwen3-coder-next", Name: "Exact duplicate loses"},
	)
	runtime := &mutableModelCatalogRuntime{
		snapshot: pool.ModelCatalogSnapshot{
			Models: initial, Generation: 1, RefreshInterval: 15 * time.Minute, LastOutcome: pool.CatalogStartup,
		},
		refreshModels: expanded,
		refreshResult: pool.CatalogRefreshResult{
			Outcome: pool.CatalogExpanded, PreviousCount: 2, CandidateCount: 7, PublishedCount: 4,
		},
	}
	sharedRegistry := loadModelCapabilityRegistry(t)
	adminHandler := admin.Handler(admin.Deps{
		Logger:       testutil.Logger(t),
		ModelCatalog: adminModelCatalogAdapter{source: runtime, reg: sharedRegistry, now: time.Now},
	})
	openAIAdapter := openai.New(openai.Config{
		Logger:            testutil.Logger(t),
		ModelCatalog:      runtime,
		ModelCapabilities: modelCapabilityCatalog{catalog: runtime, reg: sharedRegistry},
	})
	ollamaAdapter := ollama.New(ollama.Config{
		Logger:       testutil.Logger(t),
		ModelCatalog: runtime,
	})

	openAIRouter := chi.NewRouter()
	openAIRouter.Route("/v1", func(r chi.Router) { openAIAdapter.RegisterRoutes(r) })
	mux := http.NewServeMux()
	mux.Handle("/api/model-catalog", adminHandler)
	mux.Handle("/api/model-catalog/refresh", adminHandler)
	mux.Handle("/v1/", openAIRouter)
	mux.Handle("/api/", http.StripPrefix("/api", ollamaAdapter.ProtectedRouter()))
	server := httptest.NewServer(mux)
	defer server.Close()

	refreshRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/api/model-catalog/refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	refreshResponse, err := server.Client().Do(refreshRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = refreshResponse.Body.Close() }()
	if refreshResponse.StatusCode != http.StatusOK {
		t.Fatalf("refresh status = %d; want 200", refreshResponse.StatusCode)
	}

	want := []string{"auto", "claude-sonnet-5", "gpt-5.6-sol", "Auto", "qwen3-coder-next"}
	responses := map[string][]string{}

	var adminView admin.ModelCatalogView
	getModelCatalogJSON(t, server, "/api/model-catalog", &adminView)
	for _, model := range adminView.Models {
		responses["admin"] = append(responses["admin"], model.ID)
		if model.ID == "Auto" && model.SelectionMode != "explicit" {
			t.Errorf("admin Auto selection_mode = %q; want explicit", model.SelectionMode)
		}
	}

	var openAIModels struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	getModelCatalogJSON(t, server, "/v1/models", &openAIModels)
	for _, model := range openAIModels.Data {
		responses["openai_models"] = append(responses["openai_models"], model.ID)
	}

	var capabilityModels struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	getModelCatalogJSON(t, server, "/v1/model-capabilities", &capabilityModels)
	for _, model := range capabilityModels.Data {
		responses["openai_capabilities"] = append(responses["openai_capabilities"], model.ID)
	}

	var ollamaTags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	getModelCatalogJSON(t, server, "/api/tags", &ollamaTags)
	for _, model := range ollamaTags.Models {
		responses["ollama_tags"] = append(responses["ollama_tags"], model.Name)
	}

	for surface, got := range responses {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s IDs = %v; want %v", surface, got, want)
		}
		autoCount := 0
		for _, id := range got {
			if id == "auto" {
				autoCount++
			}
		}
		if autoCount != 1 {
			t.Errorf("%s auto count = %d; want 1", surface, autoCount)
		}
	}
	if snapshotCalls, refreshCalls, modelReadCalls := runtime.callCounts(); snapshotCalls != 1 || refreshCalls != 1 || modelReadCalls != 3 {
		t.Fatalf("runtime calls = snapshot %d, refresh %d, models %d; want 1, 1, 3", snapshotCalls, refreshCalls, modelReadCalls)
	}
}

func getModelCatalogJSON(t *testing.T, server *httptest.Server, path string, target any) {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d; want 200", path, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode GET %s: %v", path, err)
	}
}
