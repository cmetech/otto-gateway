package config_test

import (
	"strings"
	"testing"
	"time"

	"otto-gateway/internal/config"
)

func TestLoad_WorkerIdleRecycleDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MS", "")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KiroWorkerIdleRecycleAfter != 0 || cfg.KiroWorkerIdleRecycleMemoryMB != 500 {
		t.Fatalf("idle policy = (%v,%d), want (0,500)", cfg.KiroWorkerIdleRecycleAfter, cfg.KiroWorkerIdleRecycleMemoryMB)
	}
}

func TestLoad_WorkerIdleRecycleOverrides(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MS", "15m")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "768")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KiroWorkerIdleRecycleAfter != 15*time.Minute || cfg.KiroWorkerIdleRecycleMemoryMB != 768 {
		t.Fatalf("idle policy = (%v,%d), want (15m,768)", cfg.KiroWorkerIdleRecycleAfter, cfg.KiroWorkerIdleRecycleMemoryMB)
	}
}

func TestLoad_WorkerIdleRecycleMilliseconds(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("KIRO_WORKER_IDLE_RECYCLE_MS", "900000")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KiroWorkerIdleRecycleAfter != 15*time.Minute {
		t.Fatalf("idle duration = %v, want 15m", cfg.KiroWorkerIdleRecycleAfter)
	}
}

func TestLoad_WorkerIdleRecycleRejectsInvalidValues(t *testing.T) {
	tests := []struct{ key, value string }{
		{"KIRO_WORKER_IDLE_RECYCLE_MS", "-1"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "0"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "-1"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "1048577"},
		{"KIRO_WORKER_IDLE_RECYCLE_MEMORY_MB", "9223372036854775808"},
	}
	for _, tc := range tests {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv("HTTP_ADDR", "127.0.0.1:0")
			t.Setenv(tc.key, tc.value)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("Load error = %v, want named %s error", err, tc.key)
			}
		})
	}
}
