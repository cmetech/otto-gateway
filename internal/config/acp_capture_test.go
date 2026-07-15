package config_test

import (
	"strings"
	"testing"

	"otto-gateway/internal/config"
)

func TestLoad_AcpCaptureDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AcpCapture {
		t.Error("AcpCapture default = true, want false")
	}
	if cfg.AcpCaptureSize != 512 {
		t.Errorf("AcpCaptureSize default = %d, want 512", cfg.AcpCaptureSize)
	}
}

func TestLoad_AcpCaptureEnabled(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ACP_CAPTURE", "true")
	t.Setenv("ACP_CAPTURE_SIZE", "128")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.AcpCapture {
		t.Error("AcpCapture = false, want true")
	}
	if cfg.AcpCaptureSize != 128 {
		t.Errorf("AcpCaptureSize = %d, want 128", cfg.AcpCaptureSize)
	}
}

func TestLoad_AcpCaptureSizeRejectsNonPositive(t *testing.T) {
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("ACP_CAPTURE_SIZE", "0")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "ACP_CAPTURE_SIZE") {
		t.Fatalf("ACP_CAPTURE_SIZE=0 must be rejected, got %v", err)
	}
}
