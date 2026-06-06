//go:build darwin || windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Snapshot is the subset of /admin/api/snapshot the tray surfaces.
// Field names track the gateway's JSON shape — see
// internal/admin/snapshot.go for the source of truth. PoolAlive and
// PoolSize are denormalized from the nested pool object so the FSM
// does not have to know the JSON layout.
type Snapshot struct {
	Status        string    `json:"status"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	Pool          PoolStats `json:"pool"`

	// Convenience accessors populated by snapshot() — JSON-skipped.
	PoolAlive int `json:"-"`
	PoolSize  int `json:"-"`

	// Hooks is the /health/hooks side-fetched chain — populated by the
	// tray probe (NOT by the snapshot endpoint) so the FSM can degrade
	// on a non-empty LastError. Empty when /health/hooks is unreachable
	// or returns a non-2xx; the FSM treats absence as "no hook signal".
	Hooks []HookEntry `json:"-"`
}

// PoolStats mirrors internal/admin.SnapshotPool. Only Size/Alive
// matter for the tray's "pool N/M ready" header.
type PoolStats struct {
	Size  int `json:"size"`
	Alive int `json:"alive"`
	Busy  int `json:"busy"`
}

// HookEntry mirrors internal/server.HookDescription. The tray
// surfaces enabled count + name for the menu header AND uses
// LastError to drive the degraded state when an enabled hook has
// reported an error since its last successful invocation.
type HookEntry struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	LastError string `json:"last_error,omitempty"`
}

type hooksEnvelope struct {
	Hooks []HookEntry `json:"hooks"`
}

type statusClient struct {
	baseURL string
	http    *http.Client
}

func newStatusClient(baseURL string, timeout time.Duration) *statusClient {
	return &statusClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *statusClient) healthOK() bool {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (c *statusClient) snapshot() (Snapshot, error) {
	var snap Snapshot
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.baseURL+"/admin/api/snapshot", nil)
	if err != nil {
		return snap, fmt.Errorf("build snapshot request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return snap, fmt.Errorf("snapshot request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return snap, fmt.Errorf("decode snapshot: %w", err)
	}
	snap.PoolAlive = snap.Pool.Alive
	snap.PoolSize = snap.Pool.Size
	return snap, nil
}

func (c *statusClient) hooks() ([]HookEntry, error) { //nolint:unused // wired in by Task 12 tray UI
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, c.baseURL+"/health/hooks", nil)
	if err != nil {
		return nil, fmt.Errorf("build hooks request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hooks request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var env hooksEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode hooks: %w", err)
	}
	return env.Hooks, nil
}
