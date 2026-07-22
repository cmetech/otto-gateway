//go:build darwin || windows

package main

import (
	"errors"
	"testing"
)

func TestIsDesktopRunning_UsesSeam(t *testing.T) {
	old := desktopRunningFn
	defer func() { desktopRunningFn = old }()

	candidate := desktopCandidate{Identity: defaultBrandIdentity(), ExecutablePath: `C:\Programs\OTTO\OTTO.exe`}
	desktopRunningFn = func(got desktopCandidate) (bool, error) {
		if got.ExecutablePath != candidate.ExecutablePath {
			t.Fatalf("candidate path = %q, want %q", got.ExecutablePath, candidate.ExecutablePath)
		}
		return true, nil
	}
	running, err := isDesktopRunning(candidate)
	if err != nil || !running {
		t.Fatalf("isDesktopRunning() = (%v, %v), want (true, nil)", running, err)
	}
	wantErr := errors.New("process enumeration failed")
	desktopRunningFn = func(desktopCandidate) (bool, error) { return false, wantErr }
	running, err = isDesktopRunning(candidate)
	if running || !errors.Is(err, wantErr) {
		t.Fatalf("isDesktopRunning() = (%v, %v), want (false, %v)", running, err, wantErr)
	}
}

func TestResolveDesktopCandidates(t *testing.T) {
	loop := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24", HomeDir: ".loop24", AppPath: "LOOP24.exe", ExecutablePath: `C:\Apps\LOOP24.exe`}
	otto := desktopCandidate{Identity: identityFromDisplayName("OTTO"), Slug: "otto", HomeDir: ".otto", AppPath: "OTTO.exe", ExecutablePath: `C:\Apps\OTTO.exe`}
	tests := []struct {
		name       string
		candidates []desktopCandidate
		running    map[string]bool
		installing bool
		want       DesktopState
		slug       string
	}{
		{name: "none", want: DesktopNotInstalled},
		{name: "one stopped", candidates: []desktopCandidate{loop}, want: DesktopStopped, slug: "loop24"},
		{name: "one running", candidates: []desktopCandidate{loop}, running: map[string]bool{loop.ExecutablePath: true}, want: DesktopRunning, slug: "loop24"},
		{name: "two installed", candidates: []desktopCandidate{loop, otto}, want: DesktopAmbiguous},
		{name: "two running", candidates: []desktopCandidate{loop, otto}, running: map[string]bool{loop.ExecutablePath: true, otto.ExecutablePath: true}, want: DesktopAmbiguous},
		{name: "one of two running", candidates: []desktopCandidate{loop, otto}, running: map[string]bool{loop.ExecutablePath: true}, want: DesktopRunning, slug: "loop24"},
		{name: "installing wins", candidates: []desktopCandidate{loop}, running: map[string]bool{loop.ExecutablePath: true}, installing: true, want: DesktopInstalling},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDesktopCandidates(tc.candidates, func(candidate desktopCandidate) (bool, error) {
				return tc.running[candidate.ExecutablePath], nil
			}, tc.installing)
			gotSlug := ""
			if got.Candidate != nil {
				gotSlug = got.Candidate.Slug
			}
			if got.State != tc.want || gotSlug != tc.slug {
				t.Fatalf("output = %+v, candidate slug = %q; want state %q, slug %q", got, gotSlug, tc.want, tc.slug)
			}
		})
	}
}

func TestResolveDesktopCandidates_LivenessError(t *testing.T) {
	candidate := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24"}
	wantErr := errors.New("process enumeration failed")

	got := resolveDesktopCandidates([]desktopCandidate{candidate}, func(desktopCandidate) (bool, error) {
		return false, wantErr
	}, false)

	if got.State != DesktopDetectionError || got.Candidate != nil {
		t.Fatalf("output = %+v, want detection error without candidate", got)
	}
	if got.Detail == "" {
		t.Fatal("expected detection error detail")
	}
}
