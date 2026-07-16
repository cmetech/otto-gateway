package openai

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/canonical"
)

// fakeCapCatalog is a test-local ModelCapabilityCatalog.
type fakeCapCatalog struct{ cat canonical.CapabilityCatalog }

func (f *fakeCapCatalog) ModelCapabilities() canonical.CapabilityCatalog { return f.cat }

func mountedCapAdapter(seam ModelCapabilityCatalog) *httptest.Server {
	a := New(Config{
		Logger:            slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{})),
		ModelCapabilities: seam,
	})
	r := chi.NewRouter()
	r.Route("/v1", func(sub chi.Router) { a.RegisterRoutes(sub) })
	return httptest.NewServer(r)
}

func sampleCatalog() canonical.CapabilityCatalog {
	return canonical.CapabilityCatalog{
		RegistryRevision: "sha256-abc",
		GeneratedAt:      time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
		Entries: []canonical.ModelCapability{
			{
				ID: "auto", Name: "Automatic", Available: true, SelectionMode: "automatic",
				Capabilities: map[string]canonical.CapabilityState{"completion": "unknown", "tools": "unknown", "vision": "unknown", "reasoning": "unknown"},
				Evidence:     map[string]canonical.Evidence{},
			},
			{
				ID: "claude-opus-4.8", Name: "Claude Opus 4.8", Available: true, SelectionMode: "explicit",
				Capabilities: map[string]canonical.CapabilityState{"completion": "supported", "tools": "unknown", "vision": "unknown", "reasoning": "unknown"},
				Evidence:     map[string]canonical.Evidence{"completion": {Source: "kiro_declared", Reference: "live catalog", VerifiedAt: "2026-07-16"}},
			},
			{
				ID: "ghost", Name: "Ghost", Available: true, SelectionMode: "explicit",
				Capabilities: map[string]canonical.CapabilityState{"completion": "unknown", "tools": "unknown", "vision": "unknown", "reasoning": "unknown"},
				Evidence:     map[string]canonical.Evidence{},
			},
		},
	}
}

// capListWire mirrors the response for decoding in tests.
type capListWire struct {
	Object           string `json:"object"`
	RegistryRevision string `json:"registry_revision"`
	GeneratedAt      string `json:"generated_at"`
	Data             []struct {
		ID            string                       `json:"id"`
		Name          string                       `json:"name"`
		Available     bool                         `json:"available"`
		SelectionMode string                       `json:"selection_mode"`
		Capabilities  map[string]string            `json:"capabilities"`
		Evidence      map[string]map[string]string `json:"evidence"`
	} `json:"data"`
}

func getCaps(t *testing.T, srv *httptest.Server) (*http.Response, capListWire) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/model-capabilities", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	var out capListWire
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, out
}

func TestModelCapabilities_Shape(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: got %q", ct)
	}
	if list.Object != "list" {
		t.Errorf("object: got %q, want list", list.Object)
	}
	if list.RegistryRevision != "sha256-abc" {
		t.Errorf("registry_revision: got %q", list.RegistryRevision)
	}
	if list.GeneratedAt != "2026-07-16T12:00:00Z" {
		t.Errorf("generated_at: got %q, want RFC3339 UTC", list.GeneratedAt)
	}
	if len(list.Data) == 0 || list.Data[0].ID != "auto" || list.Data[0].SelectionMode != "automatic" {
		t.Fatalf("auto not first/automatic: %+v", list.Data)
	}
}

func TestModelCapabilities_RegisteredStates(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()

	var found bool
	for _, e := range list.Data {
		if e.ID == "claude-opus-4.8" {
			found = true
			if e.Capabilities["completion"] != "supported" {
				t.Errorf("completion: got %q, want supported", e.Capabilities["completion"])
			}
			if _, ok := e.Evidence["completion"]; !ok {
				t.Errorf("completion evidence missing")
			}
		}
	}
	if !found {
		t.Fatal("claude-opus-4.8 not in response")
	}
}

func TestModelCapabilities_UnknownModel(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()

	for _, e := range list.Data {
		if e.ID == "ghost" {
			for k, v := range e.Capabilities {
				if v != "unknown" {
					t.Errorf("ghost cap %q: got %q, want unknown", k, v)
				}
			}
			if len(e.Evidence) != 0 {
				t.Errorf("ghost evidence should be empty")
			}
		}
	}
}

func TestModelCapabilities_NilSeamEmptyList(t *testing.T) {
	srv := mountedCapAdapter(nil)
	defer srv.Close()
	resp, list := getCaps(t, srv)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	if list.Object != "list" {
		t.Errorf("object: got %q, want list", list.Object)
	}
	if len(list.Data) != 0 {
		t.Errorf("nil seam should yield empty data, got %d entries", len(list.Data))
	}
}

func TestModelCapabilities_NoLeakage(t *testing.T) {
	srv := mountedCapAdapter(&fakeCapCatalog{cat: sampleCatalog()})
	defer srv.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/model-capabilities", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	for _, banned := range []string{"KIRO_", "worker", "slot", "/Users/", "AUTH_TOKEN", "prompt"} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("response leaks %q: %s", banned, raw)
		}
	}
}
