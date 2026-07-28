//go:build darwin || windows

package main

import "testing"

func TestShouldNotifyStartFailure(t *testing.T) {
	tests := []struct {
		name           string
		result         runResult
		healthy        bool
		wantNotify     bool
		wantProbeCalls int
	}{
		{
			name:           "successful wrapper does not probe or notify",
			result:         runResult{ExitCode: 0},
			healthy:        false,
			wantNotify:     false,
			wantProbeCalls: 0,
		},
		{
			name:           "wrapper failure probes once and is suppressed when gateway is healthy",
			result:         runResult{ExitCode: 1, Stderr: fixtureOverridesLine},
			healthy:        true,
			wantNotify:     false,
			wantProbeCalls: 1,
		},
		{
			name:           "wrapper failure probes once and notifies when gateway is unhealthy",
			result:         runResult{ExitCode: 1, Stderr: "bind: address already in use"},
			healthy:        false,
			wantNotify:     true,
			wantProbeCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probeCalls := 0
			probe := func() bool {
				probeCalls++
				return tt.healthy
			}
			if got := shouldNotifyStartFailure(tt.result, probe); got != tt.wantNotify {
				t.Fatalf("shouldNotifyStartFailure(%+v) = %t, want %t", tt.result, got, tt.wantNotify)
			}
			if probeCalls != tt.wantProbeCalls {
				t.Fatalf("probe called %d time(s), want %d", probeCalls, tt.wantProbeCalls)
			}
		})
	}
}
