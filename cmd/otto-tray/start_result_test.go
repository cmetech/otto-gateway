//go:build darwin || windows

package main

import "testing"

func TestShouldNotifyStartFailure(t *testing.T) {
	tests := []struct {
		name    string
		result  runResult
		healthy bool
		want    bool
	}{
		{
			name:    "successful wrapper does not notify",
			result:  runResult{ExitCode: 0},
			healthy: false,
			want:    false,
		},
		{
			name:    "wrapper failure is suppressed when gateway is healthy",
			result:  runResult{ExitCode: 1, Stderr: fixtureOverridesLine},
			healthy: true,
			want:    false,
		},
		{
			name:    "wrapper failure notifies when gateway is unhealthy",
			result:  runResult{ExitCode: 1, Stderr: "bind: address already in use"},
			healthy: false,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldNotifyStartFailure(tt.result, tt.healthy); got != tt.want {
				t.Fatalf("shouldNotifyStartFailure(%+v, healthy=%t) = %t, want %t", tt.result, tt.healthy, got, tt.want)
			}
		})
	}
}
