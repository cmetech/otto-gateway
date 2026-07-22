//go:build darwin || windows

package main

// desktopRunningFn is the process-liveness probe for the desktop app. It is a
// package var so tests can substitute a stub and so each platform file can
// assign its own implementation in init(). isDesktopRunning is the caller-facing
// entry.
var desktopRunningFn = func(id brandIdentity) (bool, error) { return false, nil }

func isDesktopRunning(id brandIdentity) (bool, error) { return desktopRunningFn(id) }

// resolveDesktopCandidates selects a candidate only when the installed and
// running evidence identifies one unambiguously. Liveness failures invalidate
// the whole result because a partial process snapshot is not trustworthy.
func resolveDesktopCandidates(
	candidates []desktopCandidate,
	isRunning func(brandIdentity) (bool, error),
	installing bool,
) desktopOutput {
	if installing {
		return desktopOutput{State: DesktopInstalling}
	}
	if len(candidates) == 0 {
		return desktopOutput{State: DesktopNotInstalled}
	}

	running := make([]desktopCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		alive, err := isRunning(candidate.Identity)
		if err != nil {
			return desktopOutput{State: DesktopDetectionError, Detail: err.Error()}
		}
		if alive {
			running = append(running, candidate)
		}
	}

	if len(running) == 1 {
		candidate := running[0]
		return desktopOutput{State: DesktopRunning, Candidate: &candidate}
	}
	if len(running) > 1 || len(candidates) > 1 {
		return desktopOutput{State: DesktopAmbiguous}
	}

	candidate := candidates[0]
	return desktopOutput{State: DesktopStopped, Candidate: &candidate}
}
