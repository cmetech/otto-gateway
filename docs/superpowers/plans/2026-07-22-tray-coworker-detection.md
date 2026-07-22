# Tray Co-Worker Detection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Dynamically detect the one running branded Co-Worker, enable its App and Data folder actions only while active, support immediate manual refresh, and keep the Gateway icon stable across every gateway state.

**Architecture:** Replace fixed-OTTO lookup with bounded brand.json discovery and an immutable desktop-resolution output. The existing three-second poller and a buffered manual-refresh channel feed one serialized probe; the UI loop projects each output into menu state, while actions revalidate the stored candidate. Icon selection remains separate and always returns the Gateway asset.

**Tech Stack:** Go 1.24, github.com/energye/systray, standard-library JSON/filesystem/process APIs, table-driven Go tests, Darwin build and Windows cross-build.

## Global Constraints

- Use the blue Gateway icon for every gateway state on Windows and macOS.
- A Co-Worker is active only when exactly one validated compatible branded application is running.
- Co-Worker App and Data folder items stay disabled unless that active-running condition holds; Gateway Folder remains independent.
- Discovery is descriptor-driven and shallow beneath the approved install roots. Never give LOOP24 or OTTO a fixed active-brand priority.
- brand.json never supplies an executable or directory path. Derive paths from the bounded descriptor location and validate the expected executable.
- Invalid descriptors are non-actionable. Multiple installed or running brands are ambiguous and must not select, start, stop, or open either brand.
- Timer and manual refresh use one serialized detector. Manual requests are capacity-one buffered and coalesced.
- Folder handlers revalidate the stored process before opening and clear stale state on failure.
- Honor genuine custom HERMES_HOME. Reject a populated foreign %LOCALAPPDATA%\<other-brand> default for the selected brand.
- Preserve Gateway wrapper, GW_HOME, support, metrics, installer command, runtime workarounds, and the approved favicon commit.
- Follow red-green-refactor: run each focused test in a failing state before production behavior is added.

---

## File Responsibilities

- cmd/otto-tray/brand.go: schema-v1 descriptor parsing and identity validation.
- cmd/otto-tray/desktop.go: bounded discovery, executable checks, legacy OTTO fallback, and de-duplication.
- cmd/otto-tray/desktopstate.go: immutable results, selection states, and serialized timer/manual poller.
- cmd/otto-tray/desktoprun.go plus platform files: liveness seam with distinguishable errors.
- cmd/otto-tray/desktoptray.go: production probe, menu projection, refresh, and selected Start/Stop.
- cmd/otto-tray/openfolder.go: selected App/Data path resolution and click-time revalidation.
- cmd/otto-tray/tray.go: channels, stored output, neutral startup menu, refresh item, and initial probe.
- cmd/otto-tray/gatewayicon.go: pure all-states-to-Gateway mapping.

---

### Task 1: Validated Descriptor Discovery

**Files:**
- Modify: cmd/otto-tray/brand.go
- Modify: cmd/otto-tray/brand_test.go
- Modify: cmd/otto-tray/desktop.go
- Modify: cmd/otto-tray/desktop_test.go

**Interfaces:**
- Produces: desktopCandidate, desktopDiscoveryDeps, discoverDesktopCandidates, productionDesktopDiscoveryDeps.
- Consumed by Task 2 for active candidate selection.

- [ ] **Step 1: Write failing descriptor validation tests**

Add this accepted payload and table cases for schema 2, unsafe slug, unsafe displayName, mismatched homeDir, and foreign gateway:

~~~go
func TestParseBrandDescriptor(t *testing.T) {
	valid := []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto","releasesRepo":"cmetech/loop24"}`)
	doc, ok := parseBrandDescriptor(valid)
	if !ok || doc.Slug != "loop24" || doc.DisplayName != "LOOP24" || doc.HomeDir != ".loop24" {
		t.Fatalf("valid descriptor = (%+v, %v)", doc, ok)
	}
	invalid := [][]byte{
		[]byte(`{"schemaVersion":2,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"../loop24","displayName":"LOOP24","homeDir":".../loop24","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"loop24","displayName":"bad;name","homeDir":".loop24","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".otto","gateway":"otto"}`),
		[]byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"other"}`),
	}
	for _, raw := range invalid {
		if _, ok := parseBrandDescriptor(raw); ok {
			t.Errorf("accepted invalid descriptor %s", raw)
		}
	}
}
~~~

- [ ] **Step 2: Run the descriptor test and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestParseBrandDescriptor' -count=1
~~~

Expected: compile failure because parseBrandDescriptor does not exist.

- [ ] **Step 3: Implement descriptor validation**

Add encoding/json and strings imports, then these exact types and constraints:

~~~go
type brandDescriptor struct {
	SchemaVersion int    `json:"schemaVersion"`
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	HomeDir       string `json:"homeDir"`
	Gateway       string `json:"gateway"`
	ReleasesRepo  string `json:"releasesRepo"`
}

var brandSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)

func parseBrandDescriptor(data []byte) (brandDescriptor, bool) {
	var doc brandDescriptor
	if json.Unmarshal(data, &doc) != nil ||
		doc.SchemaVersion != 1 ||
		!brandSlugRe.MatchString(doc.Slug) ||
		!validateDisplayName(doc.DisplayName) ||
		doc.HomeDir != "."+doc.Slug ||
		!strings.EqualFold(doc.Gateway, "otto") {
		return brandDescriptor{}, false
	}
	return doc, true
}
~~~

Update brandIdentity comments to say descriptors drive desktop actions only and never tray icons.

- [ ] **Step 4: Run descriptor tests and verify GREEN**

~~~bash
go test ./cmd/otto-tray -run 'Test(ParseBrandDescriptor|ValidateDisplayName|IdentityFromDisplayName)' -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Write failing discovery tests**

Use injected dependencies and filepath.Join. The primary Windows case is:

~~~go
func TestDiscoverDesktopCandidatesWindowsLoop24(t *testing.T) {
	root := filepath.Join("C:", "Users", "me", "AppData", "Local")
	appDir := filepath.Join(root, "Programs", "LOOP24")
	desc := filepath.Join(appDir, "resources", "brand.json")
	exe := filepath.Join(appDir, "LOOP24.exe")
	deps := desktopDiscoveryDeps{
		glob: func(string) ([]string, error) { return []string{desc, desc}, nil },
		readFile: func(string) ([]byte, error) {
			return []byte(`{"schemaVersion":1,"slug":"loop24","displayName":"LOOP24","homeDir":".loop24","gateway":"otto"}`), nil
		},
		exists: func(path string) bool { return path == exe },
	}
	env := func(k string) string {
		if k == "LOCALAPPDATA" { return root }
		return ""
	}
	got, err := discoverDesktopCandidates("windows", env, "", deps)
	if err != nil || len(got) != 1 {
		t.Fatalf("candidates = %+v, err=%v", got, err)
	}
	if got[0].Slug != "loop24" || got[0].Identity.WinExeName != "LOOP24.exe" || got[0].AppPath != exe {
		t.Fatalf("candidate = %+v", got[0])
	}
}
~~~

Add cases for:

- LOOP24.app/Contents/Resources/brand.json with Contents/MacOS/LOOP24;
- missing executable, invalid descriptor, and foreign gateway ignored;
- descriptor read failure returned as a discovery error;
- duplicate descriptor matches producing one candidate;
- descriptor-backed OTTO suppressing the legacy fallback duplicate;
- legacy OTTO without a descriptor producing slug otto and homeDir .otto.

- [ ] **Step 6: Run discovery tests and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestDiscoverDesktopCandidates' -count=1
~~~

Expected: compile failure for the new discovery types.

- [ ] **Step 7: Implement bounded discovery**

Use:

~~~go
type desktopCandidate struct {
	Identity       brandIdentity
	Slug           string
	HomeDir        string
	AppPath        string
	DescriptorPath string
}

type desktopDiscoveryDeps struct {
	glob     func(string) ([]string, error)
	readFile func(string) ([]byte, error)
	exists   func(string) bool
}
~~~

Implement desktopDescriptorPatterns with the exact roots from the approved spec. discoverDesktopCandidates must glob each shallow pattern, parse each descriptor, derive its owning app directory, require the matching executable, de-duplicate AppPath case-insensitively on Windows, and add the existing well-known OTTO candidate only when not represented. A glob/read error returns no actionable partial result.

When a matched descriptor is syntactically readable but fails validation or its expected executable is absent, emit a slog.Debug record with the descriptor path and rejection reason before ignoring it. Do not include descriptor contents in the log.

Production dependencies are:

~~~go
var productionDesktopDiscoveryDeps = desktopDiscoveryDeps{
	glob:     filepath.Glob,
	readFile: os.ReadFile,
	exists:   statExists,
}
~~~

Delete resolveDesktopIdentity; later code must consume a discovered candidate instead of recreating OTTO defaults.

- [ ] **Step 8: Run Task 1 verification**

~~~bash
go test ./cmd/otto-tray -run 'Test(ParseBrandDescriptor|DiscoverDesktopCandidates|DesktopAppCandidates|InstalledAppPath)' -count=1
go test ./cmd/otto-tray -count=1
~~~

Expected: PASS.

- [ ] **Step 9: Commit Task 1**

~~~bash
git add cmd/otto-tray/brand.go cmd/otto-tray/brand_test.go cmd/otto-tray/desktop.go cmd/otto-tray/desktop_test.go
git commit -m "feat(tray): discover branded Co-Worker apps"
~~~

---

### Task 2: Active Resolution and Serialized Refresh

**Files:**
- Modify: cmd/otto-tray/desktoprun.go
- Modify: cmd/otto-tray/desktoprun_test.go
- Modify: cmd/otto-tray/desktop_windows.go
- Modify: cmd/otto-tray/desktop_darwin.go
- Modify: cmd/otto-tray/desktopstate.go
- Modify: cmd/otto-tray/desktopstate_test.go

**Interfaces:**
- Consumes: Task 1 desktopCandidate.
- Produces: desktopOutput, expanded DesktopState, resolveDesktopCandidates, and a timer/manual runDesktopPoller.

- [ ] **Step 1: Write failing selection tests**

~~~go
func TestResolveDesktopCandidates(t *testing.T) {
	loop := desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24", HomeDir: ".loop24", AppPath: "LOOP24.exe"}
	otto := desktopCandidate{Identity: identityFromDisplayName("OTTO"), Slug: "otto", HomeDir: ".otto", AppPath: "OTTO.exe"}
	tests := []struct {
		name string
		candidates []desktopCandidate
		running map[string]bool
		want DesktopState
		slug string
	}{
		{"none", nil, nil, DesktopNotInstalled, ""},
		{"one stopped", []desktopCandidate{loop}, nil, DesktopStopped, "loop24"},
		{"one running", []desktopCandidate{loop}, map[string]bool{"LOOP24.exe": true}, DesktopRunning, "loop24"},
		{"two installed", []desktopCandidate{loop, otto}, nil, DesktopAmbiguous, ""},
		{"two running", []desktopCandidate{loop, otto}, map[string]bool{"LOOP24.exe": true, "OTTO.exe": true}, DesktopAmbiguous, ""},
		{"one of two running", []desktopCandidate{loop, otto}, map[string]bool{"LOOP24.exe": true}, DesktopRunning, "loop24"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveDesktopCandidates(tc.candidates, func(id brandIdentity) (bool, error) {
				return tc.running[id.WinExeName], nil
			}, false)
			gotSlug := ""
			if got.Candidate != nil { gotSlug = got.Candidate.Slug }
			if got.State != tc.want || gotSlug != tc.slug { t.Fatalf("output = %+v", got) }
		})
	}
}
~~~

Add Installing-wins and liveness-error-to-DesktopDetectionError cases.

- [ ] **Step 2: Run selection tests and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestResolveDesktopCandidates' -count=1
~~~

Expected: compile failure for new states/interfaces.

- [ ] **Step 3: Implement immutable output and error-preserving liveness**

Use:

~~~go
const (
	DesktopDetecting      DesktopState = "detecting"
	DesktopNotInstalled   DesktopState = "not-installed"
	DesktopStopped        DesktopState = "stopped"
	DesktopRunning        DesktopState = "running"
	DesktopInstalling     DesktopState = "installing"
	DesktopAmbiguous      DesktopState = "ambiguous"
	DesktopDetectionError DesktopState = "detection-error"
)

type desktopOutput struct {
	State     DesktopState
	Candidate *desktopCandidate
	Detail    string
}
~~~

Change the seam to:

~~~go
var desktopRunningFn = func(brandIdentity) (bool, error) { return false, nil }
func isDesktopRunning(id brandIdentity) (bool, error) { return desktopRunningFn(id) }
~~~

Windows returns tasklist errors. Darwin maps pgrep exit code 1 to not-running with nil error and preserves other failures. resolveDesktopCandidates implements the six approved selection rules and never returns a candidate for ambiguous/error/not-installed.

- [ ] **Step 4: Run selection/liveness tests and verify GREEN**

~~~bash
go test ./cmd/otto-tray -run 'Test(ResolveDesktopCandidates|IsDesktopRunning)' -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Write failing timer/manual poller tests**

~~~go
func TestRunDesktopPollerTimerAndRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tick := make(chan time.Time, 1)
	refresh := make(chan struct{}, 1)
	out := make(chan desktopOutput, 4)
	var calls atomic.Int32
	probe := func() desktopOutput {
		calls.Add(1)
		return desktopOutput{State: DesktopRunning, Candidate: &desktopCandidate{Slug: "loop24"}}
	}
	go runDesktopPoller(ctx, probe, tick, refresh, out)
	tick <- time.Now()
	if got := <-out; got.State != DesktopRunning { t.Fatalf("timer = %+v", got) }
	refresh <- struct{}{}
	if got := <-out; got.State != DesktopDetecting { t.Fatalf("refresh first = %+v", got) }
	if got := <-out; got.State != DesktopRunning { t.Fatalf("refresh result = %+v", got) }
	if calls.Load() != 2 { t.Fatalf("calls = %d", calls.Load()) }
}
~~~

Add requestDesktopRefresh coverage showing two non-blocking requests leave one item in a capacity-one channel.

- [ ] **Step 6: Run poller tests and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestRunDesktopPoller|TestRequestDesktopRefresh' -count=1
~~~

Expected: compile failure because the poller lacks refresh and output support.

- [ ] **Step 7: Implement serialized poller and coalescing**

Use this signature:

~~~go
func runDesktopPoller(
	ctx context.Context,
	probe func() desktopOutput,
	tick <-chan time.Time,
	refresh <-chan struct{},
	out chan<- desktopOutput,
)
~~~

Timer emits one probe result. Manual refresh emits DesktopDetecting then runs and emits the same probe. Both happen in the one poller goroutine. Use a context-aware output helper.

~~~go
func requestDesktopRefresh(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
~~~

- [ ] **Step 8: Run Task 2 verification**

~~~bash
go test ./cmd/otto-tray -run 'Test(ResolveDesktopCandidates|RunDesktopPoller|RequestDesktopRefresh|IsDesktopRunning)' -count=1
go test ./cmd/otto-tray -count=1
~~~

Expected: PASS.

- [ ] **Step 9: Commit Task 2**

~~~bash
git add cmd/otto-tray/desktoprun.go cmd/otto-tray/desktoprun_test.go cmd/otto-tray/desktop_windows.go cmd/otto-tray/desktop_darwin.go cmd/otto-tray/desktopstate.go cmd/otto-tray/desktopstate_test.go
git commit -m "feat(tray): resolve the active Co-Worker"
~~~

---

### Task 3: Dynamic Menu and Safe Folder Actions

**Files:**
- Modify: cmd/otto-tray/tray.go
- Modify: cmd/otto-tray/desktoptray.go
- Modify: cmd/otto-tray/desktoptray_test.go
- Modify: cmd/otto-tray/openfolder.go
- Modify: cmd/otto-tray/openfolder_test.go
- Modify: cmd/otto-tray/desktopcmd_test.go if required by the liveness seam

**Interfaces:**
- Consumes: Tasks 1-2 discovery and desktopOutput.
- Produces: neutral startup UI, Refresh Co-Worker Detection, stored output, menu projection, safe Start/Stop, and live-only folder actions.

- [ ] **Step 1: Write failing menu projection tests**

Define a pure desktopMenuModel and test every state. Required assertions:

~~~go
func TestDesktopMenuForOutput(t *testing.T) {
	loop := &desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24", HomeDir: ".loop24"}
	tests := []struct {
		out desktopOutput
		header string
		enabled bool
		appTitle string
		dataTitle string
		start bool
		stop bool
	}{
		{desktopOutput{State: DesktopDetecting}, "Co-Worker · detecting…", false, "Open Co-Worker App Folder", "Open Co-Worker Data Folder", false, false},
		{desktopOutput{State: DesktopNotInstalled}, "Co-Worker · not installed", false, "Open Co-Worker App Folder", "Open Co-Worker Data Folder", false, false},
		{desktopOutput{State: DesktopStopped, Candidate: loop}, "Co-Worker · LOOP24 not running", false, "Open Co-Worker App Folder", "Open Co-Worker Data Folder", true, false},
		{desktopOutput{State: DesktopRunning, Candidate: loop}, "Co-Worker · LOOP24 running", true, "Open LOOP24 App Folder", "Open LOOP24 Data Folder", false, true},
		{desktopOutput{State: DesktopAmbiguous}, "Co-Worker · multiple apps detected", false, "Open Co-Worker App Folder", "Open Co-Worker Data Folder", false, false},
		{desktopOutput{State: DesktopDetectionError}, "Co-Worker · detection error", false, "Open Co-Worker App Folder", "Open Co-Worker Data Folder", false, false},
	}
	for _, tc := range tests {
		got := desktopMenuForOutput(tc.out)
		if got.Header != tc.header || got.FoldersEnabled != tc.enabled ||
			got.AppFolderTitle != tc.appTitle || got.DataFolderTitle != tc.dataTitle ||
			got.StartVisible != tc.start || got.StopVisible != tc.stop {
			t.Errorf("%s => %+v", tc.out.State, got)
		}
	}
}
~~~

Also assert Install visibility/enabled: visible for NotInstalled, visible but disabled for Installing, hidden otherwise.

- [ ] **Step 2: Run menu tests and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestDesktopMenuForOutput' -count=1
~~~

Expected: compile failure because the projection does not exist.

- [ ] **Step 3: Implement projection and production probe**

Production probe:

~~~go
func (s *trayState) makeDesktopProbe() func() desktopOutput {
	return func() desktopOutput {
		candidates, err := discoverDesktopCandidates(runtime.GOOS, os.Getenv, homeDir(), productionDesktopDiscoveryDeps)
		if err != nil {
			return desktopOutput{State: DesktopDetectionError, Detail: err.Error()}
		}
		return resolveDesktopCandidates(candidates, isDesktopRunning, s.desktopInstalling.Load())
	}
}
~~~

desktopMenuModel contains Header, AppFolderTitle, DataFolderTitle, FoldersEnabled and Install/Start/Stop visible/enabled flags. applyDesktopOutput stores an immutable copy in atomic.Pointer[desktopOutput], applies the projection, and is the only desktop UI mutation path.

The projection deliberately contains no Gateway-folder flag: applyDesktopOutput must never enable, disable, retitle, or otherwise mutate miOpenGatewayFolder. Add a regression assertion over the Task 3 diff before commit that handleOpenGatewayFolder and its menu callback remain unchanged.

- [ ] **Step 4: Write failing HERMES_HOME brand-safety tests**

Primary foreign-default case:

~~~go
func TestResolveHermesHomeRejectsForeignPopulatedDefault(t *testing.T) {
	local := filepath.Join("C:", "Users", "me", "AppData", "Local")
	foreign := filepath.Join(local, "otto")
	env := func(k string) string {
		if k == "LOCALAPPDATA" { return local }
		if k == "HERMES_HOME" { return foreign }
		return ""
	}
	exists := func(path string) bool { return path == filepath.Join(foreign, "hermes-agent") }
	got := resolveHermesHome("windows", env, "", "loop24", ".loop24", func(string) string { return "" }, exists)
	if got != filepath.Join(local, "loop24") { t.Fatalf("got %q", got) }
}
~~~

Also test selected %LOCALAPPDATA%\\loop24, external custom env/registry homes, unpopulated foreign paths, and macOS home/.loop24.

- [ ] **Step 5: Run home tests and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestResolveHermesHome' -count=1
~~~

Expected: signature compile failure or foreign-default assertion failure.

- [ ] **Step 6: Implement candidate-aware home resolution**

Use:

~~~go
func resolveHermesHome(
	goos string,
	env func(string) string,
	home, slug, brandHomeDir string,
	winReg func(string) string,
	exists func(string) bool,
) string
~~~

For Windows, examine env then registry. Ignore only an immediate foreign child of normalized LOCALAPPDATA whose base differs from slug and whose hermes-agent child exists. Then use LOCALAPPDATA/slug. For macOS default to home/brandHomeDir.

Add:

~~~go
func runningDesktopCandidate(out *desktopOutput, running func(brandIdentity) (bool, error)) (*desktopCandidate, error)
~~~

It succeeds only for DesktopRunning, a non-nil candidate, and a fresh exact-process running result.

Define one injected action helper so tests never launch Explorer or Finder:

~~~go
type desktopFolderKind int

const (
	desktopAppFolder desktopFolderKind = iota
	desktopDataFolder
)

func runOpenDesktopFolder(
	kind desktopFolderKind,
	out *desktopOutput,
	goos string,
	env func(string) string,
	home string,
	winReg func(string) string,
	exists func(string) bool,
	running func(brandIdentity) (bool, error),
	open func(string, bool) error,
) error
~~~

The helper first calls runningDesktopCandidate. App kind opens appFolderTarget(candidate.AppPath). Data kind resolves HERMES_HOME from candidate.Slug and candidate.HomeDir, requires that directory to exist, then opens it without reveal.

- [ ] **Step 7: Write failing action validation tests**

~~~go
func TestRunningDesktopCandidateRejectsStaleSnapshot(t *testing.T) {
	c := &desktopCandidate{Identity: identityFromDisplayName("LOOP24"), Slug: "loop24"}
	if _, err := runningDesktopCandidate(&desktopOutput{State: DesktopStopped, Candidate: c}, func(brandIdentity) (bool, error) { return false, nil }); err == nil {
		t.Fatal("stopped candidate was actionable")
	}
	if _, err := runningDesktopCandidate(&desktopOutput{State: DesktopRunning, Candidate: c}, func(brandIdentity) (bool, error) { return false, nil }); err == nil {
		t.Fatal("stale snapshot was actionable")
	}
}
~~~

Add an injected runOpenDesktopFolder test proving the opener is not called after failed revalidation and exact LOOP24 AppPath/DataPath reach it on success.

- [ ] **Step 8: Run action tests and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'Test(RunningDesktopCandidate|RunOpenDesktopFolder)' -count=1
~~~

Expected: compile failure for missing validator/action helper.

- [ ] **Step 9: Wire tray channels and actions**

Replace desktopAppPath with:

~~~go
desktopCh        chan desktopOutput
desktopRefreshCh chan struct{}
desktopCurrent   atomic.Pointer[desktopOutput]
miDesktopRefresh *systray.MenuItem
~~~

Initialize refresh with capacity one. onReady must remove fixed identity resolution, create neutral App/Data items and immediately disable them, add Refresh Co-Worker Detection, pass timer and refresh channels to runDesktopPoller, and request an initial refresh after the UI loop starts.

Refresh callback calls requestDesktopRefresh. Start acts only on one DesktopStopped candidate. Stop acts only on one DesktopRunning candidate. App/Data handlers use runningDesktopCandidate; stale state publishes a non-running/error output, requests refresh, leaves Explorer closed, and notifies. Gateway Folder is unchanged.

- [ ] **Step 10: Run Task 3 verification**

~~~bash
go test ./cmd/otto-tray -run 'Test(DesktopMenuForOutput|ResolveHermesHome|RunningDesktopCandidate|RunOpenDesktopFolder|RunDesktopPoller|ResolveDesktopCandidates)' -count=1
go test ./cmd/otto-tray -count=1
~~~

Expected: PASS.

- [ ] **Step 11: Commit Task 3**

~~~bash
git add cmd/otto-tray/tray.go cmd/otto-tray/desktoptray.go cmd/otto-tray/desktoptray_test.go cmd/otto-tray/openfolder.go cmd/otto-tray/openfolder_test.go cmd/otto-tray/desktopcmd_test.go
git commit -m "feat(tray): enable folders for the running Co-Worker"
~~~

---

### Task 4: Stable Gateway Icon and Full Verification

**Files:**
- Create: cmd/otto-tray/gatewayicon.go
- Create: cmd/otto-tray/gatewayicon_test.go
- Modify: cmd/otto-tray/uihelpers_windows.go
- Modify: cmd/otto-tray/uihelpers_darwin.go

**Interfaces:**
- Produces: gatewayIconForState(State) []byte, used by both platform helpers.

- [ ] **Step 1: Write failing all-states icon test**

~~~go
func TestGatewayIconForEveryState(t *testing.T) {
	states := []State{StateUnknown, StateStopped, StateStarting, StateRunning, StateDegraded, StateError}
	for _, state := range states {
		if got := gatewayIconForState(state); !bytes.Equal(got, icon.Gateway) {
			t.Errorf("state %s did not use Gateway icon", state)
		}
	}
}
~~~

- [ ] **Step 2: Run icon test and verify RED**

~~~bash
go test ./cmd/otto-tray -run 'TestGatewayIconForEveryState' -count=1
~~~

Expected: compile failure because gatewayIconForState does not exist.

- [ ] **Step 3: Implement one icon mapping**

Create:

~~~go
//go:build darwin || windows

package main

import "otto-gateway/cmd/otto-tray/icon"

func gatewayIconForState(State) []byte { return icon.Gateway }
~~~

Windows setIconForState calls systray.SetIcon(gatewayIconForState(state)) with no switch. Darwin calls systray.SetTemplateIcon(gatewayIconForState(state), gatewayIconForState(state)) with no switch. Comments state status is conveyed by tooltip/header.

- [ ] **Step 4: Run focused and full verification**

~~~bash
go test ./cmd/otto-tray -run 'TestGatewayIconForEveryState' -count=1
gofumpt -w cmd/otto-tray/*.go
git diff --check
go test ./... -count=1
go vet ./...
go build ./cmd/otto-gateway
go build ./cmd/otto-tray
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags='-H windowsgui' -o /tmp/gateway-tray-windows-amd64.exe ./cmd/otto-tray
golangci-lint run ./...
~~~

Expected: every command succeeds.

- [ ] **Step 5: Run source regression assertions**

~~~bash
if rg -n 'resolveDesktopIdentity|desktopAppPath' cmd/otto-tray; then exit 1; fi
rg -n 'setIconForState' cmd/otto-tray/uihelpers_windows.go cmd/otto-tray/uihelpers_darwin.go
git status --short
~~~

Expected: fixed resolver/cache names absent, both platform helpers present, only intended files modified.

- [ ] **Step 6: Commit Task 4**

~~~bash
git add cmd/otto-tray/gatewayicon.go cmd/otto-tray/gatewayicon_test.go cmd/otto-tray/uihelpers_windows.go cmd/otto-tray/uihelpers_darwin.go
git commit -m "fix(tray): keep the Gateway icon across states"
~~~

- [ ] **Step 7: Record Windows runtime acceptance without overstating unavailable evidence**

~~~text
[ ] Tray before Co-Worker: App/Data neutral and disabled; Gateway Folder available.
[ ] Launch LOOP24: within 3 seconds LOOP24 header and correct folders enable.
[ ] App Folder opens the directory containing LOOP24.exe.
[ ] Data Folder opens %LOCALAPPDATA%\\loop24 or genuine custom HERMES_HOME.
[ ] Stop LOOP24: within 3 seconds folder items become neutral and disabled.
[ ] Manual Refresh updates immediately without duplicate probes.
[ ] Run OTTO instead: exact OTTO process and folders are selected.
[ ] Run OTTO and LOOP24: ambiguity shown and brand-dependent actions disabled.
[ ] Gateway state transitions retain the blue Gateway glyph.
~~~

---

## Final Review and Integration

1. Generate a whole-branch review package from the branch merge base through HEAD.
2. Dispatch a broad final reviewer against this plan and the approved design.
3. Resolve every Critical or Important finding in one fix wave; rerun covering tests and re-review.
4. Repeat complete verification from a clean worktree.
5. Use superpowers:finishing-a-development-branch to integrate the reviewed favicon and tray commits.
6. Merge to main, push, build the release, and confirm the published Windows archive contains the new gateway-tray.exe before claiming the upgrade is ready.
