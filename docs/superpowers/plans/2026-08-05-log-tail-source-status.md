# Log Tail Source Status and Kiro Logging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Kiro native logging default to `INFO` and make every dashboard log source report accurate file health, recent bounded history, live records, and filter-empty state.

**Architecture:** Configuration resolves and validates one effective Kiro level before any child launches. Each allowlisted admin source maps to a shared tailer that atomically attaches subscribers, maintains a bounded ring plus browser-safe `TailStatus`, reads recent records only on its first successful open, and emits `status`, `log`, and `ping` SSE events. The browser renders transport health separately from file health and row-filter state.

**Tech Stack:** Go 1.24 standard library (`os`, `bufio`, `encoding/json`, `log/slog`, `net/http`), chi admin routes, Server-Sent Events, vanilla JavaScript, Node's built-in test runner, Go's built-in test framework, `go.uber.org/goleak`.

## Global Constraints

- `KIRO_LOG_LEVEL` unset or empty resolves to uppercase `INFO`; there is no `OFF` state.
- Explicit Kiro levels are case-insensitive `ERROR`, `WARN`, `INFO`, `DEBUG`, or `TRACE`; all other non-empty values fail startup.
- Every pooled and dedicated Kiro child receives both `KIRO_CHAT_LOG_FILE` and the resolved `KIRO_LOG_LEVEL`.
- Initial history is read-only, limited to the latest 500 complete records and an 8 MiB input window, and still enforces the existing 1 MiB per-record cap.
- Rotation and truncation reopen at EOF and never replay replacement-file history.
- Browser status payloads never contain filesystem paths or raw operating-system errors.
- Kiro remains a dashboard source only for strict-loopback listeners.
- Missing and unreadable sources remain recoverable while SSE stays open.
- Use red-green-refactor for every production behavior; observe each new test fail for the intended missing behavior before implementation.
- Preserve non-blocking fan-out, pause/resume, reconnect deduplication, shutdown cancellation, and the no-new-dependency posture.

---

## File Structure

- `internal/config/config.go` and `config_test.go` — Kiro level resolution and validation.
- `cmd/otto-gateway/main.go` and `main_test.go` — child environment, startup log, and admin source metadata.
- `internal/admin/admin.go` — effective level in admin dependencies and configuration reference.
- `internal/admin/tail_status.go` — new status types, coalescing, and warning state.
- `internal/admin/tail_backfill.go` — new bounded first-open history parser.
- `internal/admin/tail.go` and tailer tests — atomic attachment, lifecycle, backfill, live reads, recovery, and registry.
- `internal/admin/sse.go` and `sse_test.go` — status/log/ping protocol and coherent attachment.
- `internal/admin/static/js/admin.js`, `admin_js_test.js`, and `dashboard.html.tmpl` — precise UI states and filter-empty behavior.
- `scripts/.env.example` and `docs/operating.md` — generated and operator-facing configuration.

---

### Task 1: Resolve and propagate the Kiro native log level

**Files:**
- Modify: `internal/config/config.go:102-121,425-456,1043-1053`
- Modify: `internal/config/config_test.go:16-110`
- Modify: `cmd/otto-gateway/main.go:154-195,746-810,1085-1105,1130-1140`
- Modify: `cmd/otto-gateway/main_test.go:914-945,1112-1182`
- Modify: `internal/admin/admin.go:90-130,180-205,398-430,798-850`

**Interfaces:**
- Produces: `Config.KiroLogLevel string`, one of `ERROR|WARN|INFO|DEBUG|TRACE` after `config.Load` succeeds.
- Produces: `parseKiroLogLevel(raw string) (string, error)` inside `internal/config`.
- Produces: `kiroProcessEnv(config.Config) []string` with exact file and level entries.
- Produces: `admin.Deps.KiroLogLevel string` and `admin.Deps.LogSourceLevels map[string]string`.
- Consumes: existing pool/session `KiroEnv` propagation and source ID `kiro`.

- [ ] **Step 1: Write failing configuration tests**

Add to `internal/config/config_test.go`:

```go
func TestLoadKiroLogLevel(t *testing.T) {
	tests := []struct{ raw, want string }{
		{raw: "", want: "INFO"},
		{raw: "debug", want: "DEBUG"},
		{raw: "TrAcE", want: "TRACE"},
		{raw: "WARN", want: "WARN"},
		{raw: "error", want: "ERROR"},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("KIRO_LOG_LEVEL", tc.raw)
			cfg, err := config.Load()
			if err != nil { t.Fatal(err) }
			if cfg.KiroLogLevel != tc.want {
				t.Fatalf("KiroLogLevel = %q, want %q", cfg.KiroLogLevel, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidKiroLogLevel(t *testing.T) {
	t.Setenv("KIRO_LOG_LEVEL", "verbose")
	_, err := config.Load()
	if err == nil || !strings.Contains(err.Error(), "KIRO_LOG_LEVEL") ||
		!strings.Contains(err.Error(), "ERROR, WARN, INFO, DEBUG, TRACE") {
		t.Fatalf("error = %v, want supported-level diagnostic", err)
	}
}
```

- [ ] **Step 2: Run the tests and verify RED**

```bash
go test ./internal/config -run 'TestLoadKiroLogLevel|TestLoadRejectsInvalidKiroLogLevel' -count=1
```

Expected: build failure because `Config.KiroLogLevel` is absent.

- [ ] **Step 3: Implement minimal level resolution**

```go
const defaultKiroLogLevel = "INFO"

func parseKiroLogLevel(raw string) (string, error) {
	level := strings.ToUpper(strings.TrimSpace(raw))
	if level == "" { return defaultKiroLogLevel, nil }
	switch level {
	case "ERROR", "WARN", "INFO", "DEBUG", "TRACE":
		return level, nil
	default:
		return "", fmt.Errorf(
			"KIRO_LOG_LEVEL: unsupported value %q (supported: ERROR, WARN, INFO, DEBUG, TRACE)", raw)
	}
}
```

Add `KiroLogLevel` beside the existing Kiro log fields. Resolve it in `Load`, append errors to `errs`, and store it in the returned `Config`.

- [ ] **Step 4: Verify configuration GREEN**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
go test ./internal/config -run 'TestLoadKiroLogLevel|TestLoadRejectsInvalidKiroLogLevel|TestLoadDefaults' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing child-environment tests**

Replace the old assertion forbidding level injection in `TestKiroProcessEnvironmentComposition` with:

```go
childEnv := kiroProcessEnv(config.Config{
	KiroChatLogFile: childLogFile,
	KiroLogLevel: "INFO",
})
want := []string{
	"KIRO_CHAT_LOG_FILE=" + childLogFile,
	"KIRO_LOG_LEVEL=INFO",
}
if diff := cmp.Diff(want, childEnv); diff != "" {
	t.Fatalf("child environment mismatch (-want +got):\n%s", diff)
}
```

Keep the real helper-process assertion, but expect child `INFO` to override parent `debug`. Extend the launch-record test to require `INFO`.

- [ ] **Step 6: Run launch tests and verify RED**

```bash
go test ./cmd/otto-gateway -run 'TestKiroProcessEnvironmentComposition|TestPrepareKiroLaunchMaterializesAndLogsDefaultAgent' -count=1
```

Expected: FAIL because only the file is injected and the startup record omits level.

- [ ] **Step 7: Implement propagation and admin metadata**

```go
func kiroProcessEnv(cfg config.Config) []string {
	level := cfg.KiroLogLevel
	if level == "" { level = "INFO" }
	return []string{
		"KIRO_CHAT_LOG_FILE=" + cfg.KiroChatLogFile,
		"KIRO_LOG_LEVEL=" + level,
	}
}
```

Add `chat_log_level` to the startup record. Add `KiroLogLevel` and `LogSourceLevels` to `admin.Deps`, wire `{"kiro": cfg.KiroLogLevel}` from main, and add a `KIRO_LOG_LEVEL` admin-doc row with default `INFO`, supported values, and current value.

- [ ] **Step 8: Verify the slice GREEN**

```bash
gofmt -w cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/admin.go
go test ./internal/config ./cmd/otto-gateway ./internal/admin -run 'KiroLog|KiroProcessEnvironment|PrepareKiroLaunch|Docs' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/admin.go
git commit -m "feat: default Kiro native logging to info"
```

---

### Task 2: Add browser-safe tail status and atomic attachment

**Files:**
- Create: `internal/admin/tail_status.go`
- Modify: `internal/admin/tail.go:150-260,319-450,536-595`
- Modify: `internal/admin/tail_test.go:1-75,135-235,451-610`
- Modify: `internal/admin/sse.go:99-134`

**Interfaces:**
- Consumes: `admin.Deps.LogSourceLevels` from Task 1.
- Produces: `TailState` constants `opening`, `missing`, `unreadable`, `empty`, and `watching`.
- Produces: `TailStatus` JSON fields `state`, `size_bytes`, `modified_at`, and optional `level`.
- Produces: `Attach(context.Context) (*subscriber, []string, TailStatus)`, `subscriber.StatusC`, and a size-one `subscriber.BackfillC chan []string` for lossless first-open history delivery.
- Produces: `TailerRegistry.Get(name, path, level string) *Tailer`.
- Preserves `Subscribe` and `Snapshot` as white-box compatibility helpers; production SSE moves to `Attach` in Task 5.

- [ ] **Step 1: Write failing schema, attachment, and file-state tests**

```go
func TestTailStatusJSONExcludesPathAndRawError(t *testing.T) {
	size := int64(0)
	status := TailStatus{State: TailStateEmpty, SizeBytes: &size, Level: "INFO"}
	body, err := json.Marshal(status)
	if err != nil { t.Fatal(err) }
	for _, forbidden := range []string{"path", "error", t.TempDir()} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, body)
		}
	}
}

func TestTailerAttachReturnsCoherentSnapshotAndOpeningStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	appendToFile(t, path, "existing")
	tailer := newTailer(path, "INFO", discardLogger())
	tailer.ring.Push("existing")
	sub, snapshot, status := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(sub)
	if diff := cmp.Diff([]string{"existing"}, snapshot); diff != "" {
		t.Fatalf("snapshot mismatch (-want +got):\n%s", diff)
	}
	if status.State != TailStateOpening || status.Level != "INFO" {
		t.Fatalf("status = %+v, want opening INFO", status)
	}
}
```

Add `waitStatus` and table cases: existing zero-byte file becomes `empty`; nonexistent file becomes `missing`; path below a regular-file parent becomes `unreadable`.

Add two recovery cases that keep the same subscription open. For `missing`, create the zero-byte file and require a transition to `empty`, then append a complete line and require `watching`. For `unreadable`, begin with a regular file where the parent directory must be, remove it, create the directory and log file, and require a transition to `watching`. These deterministic path transitions exercise the same retry/recovery branches as permission repair without depending on the test process's OS privileges.

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./internal/admin -run 'TestTailStatus|TestTailerAttach|TestTailerFileStates' -count=1
```

Expected: build failure because the status API is absent.

- [ ] **Step 3: Implement status types**

Create `tail_status.go`:

```go
package admin

type TailState string

const (
	TailStateOpening TailState = "opening"
	TailStateMissing TailState = "missing"
	TailStateUnreadable TailState = "unreadable"
	TailStateEmpty TailState = "empty"
	TailStateWatching TailState = "watching"
)

type TailStatus struct {
	State TailState `json:"state"`
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
	Level string `json:"level,omitempty"`
}
```

Add `level` and current `status` to `Tailer`; initialize them in unexported `newTailer(path, level, logger)`. Keep `NewTailer(path, logger)` delegating with an empty level.

- [ ] **Step 4: Implement atomic attachment and status coalescing**

Under `t.mu`, `Attach` registers a subscriber with buffered live-record `C`, size-one `BackfillC`, and size-one `StatusC`; copies the ring and status; and lazy-starts the run goroutine. Make `Subscribe` call `Attach` and discard the extra return values.

Implement `publishStatus` under `t.mu`: compare value fields, store changes, drain one stale `StatusC` value, and non-blockingly send the newest status. Close all three subscriber channels during `Unsubscribe`.

On open errors publish `missing` only for `fs.ErrNotExist`, otherwise `unreadable`. On successful stat publish `empty` for size zero or `watching` otherwise, including size and UTC RFC3339Nano modification time. Seek/stat/read failures publish `unreadable` and retain retries.

Update registry construction to `newTailer(path, level, logger)` and handler lookup to `Get(source, path, h.deps.LogSourceLevels[source])`.

- [ ] **Step 5: Verify GREEN under race**

```bash
gofmt -w internal/admin/tail_status.go internal/admin/tail.go internal/admin/tail_test.go internal/admin/sse.go
go test -race ./internal/admin -run 'TestTailStatus|TestTailerAttach|TestTailerFileStates|TestTailerRegistry|TestAdmin_TailerLazy' -count=1
```

Expected: PASS without races or goleaks.

- [ ] **Step 6: Commit**

```bash
git add internal/admin/tail_status.go internal/admin/tail.go internal/admin/tail_test.go internal/admin/sse.go
git commit -m "feat: track admin log source status"
```

---

### Task 3: Backfill bounded recent history on first successful open

**Files:**
- Create: `internal/admin/tail_backfill.go`
- Modify: `internal/admin/tail.go:300-450`
- Modify: `internal/admin/tail_test.go:222-450`
- Modify: `internal/admin/tail_timberjack_test.go:45-90`
- Modify: `internal/admin/regression_rel_http_05_test.go:40-90`

**Interfaces:**
- Consumes: `RingBufferLines` and `TailerMaxLineBytes`.
- Produces: `TailerInitialBackfillMaxBytes int64 = 8 * 1024 * 1024` and persistent `Tailer.initialLoaded`, which survives last-subscriber shutdown and lazy-run restart.
- Produces: `readInitialBackfill(f *os.File, maxLines int, maxBytes int64) (lines []string, partial string, size int64, err error)`.
- Consumes: `subscriber.BackfillC` from Task 2; one batch occupies one channel slot, so the existing 16-record live buffer cannot truncate a 500-record initial history.
- Preserves: every rotation/truncation reopen seeks to EOF without history replay.

- [ ] **Step 1: Write failing pure parser tests**

```go
func TestReadInitialBackfillKeepsLatestCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	appendToFile(t, path, "one", "two", "three", "four")
	f, err := os.Open(path)
	if err != nil { t.Fatal(err) }
	defer f.Close()
	lines, partial, _, err := readInitialBackfill(f, 2, 1024)
	if err != nil { t.Fatal(err) }
	if diff := cmp.Diff([]string{"three", "four"}, lines); diff != "" {
		t.Fatalf("lines mismatch (-want +got):\n%s", diff)
	}
	if partial != "" { t.Fatalf("partial = %q, want empty", partial) }
}

func TestReadInitialBackfillDropsLeadingFragmentAndCarriesTrailingPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	if err := os.WriteFile(path, []byte("discard-me\nkeep\nunfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil { t.Fatal(err) }
	defer f.Close()
	lines, partial, _, err := readInitialBackfill(
		f, 10, int64(len("ard-me\nkeep\nunfinished")))
	if err != nil { t.Fatal(err) }
	if diff := cmp.Diff([]string{"keep"}, lines); diff != "" {
		t.Fatalf("lines mismatch (-want +got):\n%s", diff)
	}
	if partial != "unfinished" { t.Fatalf("partial = %q", partial) }
}
```

- [ ] **Step 2: Run parser tests and verify RED**

```bash
go test ./internal/admin -run 'TestReadInitialBackfill' -count=1
```

Expected: build failure because the helper is absent.

- [ ] **Step 3: Implement the bounded parser**

Create `tail_backfill.go`. Use `f.Stat`; choose `start := max(0, size-maxBytes)`; read `[start,size)` with `ReadAt` so the live offset does not move; discard through the first newline when `start > 0`; split off trailing unterminated bytes; trim one trailing `\r` from complete records; cap records to `TailerMaxLineBytes`; retain only the final `maxLines` records; return the observed size.

- [ ] **Step 4: Verify parser GREEN**

```bash
gofmt -w internal/admin/tail_backfill.go internal/admin/tail_test.go
go test ./internal/admin -run 'TestReadInitialBackfill' -count=1
```

Expected: PASS.

- [ ] **Step 5: Write failing integration tests**

Replace the old no-preexisting-history assertion with:

```go
func TestTailerFirstOpenBackfillsLatest500ThenStreamsLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tail.log")
	for i := 0; i < RingBufferLines+25; i++ {
		appendToFile(t, path, fmt.Sprintf("pre-%03d", i))
	}
	tailer := NewTailer(path, discardLogger())
	sub, _, _ := tailer.Attach(t.Context())
	defer tailer.Unsubscribe(sub)
	var got []string
	select {
	case got = <-sub.BackfillC:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial backfill batch")
	}
	if len(got) != RingBufferLines || got[0] != "pre-025" || got[len(got)-1] != "pre-524" {
		t.Fatalf("boundary mismatch: len=%d first=%q last=%q", len(got), got[0], got[len(got)-1])
	}
	appendToFile(t, path, "live")
	if line := waitLine(sub.C, 2*time.Second); line != "live" {
		t.Fatalf("live line = %q", line)
	}
}
```

Add an integration case starting with `complete\npartial`, then appending `-done\n`; assert `partial-done` arrives exactly once. Update rotation tests so replacement history written before detection remains skipped while a later append arrives.

Add `TestTailerBackfillsOnlyOnceAcrossLazyRunRestart`: consume the first backfill, unsubscribe the last subscriber, attach again, assert the ring snapshot contains the historical records, and assert the second subscriber's `BackfillC` receives no replay before a later live append arrives on `C`.

Add `TestTailerInitialBackfillAttachmentHandoff`: attach subscriber A before calling `publishInitialBackfill([]string{"one", "two"})` and require an empty snapshot plus exactly one two-line batch; attach subscriber B afterward and require the same two lines in its snapshot plus no batch. This directly proves that the shared lock chooses exactly one delivery path at the attachment boundary.

- [ ] **Step 6: Run integration tests and verify RED**

```bash
go test ./internal/admin -run 'TestTailer(FirstOpenBackfills|InitialPartial|BackfillsOnlyOnce|InitialBackfillAttachmentHandoff)|TestAdmin_TailerRotation|TestAdmin_TailerSurvivesTimberjackRotate' -count=1
```

Expected: FAIL because first open still seeks directly to EOF.

- [ ] **Step 7: Integrate first-open backfill**

Add `initialLoaded bool` to `Tailer` rather than to `run`, so stopping and lazily restarting the run goroutine does not replay history. On a successful open, inspect that field under `t.mu`; when it is false call:

```go
lines, carry, size, err := readInitialBackfill(
	f, RingBufferLines, TailerInitialBackfillMaxBytes)
if err != nil {
	_ = f.Close()
	f = nil
	reader = nil
	partialLine = ""
	if t.logger != nil {
		t.logger.Warn("admin: tailer cannot read initial log history", "path", t.path, "err", err)
	}
	t.publishStatus(TailStatus{State: TailStateUnreadable, Level: t.level})
	return
}
if _, err = f.Seek(size, io.SeekStart); err != nil {
	_ = f.Close()
	f = nil
	reader = nil
	partialLine = ""
	if t.logger != nil {
		t.logger.Warn("admin: tailer cannot seek after initial log history", "path", t.path, "err", err)
	}
	t.publishStatus(TailStatus{State: TailStateUnreadable, Level: t.level})
	return
}
reader = bufio.NewReaderSize(f, 64*1024)
partialLine = carry
lastSize = size
t.publishInitialBackfill(lines)
```

Implement `publishInitialBackfill` under `t.mu` only after both the bounded read and seek succeed: push every line into the ring, set `t.initialLoaded = true` even when the file has no complete records, then—when `lines` is non-empty—non-blockingly send one copied `[]string` batch to each subscriber's size-one `BackfillC`. This creates an atomic handoff: an attachment before publication receives the batch, while an attachment after publication receives the same records in its snapshot, without duplication or loss. For any successful open when `t.initialLoaded` is already true, and for later rotation/truncation reopens, reset the partial carry, seek EOF, and never call `readInitialBackfill`.

- [ ] **Step 8: Verify all tail behavior GREEN**

```bash
gofmt -w internal/admin/tail_backfill.go internal/admin/tail.go internal/admin/tail_test.go internal/admin/tail_timberjack_test.go internal/admin/regression_rel_http_05_test.go
go test -race ./internal/admin -run 'TestReadInitialBackfill|TestTailer|TestAdmin_Tailer|TestAdmin_RingBuffer' -count=1
```

Expected: PASS; rotation history remains absent.

- [ ] **Step 9: Commit**

```bash
git add internal/admin/tail_backfill.go internal/admin/tail.go internal/admin/tail_test.go internal/admin/tail_timberjack_test.go internal/admin/regression_rel_http_05_test.go
git commit -m "feat: backfill recent admin log history"
```

---

### Task 4: Rate-limit file failures and log recovery

**Files:**
- Modify: `internal/admin/tail_status.go`
- Modify: `internal/admin/tail.go:175-198,319-450`
- Modify: `internal/admin/tail_test.go:451-555`
- Modify: `internal/admin/regression_rel_obsv_04_test.go:1-70`

**Interfaces:**
- Produces: `TailerFailureWarnInterval = time.Minute`.
- Produces: `Tailer.now func() time.Time`, default `time.Now`.
- Produces: `recordFileFailure(TailState, error)` and `recordFileRecovery()`.
- Preserves server-side path/error diagnostics while browser status remains sanitized.

- [ ] **Step 1: Write failing deterministic limiter tests**

```go
func TestTailerFileFailureWarningsAreRateLimitedAndRecoveryLogged(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tailer := newTailer("/private/source.log", "INFO", logger)
	tailer.now = func() time.Time { return now }
	tailer.recordFileFailure(TailStateMissing, fs.ErrNotExist)
	tailer.recordFileFailure(TailStateMissing, fs.ErrNotExist)
	if got := strings.Count(logs.String(), "tailer cannot open log"); got != 1 {
		t.Fatalf("warning count = %d, want 1: %s", got, logs.String())
	}
	now = now.Add(TailerFailureWarnInterval)
	tailer.recordFileFailure(TailStateMissing, fs.ErrNotExist)
	if got := strings.Count(logs.String(), "tailer cannot open log"); got != 2 {
		t.Fatalf("warning count after interval = %d, want 2", got)
	}
	tailer.recordFileRecovery()
	if !strings.Contains(logs.String(), "tailer log source recovered") {
		t.Fatalf("missing recovery record: %s", logs.String())
	}
}
```

Add a case changing from `missing` to `unreadable` before the interval and require an immediate second warning.

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./internal/admin -run 'TestTailerFileFailureWarnings' -count=1
```

Expected: build failure because limiter members are absent.

- [ ] **Step 3: Implement limiter and recovery**

Track last failure state, stable error class, and warning time on `Tailer`. Classify not-exist, permission, and the concrete error type fallback. Warn when class/state changes or the interval elapsed. Recover once after a prior failure and clear limiter fields. Replace every direct open, stat, seek, live-read, and initial-backfill warning with `recordFileFailure`; call `recordFileRecovery` only after open, initial read, and seek all succeed.

- [ ] **Step 4: Verify GREEN**

```bash
gofmt -w internal/admin/tail_status.go internal/admin/tail.go internal/admin/tail_test.go internal/admin/regression_rel_obsv_04_test.go
go test -race ./internal/admin -run 'TestTailerFileFailureWarnings|TestAdmin_TailerMissingFileGracefulRetry|TestREL_OBSV_04' -count=1
```

Expected: PASS with bounded warnings and one recovery record.

- [ ] **Step 5: Commit**

```bash
git add internal/admin/tail_status.go internal/admin/tail.go internal/admin/tail_test.go internal/admin/regression_rel_obsv_04_test.go
git commit -m "fix: rate limit admin tailer file warnings"
```

---

### Task 5: Stream initial and transition status over SSE

**Files:**
- Modify: `internal/admin/sse.go:56-270`
- Modify: `internal/admin/sse_test.go:20-735`
- Modify: `internal/admin/regression_rel_http_07_test.go:70-195`

**Interfaces:**
- Consumes: `Tailer.Attach`, `subscriber.BackfillC`, `subscriber.StatusC`, and `TailStatus`.
- Produces: `writeSSEStatus(io.Writer, TailStatus)`.
- Changes: `sseLoop` receives `initialStatus TailStatus` and selects on `sub.StatusC`.
- Preserves ping cadence, shutdown channel, unknown-source `400`, flusher failure, and the single response-writer goroutine.

- [ ] **Step 1: Write failing status-frame and flush tests**

```go
func TestWriteSSEStatusUsesBrowserSafeJSON(t *testing.T) {
	size := int64(0)
	status := TailStatus{State: TailStateEmpty, SizeBytes: &size, Level: "INFO"}
	var body strings.Builder
	writeSSEStatus(&body, status)
	want := "event: status\ndata: {\"state\":\"empty\",\"size_bytes\":0,\"level\":\"INFO\"}\n\n"
	if body.String() != want { t.Fatalf("frame = %q, want %q", body.String(), want) }
}
```

Add a direct-loop test with empty snapshot and no ticker event; assert one immediate flush containing initial status. Add a handler test observing `opening` followed by `empty` for an empty file. Add a direct-loop ordering test that supplies a two-line `BackfillC` batch, then a live `C` record and a `StatusC` transition, and asserts all three event types are framed on the same response while each backfill record precedes the live record.

- [ ] **Step 2: Run tests and verify RED**

```bash
go test ./internal/admin -run 'TestWriteSSEStatus|TestAdmin_SSEInitialStatus|TestAdmin_SSEEmptySourceStatusTransition|TestAdmin_SSEBackfillBeforeLive' -count=1
```

Expected: build failure because status framing is absent.

- [ ] **Step 3: Implement framing and coherent handler attachment**

```go
func writeSSEStatus(w io.Writer, status TailStatus) {
	payload, err := json.Marshal(status)
	if err != nil { return }
	writeSSELine(w, "status", string(payload))
}
```

Use `sub, snapshot, initialStatus := tailer.Attach(r.Context())`. At loop start write status, then snapshot logs, then flush unconditionally. Add a `sub.BackfillC` select arm that expands the batch into ordered `log` frames and flushes once. Before writing a selected live `sub.C` record, perform a non-blocking `BackfillC` drain and write any pending batch first; because the tailer enqueues its sole batch before it can enqueue a live record, this preserves backfill-before-live ordering even when both channels are ready in the outer `select`. Add a `sub.StatusC` select arm that writes/flushed `status`. A closed subscriber channel returns a clear loop error.

- [ ] **Step 4: Update existing loop/backfill tests**

Pass `TailStatus{State: TailStateOpening}` to all direct `sseLoop` calls. Remove manual `tailer.ring.Push` from `TestAdmin_SSEBackfillAndLive`; prepopulate the actual file and assert real first-open backfill.

- [ ] **Step 5: Verify the complete admin package GREEN**

```bash
gofmt -w internal/admin/sse.go internal/admin/sse_test.go internal/admin/regression_rel_http_07_test.go
go test -race ./internal/admin -count=1
```

Expected: PASS without races or goleaks.

- [ ] **Step 6: Commit**

```bash
git add internal/admin/sse.go internal/admin/sse_test.go internal/admin/regression_rel_http_07_test.go
git commit -m "feat: stream admin log source status"
```

---

### Task 6: Render precise dashboard source and filter states

**Files:**
- Modify: `internal/admin/static/js/admin.js:718-800,940-1110,1128-1295`
- Modify: `internal/admin/admin_js_test.js:1-270`
- Modify: `internal/admin/templates/dashboard.html.tmpl:140-190`

**Interfaces:**
- Consumes: SSE `status` JSON and snapshot `log_source_labels`.
- Produces: `currentLogFileStatus`, `logSourceLabels`, `logSourceLabel(id)`, `onLogStatus(ev)`, and `refreshLogEmptyState()` inside the admin closure.
- Preserves parser, deduplication, pause buffer, regex debounce, row cap, and `textContent`-only rendering.

- [ ] **Step 1: Upgrade the browser harness and write failing source-state tests**

Make `FakeEventSource` retain and emit listeners:

```js
constructor(url) {
  this.url = url;
  this.closed = false;
  this.listeners = {};
  eventSources.push(this);
}

addEventListener(type, listener) {
  this.listeners[type] = listener;
}

emit(type, data = '') {
  if (this.listeners[type]) this.listeners[type]({ data });
  if (type === 'open' && this.onopen) this.onopen();
  if (type === 'error' && this.onerror) this.onerror();
}
```

Add viewport, empty element, activity dot, level selector, and grep input to the harness selectors. Extend `Element` with `dataset`, `style`, `classList.remove`, and recursive class-based `querySelectorAll` so real row/filter code executes. Capture `{callback, delay}` in the fake `setTimeout`, expose `runTimeout(delay)`, and use the `150` millisecond callback in the regex test while leaving the `2000` millisecond dedup timer untouched.

```js
test('Kiro empty status includes its effective level', async () => {
  const harness = createHarness([snapshot({ main: 'Gateway', kiro: 'Kiro' })]);
  harness.start();
  await settleSnapshot();
  harness.sourceSelect.value = 'kiro';
  harness.sourceSelect.dispatchEvent({ type: 'change' });
  const source = harness.eventSources[1];
  source.emit('status', JSON.stringify({ state: 'empty', size_bytes: 0, level: 'DEBUG' }));
  assert.equal(
    harness.selectors['[data-log-empty]'].textContent,
    'Kiro log is empty. Logging is configured at DEBUG; waiting for the first entry.',
  );
});
```

Add exact-copy tests for `opening`, `missing`, `unreadable`, generic `empty`, and `watching`. Emit `open` and require `Connected — Kiro`, not the internal ID.

- [ ] **Step 2: Run browser tests and verify RED**

```bash
node --test internal/admin/admin_js_test.js
```

Expected: FAIL because status listeners and source-aware copy are absent.

- [ ] **Step 3: Implement status parsing and friendly labels**

```js
var logSourceLabels = {};
var currentLogFileStatus = { state: 'opening' };

function logSourceLabel(source) {
  return logSourceLabels[source] || source;
}
```

Persist labels in `populateLogSources`; use the helper in connecting/open text. `onLogStatus` must catch malformed JSON, accept only the five defined states, store valid payloads, and call `refreshLogEmptyState`. Register it with `addEventListener('status', onLogStatus)`.

`refreshLogEmptyState` counts all rows and visible rows. Visible rows hide the placeholder. Existing rows with zero visible rows show `Log entries were received, but none match the current filters.` No rows render the approved copy for the current status; only Kiro empty interpolates `level || 'INFO'`.

- [ ] **Step 4: Write failing filter-empty tests**

Emit a real `log` event, choose an unmatched level, dispatch `change`, and require filter-empty copy. Return to `all` and require the row visible and placeholder hidden. Add the equivalent regex case by running the captured debounce callback.

- [ ] **Step 5: Run tests and verify RED for recomputation**

```bash
node --test internal/admin/admin_js_test.js
```

Expected: source-state tests pass but filter-empty tests fail until filter handlers refresh placeholder state.

- [ ] **Step 6: Recompute state after every row/filter/source transition**

Call `refreshLogEmptyState()` after append, level-filter loop, successful regex-filter loop, pause flush, and viewport clear. On source switch set `currentLogFileStatus = { state: 'opening' }`. Change template initial copy to `Checking log source…`.

- [ ] **Step 7: Verify browser and template GREEN**

```bash
node --test internal/admin/admin_js_test.js
go test ./internal/admin -run 'Dashboard|Template|Assets' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/admin/static/js/admin.js internal/admin/admin_js_test.js internal/admin/templates/dashboard.html.tmpl
git commit -m "feat: explain admin log source state"
```

---

### Task 7: Update operator configuration and run complete verification

**Files:**
- Modify: `scripts/.env.example:20-40`
- Modify: `docs/operating.md:408-445,748-770`
- Create: `tests/scripts/test-kiro-log-defaults.sh`

**Interfaces:**
- Consumes: final contracts from Tasks 1-6.
- Produces: generated and operator documentation matching the `INFO` default, supported overrides, bounded history, and file-health messages.

- [ ] **Step 1: Write a failing generated-template test**

Create `tests/scripts/test-kiro-log-defaults.sh`:

```bash
#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -P "$(dirname "$0")/../.." && pwd)"
template="$repo_root/scripts/.env.example"

[[ "$(awk '$0 == "KIRO_LOG_LEVEL=INFO" { count++ } END { print count + 0 }' "$template")" -eq 1 ]]
grep -Fq '# ERROR, WARN, INFO, DEBUG, TRACE. Restart after changing the level.' "$template"
```

Run it:

```bash
bash tests/scripts/test-kiro-log-defaults.sh
```

Expected before editing: exit non-zero because the active default is absent.

- [ ] **Step 2: Update generated configuration**

Add under the Kiro subprocess section:

```dotenv
# Native Kiro file logging is always enabled. Supported levels:
# ERROR, WARN, INFO, DEBUG, TRACE. Restart after changing the level.
KIRO_LOG_LEVEL=INFO
```

- [ ] **Step 3: Update operator documentation**

In `docs/operating.md`:

- list `INFO` as the default and all five supported values;
- describe `DEBUG` as an explicit override and removal as return to `INFO`;
- document up to 500 recent complete records on first source open;
- document missing, unreadable, empty, watching, disconnected, and no-filter-match messages;
- remove the claim that an absent/empty source is always merely a graceful indefinite wait.

- [ ] **Step 4: Verify documentation GREEN**

```bash
bash tests/scripts/test-kiro-log-defaults.sh
! rg -n '_\(unset; Kiro normal logging\)_' docs/operating.md
rg -n 'ERROR, WARN, INFO, DEBUG, TRACE|up to 500|Log file has not been created|none match the current filters' scripts/.env.example docs/operating.md
```

Expected: all commands exit zero.

- [ ] **Step 5: Run fresh targeted verification**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go cmd/otto-gateway/main.go cmd/otto-gateway/main_test.go internal/admin/*.go
go test -race ./internal/config ./cmd/otto-gateway ./internal/admin -count=1
node --test internal/admin/admin_js_test.js
```

Expected: all packages and Node subtests PASS without race/goleak failures.

- [ ] **Step 6: Run fresh repository-wide verification**

```bash
go test ./... -count=1
go build ./...
golangci-lint run ./...
git diff --check
```

Expected: every command exits zero.

- [ ] **Step 7: Run isolated Kiro runtime smoke check**

Create a temporary directory with `mktemp -d`, copy `internal/embed/acp_proxy.json` to its `.kiro/agents/acp_proxy.json`, and run `kiro-cli acp --agent acp_proxy` from that isolated workspace with stdin closed plus a temporary `KIRO_CHAT_LOG_FILE` and `KIRO_LOG_LEVEL=INFO`. Assert the temporary log exists with non-zero size. Remove only the directory returned by `mktemp -d`. Do not restart or signal the user's running Gateway or workers.

- [ ] **Step 8: Review the final diff against acceptance criteria**

```bash
git status --short
git diff --stat
git diff --check
```

Confirm level propagation, first-open-only bounded history, EOF-only rotation, all status messages, warning suppression, sanitized status JSON, and absence of unrelated edits.

- [ ] **Step 9: Commit**

```bash
git add scripts/.env.example docs/operating.md tests/scripts/test-kiro-log-defaults.sh
git commit -m "docs: explain Kiro and log tail status defaults"
```
