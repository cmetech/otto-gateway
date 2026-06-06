//go:build darwin || windows

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestStatusClient_HealthOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	if !c.healthOK() {
		t.Fatalf("expected healthOK to be true")
	}
}

func TestStatusClient_HealthBadStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})) //nolint:bodyclose // handler writes nothing
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	if c.healthOK() {
		t.Fatalf("expected healthOK false on 503")
	}
}

func TestStatusClient_HealthUnreachable(t *testing.T) {
	c := newStatusClient("http://127.0.0.1:1", 200*time.Millisecond)
	if c.healthOK() {
		t.Fatalf("expected healthOK false on connection refused")
	}
}

func TestStatusClient_SnapshotParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"status": "ok",
			"uptime_seconds": 4283.5,
			"pool": {"size": 4, "alive": 4, "busy": 0}
		}`))
	}))
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	snap, err := c.snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.UptimeSeconds != 4283.5 {
		t.Errorf("uptime: got %v, want 4283.5", snap.UptimeSeconds)
	}
	if snap.PoolAlive != 4 || snap.PoolSize != 4 {
		t.Errorf("pool: got alive=%d size=%d, want 4/4", snap.PoolAlive, snap.PoolSize)
	}
}

func TestStatusClient_HooksParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"hooks": [
				{"name":"auth","kind":"Pre","enabled":true,"config":{}},
				{"name":"pii","kind":"Pre,Post","enabled":true,"config":{}},
				{"name":"logging","kind":"Pre,Post","enabled":false,"config":{}}
			]
		}`))
	}))
	defer srv.Close()
	c := newStatusClient(srv.URL, 1*time.Second)
	hooks, err := c.hooks()
	if err != nil {
		t.Fatalf("hooks: %v", err)
	}
	if len(hooks) != 3 {
		t.Fatalf("hooks count: got %d, want 3", len(hooks))
	}
	if hooks[0].Name != "auth" || !hooks[0].Enabled {
		t.Errorf("hook[0]: got %+v, want auth enabled", hooks[0])
	}
	if hooks[2].Enabled {
		t.Errorf("hook[2] (logging): expected disabled, got enabled")
	}
}
