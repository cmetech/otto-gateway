# Task 3 Report: Dynamic Menu and Safe Folder Actions

## Summary

- Replaced the legacy `DesktopState` tray channel with typed `desktopOutput` polling, a capacity-one manual-refresh channel, and an immutable `atomic.Pointer[desktopOutput]` snapshot.
- Added the pure desktop menu projection for detecting, not-installed, installing, stopped, running, ambiguous, and detection-error states.
- Added neutral disabled Co-Worker App/Data items, dynamic running-brand titles, and `Refresh Co-Worker Detection`.
- Changed Start and Stop to consume only the selected candidate from the current typed snapshot. Stop liveness errors are handled, and the temporary unconditional post-stop probe is gone.
- Added candidate-aware `HERMES_HOME` resolution that rejects populated foreign-brand defaults while preserving selected-brand and genuine custom locations.
- Added click-time exact-process revalidation and injected App/Data folder actions. Stale snapshots close no folder, clear/publish unsafe state, request refresh, and notify.
- Removed the fixed-identity and legacy sequencing shims and their obsolete tests.
- Left the Gateway-folder handler and callback unchanged. Task 4 icon behavior was not modified.

## TDD Evidence

### Menu projection

RED:

```text
$ go test ./cmd/otto-tray -run 'TestDesktopMenuForOutput' -count=1
cmd/otto-tray/desktoptray_test.go:44:11: undefined: desktopMenuForOutput
FAIL otto-gateway/cmd/otto-tray [build failed]
```

GREEN:

```text
$ go test ./cmd/otto-tray -run 'TestDesktopMenuForOutput' -count=1
ok  otto-gateway/cmd/otto-tray  0.328s
```

### Candidate-aware HERMES_HOME

RED:

```text
$ go test ./cmd/otto-tray -run 'TestResolveHermesHome' -count=1
openfolder_test.go: multiple calls have too many arguments for the old resolveHermesHome signature
FAIL otto-gateway/cmd/otto-tray [build failed]
```

GREEN:

```text
$ go test ./cmd/otto-tray -run 'TestResolveHermesHome' -count=1
ok  otto-gateway/cmd/otto-tray  0.339s
```

### Folder validation and injected actions

RED:

```text
$ go test ./cmd/otto-tray -run 'Test(RunningDesktopCandidate|RunOpenDesktopFolder)' -count=1
undefined: runningDesktopCandidate
undefined: runOpenDesktopFolder
undefined: desktopAppFolder
undefined: desktopDataFolder
FAIL otto-gateway/cmd/otto-tray [build failed]
```

GREEN:

```text
$ go test ./cmd/otto-tray -run 'Test(RunningDesktopCandidate|RunOpenDesktopFolder)' -count=1
ok  otto-gateway/cmd/otto-tray  0.295s
```

### Typed production probe

RED:

```text
$ go test ./cmd/otto-tray -run 'TestMakeDesktopProbe' -count=1
desktoptray_test.go: got.State/Candidate/Detail undefined on legacy desktopInput
FAIL otto-gateway/cmd/otto-tray [build failed]
```

GREEN:

```text
$ go test ./cmd/otto-tray -run 'TestMakeDesktopProbe' -count=1
ok  otto-gateway/cmd/otto-tray  0.337s
```

## Verification Results

The following checks passed during implementation; the required focused and package commands were rerun fresh immediately before commit.

```text
go test ./cmd/otto-tray -run 'Test(DesktopMenuForOutput|ResolveHermesHome|RunningDesktopCandidate|RunOpenDesktopFolder|RunDesktopPoller|ResolveDesktopCandidates)' -count=1
ok  otto-gateway/cmd/otto-tray  0.288s (fresh required pre-commit run)

go test ./cmd/otto-tray -run 'Test(DesktopMenuForOutput|ResolveHermesHome|RunningDesktopCandidate|RunOpenDesktopFolder|RunDesktopPoller|ResolveDesktopCandidates|MakeDesktopProbe)' -count=1
ok  otto-gateway/cmd/otto-tray  0.224s

go test ./cmd/otto-tray -count=1
ok  otto-gateway/cmd/otto-tray  0.239s (fresh pre-commit run)

go test ./... -count=1
PASS (all repository packages; slowest package internal/admin at 20.945s)

go vet ./...
PASS (exit 0, no diagnostics)

go test -race ./cmd/otto-tray -count=1
ok  otto-gateway/cmd/otto-tray  1.413s

task3_build_dir=$(mktemp -d) && CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o "$task3_build_dir/gateway-tray" ./cmd/otto-tray && test -s "$task3_build_dir/gateway-tray"
PASS (exit 0)

task3_build_dir=$(mktemp -d) && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o "$task3_build_dir/gateway-tray.exe" ./cmd/otto-tray && test -s "$task3_build_dir/gateway-tray.exe"
PASS (exit 0)
```

Additional assertions:

- `git diff --check`: clean.
- Zero `resolveDesktopIdentity`, `desktopAppPath`, `runLegacyDesktopPoller`, `desktopInput`, or `computeDesktopState` references remain in `cmd/otto-tray`.
- Every remaining `isDesktopRunning` consumer propagates or explicitly handles its error.
- Exact extracted-function/callback comparison against `HEAD` passed: `handleOpenGatewayFolder` and `s.miOpenGatewayFolder.Click(func() { go s.handleOpenGatewayFolder() })` are unchanged.

## Files Changed

- `cmd/otto-tray/tray.go`
- `cmd/otto-tray/desktoptray.go`
- `cmd/otto-tray/desktoptray_test.go`
- `cmd/otto-tray/openfolder.go`
- `cmd/otto-tray/openfolder_test.go`
- `cmd/otto-tray/desktopstate.go`
- `cmd/otto-tray/desktopstate_test.go`
- `cmd/otto-tray/desktop.go`
- `cmd/otto-tray/desktop_test.go`
- `.superpowers/sdd/task-3-report.md`

## Self-Review

- The UI projection has no Gateway-folder property and `applyDesktopOutput` never touches `miOpenGatewayFolder`.
- `applyDesktopOutput` deep-copies the candidate before atomically publishing the output, so discovery-owned values are not shared mutably.
- Timer and manual refresh both run through the same serialized poller; capacity-one refresh requests coalesce.
- Detecting/error/ambiguous/non-running states restore neutral folder titles and disable both folder actions.
- Start uses only a stopped snapshot candidate and rechecks its discovered AppPath. Stop uses only a running snapshot candidate and targets its exact identity.
- Folder actions revalidate liveness before resolving or opening any path. Data actions additionally require the resolved directory to exist.
- Windows foreign-default detection is limited to an immediate child of normalized `LOCALAPPDATA`, a differing slug, and an existing `hermes-agent` child; other custom paths remain authoritative.
- The forced-stop follow-up liveness call occurs only when the stop command reports an error; no unconditional second post-stop probe remains.

## Concerns / Follow-up

- No code concern remains for Task 3.
- Interactive Windows tray acceptance (launch/exit multiple branded apps and click the real Explorer actions) was not possible in this environment; pure seam tests and the Windows cross-build cover the code paths here.
- Gateway icon unification remains intentionally deferred to Task 4.
