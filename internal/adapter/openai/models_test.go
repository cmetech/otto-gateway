package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// ----------------------------------------------------------------------------
// Fake ModelCatalog for TestModels
// ----------------------------------------------------------------------------

// fakeCatalog is a test-local ModelCatalog implementation.
type fakeCatalog struct {
	models []canonical.ModelInfo
}

func (f *fakeCatalog) Models() []canonical.ModelInfo {
	return f.models
}

// mountedAdapterWithCatalog constructs a server with both an engine and a
// ModelCatalog injected, used by TestModels.
func mountedAdapterWithCatalog(catalog ModelCatalog) *httptest.Server {
	a := New(Config{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		ModelCatalog: catalog,
	})
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) {
		a.RegisterRoutes(sub)
	})
	return httptest.NewServer(r)
}

// TestModels covers GET /v1/models for both catalog-present and nil-catalog cases.
func TestModels(t *testing.T) {
	t.Run("catalog_present", func(t *testing.T) {
		catalog := &fakeCatalog{
			models: []canonical.ModelInfo{
				{ID: "foo"},
				{ID: "bar"},
			},
		}
		srv := mountedAdapterWithCatalog(catalog)
		defer srv.Close()

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet, srv.URL+"/v1/models", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}

		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}

		ct := resp.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type: got %q, want application/json prefix", ct)
		}

		var list modelList
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("decode modelList: %v", err)
		}

		// object must be "list"
		if list.Object != "list" {
			t.Errorf("object: got %q, want list", list.Object)
		}

		// data must start with "auto"
		if len(list.Data) == 0 {
			t.Fatal("data: empty")
		}
		if list.Data[0].ID != "auto" {
			t.Errorf("data[0].id: got %q, want auto", list.Data[0].ID)
		}

		// auto + foo + bar = 3 entries
		if len(list.Data) != 3 {
			t.Errorf("data length: got %d, want 3 (auto+foo+bar)", len(list.Data))
		}

		// catalog entries follow in order
		if list.Data[1].ID != "foo" {
			t.Errorf("data[1].id: got %q, want foo", list.Data[1].ID)
		}
		if list.Data[2].ID != "bar" {
			t.Errorf("data[2].id: got %q, want bar", list.Data[2].ID)
		}

		// each entry has required fields
		for i, info := range list.Data {
			if info.Object != "model" {
				t.Errorf("data[%d].object: got %q, want model", i, info.Object)
			}
			if info.OwnedBy == "" {
				t.Errorf("data[%d].owned_by: empty", i)
			}
			if info.Created == 0 {
				t.Errorf("data[%d].created: got 0, want non-zero unix timestamp", i)
			}
		}
	})

	t.Run("nil_catalog_only_auto", func(t *testing.T) {
		// nil ModelCatalog → only "auto" returned
		srv := mountedAdapterWithCatalog(nil)
		defer srv.Close()

		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodGet, srv.URL+"/v1/models", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}

		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}

		var list modelList
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("decode modelList: %v", err)
		}

		if list.Object != "list" {
			t.Errorf("object: got %q, want list", list.Object)
		}

		// exactly one entry: "auto"
		if len(list.Data) != 1 {
			t.Errorf("data length: got %d, want 1 (only auto)", len(list.Data))
		}
		if list.Data[0].ID != "auto" {
			t.Errorf("data[0].id: got %q, want auto", list.Data[0].ID)
		}
		if list.Data[0].Object != "model" {
			t.Errorf("data[0].object: got %q, want model", list.Data[0].Object)
		}
	})
}

// TestModelListRender covers catalogToModelList directly:
// prepends "auto", maps each entry, applies owned_by and created.
func TestModelListRender(t *testing.T) {
	t.Run("prepends_auto", func(t *testing.T) {
		models := []canonical.ModelInfo{
			{ID: "m1", Name: "Model One"},
			{ID: "m2", Name: "Model Two"},
		}
		list := catalogToModelList(models, "kiro", 1234567890)
		if list.Object != "list" {
			t.Errorf("object: got %q, want list", list.Object)
		}
		if len(list.Data) != 3 {
			t.Fatalf("data length: got %d, want 3", len(list.Data))
		}
		if list.Data[0].ID != "auto" {
			t.Errorf("first entry id: got %q, want auto", list.Data[0].ID)
		}
	})

	t.Run("catalog_ids_in_order", func(t *testing.T) {
		models := []canonical.ModelInfo{
			{ID: "alpha"},
			{ID: "beta"},
			{ID: "gamma"},
		}
		list := catalogToModelList(models, "otto-gateway", 100)
		// data = [auto, alpha, beta, gamma]
		wantIDs := []string{"auto", "alpha", "beta", "gamma"}
		for i, want := range wantIDs {
			if list.Data[i].ID != want {
				t.Errorf("data[%d].id: got %q, want %q", i, list.Data[i].ID, want)
			}
		}
	})

	t.Run("each_entry_fields", func(t *testing.T) {
		list := catalogToModelList([]canonical.ModelInfo{{ID: "x"}}, "kiro", 9999)
		for i, info := range list.Data {
			if info.Object != "model" {
				t.Errorf("data[%d].object: got %q, want model", i, info.Object)
			}
			if info.OwnedBy != "kiro" {
				t.Errorf("data[%d].owned_by: got %q, want kiro", i, info.OwnedBy)
			}
			if info.Created != 9999 {
				t.Errorf("data[%d].created: got %d, want 9999", i, info.Created)
			}
		}
	})

	t.Run("empty_catalog_only_auto", func(t *testing.T) {
		list := catalogToModelList(nil, "kiro", 1)
		if len(list.Data) != 1 || list.Data[0].ID != "auto" {
			t.Errorf("empty catalog: got %+v, want only auto entry", list.Data)
		}
	})
}

// TestModels_ModelCatalogSourcing verifies that handleModels sources from
// the injected ModelCatalog (SC3 same-set as /api/tags by construction).
// This test asserts the ModelCatalog interface is used, not a static list.
func TestModels_ModelCatalogSourcing(t *testing.T) {
	// Use a catalog with an unusual model name to prove we source from it.
	catalog := &fakeCatalog{
		models: []canonical.ModelInfo{
			{ID: "unique-model-xyz-123"},
		},
	}
	srv := mountedAdapterWithCatalog(catalog)
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, srv.URL+"/v1/models", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var list modelList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Must contain "unique-model-xyz-123" proving dynamic sourcing, not static list.
	found := false
	for _, m := range list.Data {
		if m.ID == "unique-model-xyz-123" {
			found = true
			break
		}
	}
	if !found {
		ids := make([]string, len(list.Data))
		for i, m := range list.Data {
			ids[i] = m.ID
		}
		t.Errorf("catalog model 'unique-model-xyz-123' not in response; got ids: %v", ids)
	}
}

// Unused suppressor for the bytes import that the completions test below will use.
var _ = bytes.NewReader
