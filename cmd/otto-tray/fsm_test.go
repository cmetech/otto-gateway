//go:build darwin || windows

package main

import "testing"

func TestComputeState_StoppedWhenNoPIDAndNoHealth(t *testing.T) {
	got := computeState(stateInput{PIDAlive: false, HealthOK: false})
	if got.State != StateStopped {
		t.Fatalf("no pid, no health → want %s, got %s", StateStopped, got.State)
	}
}

func TestComputeState_RunningWhenPIDAndHealth(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive: true,
		HealthOK: true,
		Snapshot: Snapshot{PoolAlive: 4, PoolSize: 4},
	})
	if got.State != StateRunning {
		t.Fatalf("pid + health → want %s, got %s", StateRunning, got.State)
	}
}

func TestComputeState_DegradedWhenPoolEmpty(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive: true,
		HealthOK: true,
		Snapshot: Snapshot{PoolAlive: 0, PoolSize: 4},
	})
	if got.State != StateDegraded {
		t.Fatalf("pool empty → want %s, got %s", StateDegraded, got.State)
	}
}

func TestComputeState_RunningWhenPoolSizeZero(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive: true,
		HealthOK: true,
		Snapshot: Snapshot{PoolAlive: 0, PoolSize: 0},
	})
	if got.State != StateRunning {
		t.Fatalf("zero pool → want %s, got %s", StateRunning, got.State)
	}
}

func TestComputeState_StartingWhenPIDButNoHealthInsideBudget(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive:       true,
		HealthOK:       false,
		HealthFailures: 1,
		StartingBudget: true,
	})
	if got.State != StateStarting {
		t.Fatalf("pid alive within budget → want %s, got %s", StateStarting, got.State)
	}
}

func TestComputeState_ErrorAfterThreeHealthFailuresOutsideBudget(t *testing.T) {
	got := computeState(stateInput{
		PIDAlive:       true,
		HealthOK:       false,
		HealthFailures: 3,
		StartingBudget: false,
	})
	if got.State != StateError {
		t.Fatalf("pid alive, 3 failures, budget expired → want %s, got %s", StateError, got.State)
	}
}
