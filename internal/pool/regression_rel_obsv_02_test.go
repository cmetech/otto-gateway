// Package pool_test — regression for REL-OBSV-02 (D-18-05).
//
// Pre-fix: when a dead slot is lazily respawned by Pool.NewSession (the
// recovery path that closes the asymmetry with the "pool: slot died" log
// at exit_watcher.go:42), no structured log was emitted. Operators saw
// the death but had no positive confirmation of recovery.
//
// Post-fix: respawnSlot emits exactly one
//
//	slog.Info("pool: slot recovered",
//	          "label", slot.Label,
//	          "worker_pid", <NEW pid>,
//	          "previous_pid", <OLD pid>,
//	          "reason", "lazy-respawn-success")
//
// on the success path AFTER slot.Client swap + slot.dead reset. The
// previous_pid is captured from the OLD client BEFORE close.
//
// Phase 18 Plan 02 — Task 2 Part A.
package pool_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/pool"
)

// TestRegression_REL_OBSV_02 covers A1: warmup → fireDone → respawn flow
// with distinct fakeClient pids (1001 OLD, 1002 NEW). Assert exactly one
// "pool: slot recovered" Info record with the four required fields and
// byte-exact reason "lazy-respawn-success".
func TestRegression_REL_OBSV_02(t *testing.T) {
	// Inject a JSON-handler-backed logger so we can decode records.
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	fc0 := &fakeClient{pid: 1001} // warmup → will be killed
	fc1 := &fakeClient{pid: 1002} // recovery client

	cf := &fakeClientFactory{
		clients: []pool.PoolClient{fc0, fc1},
	}

	p := pool.New(pool.Config{
		Logger:  logger,
		Size:    1,
		Factory: cf,
	})

	warmCtx, warmCancel := context.WithTimeout(context.Background(), time.Second)
	defer warmCancel()
	if err := p.Warmup(warmCtx); err != nil {
		t.Fatalf("Warmup: %v", err)
	}
	defer func() { _ = p.Close() }()

	// Kill the warmup client. Exit watcher will mark slot dead.
	fc0.fireDone()

	// Wait for slot.dead = true (exit-watcher flips it on Done()).
	deadline := time.Now().Add(time.Second)
	for p.Stats().Alive == 1 {
		if time.Now().After(deadline) {
			t.Fatal("slot did not flip to dead within 1s of fireDone")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// NewSession enters the dead-slot branch and calls respawnSlot which
	// (post-fix) emits the recovery Info log on success.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sid, err := p.NewSession(ctx, "")
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer p.Cancel(sid)

	// Decode + filter records.
	var recovered map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode slog record %q: %v", line, err)
		}
		if msg, _ := rec["msg"].(string); msg == "pool: slot recovered" {
			if recovered != nil {
				t.Fatalf("duplicate 'pool: slot recovered' records; first=%+v second=%+v", recovered, rec)
			}
			recovered = rec
		}
	}

	if recovered == nil {
		t.Fatalf("no 'pool: slot recovered' record found; buf=%s", buf.String())
	}
	if lvl, _ := recovered["level"].(string); lvl != "INFO" {
		t.Errorf("level = %q, want INFO", lvl)
	}
	if lbl, _ := recovered["label"].(string); lbl == "" {
		t.Errorf("label field missing or empty; record=%+v", recovered)
	}
	if got, want := recovered["worker_pid"], float64(1002); got != want {
		t.Errorf("worker_pid = %v, want %v (NEW client pid)", got, want)
	}
	if got, want := recovered["previous_pid"], float64(1001); got != want {
		t.Errorf("previous_pid = %v, want %v (OLD client pid)", got, want)
	}
	if got, want := recovered["reason"], "lazy-respawn-success"; got != want {
		t.Errorf("reason = %q, want %q (byte-exact per CONTEXT.md §D-18-05)", got, want)
	}
}
