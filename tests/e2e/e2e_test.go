//go:build e2e

// Package e2e_test is the black-box end-to-end suite for OTTO Gateway.
//
// It boots the REAL otto-gateway binary against REAL kiro-cli, drives it
// over HTTP, and asserts on JSON / SSE shapes only — it never imports
// otto-gateway/internal/*. This is the automated counterpart to the manual
// HUMAN-UAT steps 1, 2, 3, 6 (health, auth, non-streaming + streaming
// Anthropic round-trips, surface gating / fail-fast), plus an opt-in Node
// @anthropic-ai/sdk harness for steps 4-5 (TestE2E_SDK_RoundTrip).
//
// Two gates keep the default `go test ./...` path clean:
//
//  1. The `e2e` build tag: this file only compiles under `-tags e2e`, so the
//     default test run never sees it.
//  2. The OTTO_E2E env gate: even compiled, every test skips unless
//     OTTO_E2E=1, and TestMain skips the (expensive) temp-binary build.
//
// kiro resolution mirrors internal/adapter/anthropic/integration_test.go:
// OTTO_KIRO_BIN env wins, else kiro-cli on PATH, else skip. Warmup failure
// (typically kiro-cli auth-not-refreshed in dev) is a skip, not a failure.
//
// All HTTP / subprocess calls thread a context.Context so the suite passes
// the project's noctx trust gate (.golangci.yml).
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// builtBinary holds the absolute path to the otto-gateway binary TestMain
// compiles once (only when OTTO_E2E=1). Empty when the gate is off.
var builtBinary string

// moduleRoot is the path from the test process CWD (tests/e2e/) up to the
// Go module root. The test package lives in tests/e2e/, so the module root
// is two levels up: tests/e2e/ -> tests -> root == "../..".
const moduleRoot = "../.."

// TestMain builds the real binary exactly once before running the suite,
// but ONLY when OTTO_E2E=1. With the gate off it runs m.Run() immediately
// (every test self-skips via gateOrSkip) so the gate-skip path is cheap and
// never invokes the Go build toolchain.
//
// Phase 6 Plan 06-05 (iteration-3 fix to MEDIUM #6): TestMain ALSO compiles
// the controllable fake-kiro-cli used by tests/e2e/tools_*_test.go into
// os.TempDir()/fake-kiro-cli-<pid>. The package-level fakeKiroBinaryPath var
// (declared in tools_fixtures.go) is set BEFORE m.Run so every subtest sees
// a valid path. defer-delete on m.Run() exit. This avoids the iteration-2
// bug where sync.Once + t.TempDir() left a cached path to a deleted binary
// after the first subtest cleaned up its temp dir.
func TestMain(m *testing.M) {
	if os.Getenv("OTTO_E2E") != "1" {
		os.Exit(m.Run())
	}
	// CR-02 (Phase 6 review): delegate to runE2E so every cleanup path
	// (temp dirs, fake-kiro binary) runs via defer, even when a build
	// step fails. The previous shape called os.Exit(2) on fake-kiro
	// build failure, which bypassed the defer that removed the temp
	// dir — leaking otto-e2e-* dirs on every failed suite invocation.
	os.Exit(runE2E(m))
}

// runE2E builds the otto-gateway and fake-kiro-cli binaries, wires the
// shared package-level paths (builtBinary, fakeKiroBinaryPath), runs the
// suite, and returns the exit code. Cleanup of the temp build dir and
// the fake-kiro binary is registered via defer so partial-failure paths
// (e.g., fake-kiro build fails AFTER the otto-gateway build succeeded)
// still leave a clean filesystem behind.
func runE2E(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "otto-e2e-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: MkdirTemp: %v\n", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	out := filepath.Join(tmp, "otto-gateway")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// out is derived from os.MkdirTemp (test-controlled, not external input);
	// the build target is a fixed package literal. Safe subprocess spawn.
	build := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/otto-gateway") //nolint:gosec // G204: test-controlled paths only
	// The build must run from the module root; the test process CWD is
	// tests/e2e/, so point cmd.Dir at the module root.
	build.Dir = moduleRoot
	var buildErr bytes.Buffer
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build otto-gateway failed: %v\n%s\n", err, buildErr.String())
		return 1
	}

	abs, err := filepath.Abs(out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: resolve binary path: %v\n", err)
		return 1
	}
	builtBinary = abs

	// Compile fake-kiro-cli into os.TempDir() with a per-pid suffix so the
	// path survives all subtests (iteration-3 fix to MEDIUM #6 — package-level
	// lifetime, not per-test temp dir). See tools_testmain_test.go for the
	// lifetime contract documentation.
	fakeKiroOut := filepath.Join(os.TempDir(), fmt.Sprintf("fake-kiro-cli-%d", os.Getpid()))
	// CR-02: register the defer BEFORE attempting the build. If the build
	// itself fails (or filepath.Abs below fails), the defer still removes
	// any partial output. Removing a non-existent file is a no-op.
	defer func() { _ = os.Remove(fakeKiroOut) }()
	fakeBuild := exec.CommandContext(ctx, "go", "build", "-o", fakeKiroOut, "./tests/e2e/cmd/fake-kiro-cli") //nolint:gosec // G204: fixed package literal
	fakeBuild.Dir = moduleRoot
	var fakeBuildErr bytes.Buffer
	fakeBuild.Stderr = &fakeBuildErr
	if err := fakeBuild.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build fake-kiro-cli failed: %v\n%s\n", err, fakeBuildErr.String())
		return 1
	}
	fakeKiroAbs, err := filepath.Abs(fakeKiroOut)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: resolve fake-kiro-cli path: %v\n", err)
		return 1
	}
	fakeKiroBinaryPath = fakeKiroAbs

	return m.Run()
}

// gateOrSkip is the top gate every test func calls first. With OTTO_E2E
// unset all subtests skip uniformly (and cheaply — TestMain built nothing).
func gateOrSkip(t *testing.T) {
	t.Helper()
	if os.Getenv("OTTO_E2E") != "1" {
		t.Skip("set OTTO_E2E=1 to run the E2E suite")
	}
}

// resolveKiro mirrors resolveKiroCLI from the Anthropic integration test:
// OTTO_KIRO_BIN env wins; else kiro-cli on PATH; else skip. The OTTO_E2E
// top gate is handled separately by gateOrSkip so the skip reasons stay
// distinct (gate-off vs kiro-missing).
func resolveKiro(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("OTTO_KIRO_BIN"); bin != "" {
		return bin
	}
	p, err := exec.LookPath("kiro-cli")
	if err != nil {
		t.Skip("kiro-cli not on PATH (set OTTO_KIRO_BIN to override)")
	}
	return p
}

// freePort asks the kernel for an ephemeral loopback port, closes the
// listener, and returns the "127.0.0.1:NNNNN" address. There is a small
// race between close and the gateway re-binding it — this is the standard
// test pattern and acceptable here.
func freePort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("freePort: close: %v", err)
	}
	return addr
}

// bootGateway starts the real binary on a free loopback port, polls /health
// until it returns 200, and returns the base URL plus a cleanup closure the
// caller MUST defer. extraEnv overlays (and wins over) the baseline env
// (HTTP_ADDR, AUTH_TOKEN, KIRO_CMD).
//
// On early process exit OR a 15s warmup timeout the helper kills the process
// (if alive) and t.Skipf with captured stderr — mirroring the kiroSetup
// skip-on-warmup-failure policy (auth-not-refreshed dev kiro is not a bug in
// the gateway).
func bootGateway(t *testing.T, extraEnv map[string]string) (string, func()) {
	t.Helper()
	kiro := resolveKiro(t)
	addr := freePort(t)
	baseURL := "http://" + addr

	env := append(
		os.Environ(),
		"HTTP_ADDR="+addr,
		"AUTH_TOKEN=e2e-token",
		"KIRO_CMD="+kiro,
	)
	for k, v := range extraEnv {
		env = append(env, k+"="+v)
	}

	// The gateway runs for the lifetime of the test; CommandContext ties it
	// to a context the cleanup closure cancels (satisfies noctx; the
	// graceful SIGINT path below still runs first).
	procCtx, cancelProc := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, builtBinary)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancelProc()
		t.Fatalf("bootGateway: start binary: %v", err)
	}

	// waitDone fires once the process exits (warmup failure exits non-zero
	// BEFORE the listener accepts, so we must notice an early exit instead
	// of polling /health forever).
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	// killProcess interrupts gracefully, then hard-kills (via context
	// cancel) on a 5s timeout.
	killProcess := func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case err := <-waitDone:
			if err != nil {
				t.Logf("bootGateway cleanup: gateway exited (expected non-zero on interrupt): %v", err)
			}
		case <-time.After(5 * time.Second):
			cancelProc()
			<-waitDone
			t.Logf("bootGateway cleanup: gateway did not exit on SIGINT within 5s; killed")
		}
		cancelProc()
	}

	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()

	for {
		// Notice early exit (warmup failure) before the next poll.
		select {
		case err := <-waitDone:
			cancelProc()
			t.Skipf("gateway exited before warmup completed (likely kiro-cli auth-not-refreshed): %v\nstderr:\n%s", err, stderr.String())
		default:
		}

		if healthOK(client, baseURL) {
			return baseURL, killProcess
		}

		select {
		case err := <-waitDone:
			cancelProc()
			t.Skipf("gateway exited before warmup completed (likely kiro-cli auth-not-refreshed): %v\nstderr:\n%s", err, stderr.String())
		case <-deadline:
			killProcess()
			t.Skipf("gateway warmup failed within 15s (likely kiro-cli auth-not-refreshed); stderr:\n%s", stderr.String())
		case <-tick.C:
		}
	}
}

// healthOK does a single context-bounded GET /health and reports whether it
// returned 200. Errors (connection refused during warmup) report false.
func healthOK(client *http.Client, baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// postMessages POSTs body to /v1/messages with the standard Anthropic
// headers (Content-Type + anthropic-version) plus any supplied headers.
func postMessages(t *testing.T, baseURL string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("postMessages: new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("postMessages: do: %v", err)
	}
	return resp
}

// minimalAnthropicBody is the smallest valid /v1/messages body for the
// no-auth 401 probe (we never read kiro for it — auth rejects first).
var minimalAnthropicBody = []byte(`{"model":"auto","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"hi"}]}`)

// assertMessageShape decodes an Anthropic non-streaming message body and
// asserts the load-bearing fields (type/role/stop_reason/content[0]).
func assertMessageShape(t *testing.T, resp *http.Response) {
	t.Helper()
	var msg struct {
		Type       string  `json:"type"`
		Role       string  `json:"role"`
		StopReason *string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msg.Type != "message" {
		t.Errorf("type: got %q, want message", msg.Type)
	}
	if msg.Role != "assistant" {
		t.Errorf("role: got %q, want assistant", msg.Role)
	}
	if msg.StopReason == nil {
		t.Error("stop_reason: got nil, want non-nil")
	}
	if len(msg.Content) == 0 {
		t.Fatal("content: empty (kiro-cli returned no blocks)")
	}
	if msg.Content[0].Type != "text" {
		t.Errorf("content[0].type: got %q, want text", msg.Content[0].Type)
	}
	if msg.Content[0].Text == "" {
		t.Error("content[0].text: empty")
	}
}

// TestE2E_SharedGateway boots ONE gateway and runs the auth + shape + stream
// cases as subtests sharing that boot (one warmup, several round-trips).
func TestE2E_SharedGateway(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	t.Run("Health", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
		if err != nil {
			t.Fatalf("health request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("health get: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("health status: got %d, want 200", resp.StatusCode)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("health body not JSON object: %v", err)
		}
	})

	t.Run("Unauthorized", func(t *testing.T) {
		resp := postMessages(t, baseURL, minimalAnthropicBody, nil)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("no-auth status: got %d, want 401", resp.StatusCode)
		}
	})

	nonStreamBody := []byte(`{"model":"auto","max_tokens":256,"stream":false,"messages":[{"role":"user","content":"say hi"}]}`)

	t.Run("NonStreaming_XApiKey", func(t *testing.T) {
		resp := postMessages(t, baseURL, nonStreamBody, map[string]string{"x-api-key": "e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			raw := readAll(resp)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}
		assertMessageShape(t, resp)
	})

	t.Run("NonStreaming_Bearer", func(t *testing.T) {
		resp := postMessages(t, baseURL, nonStreamBody, map[string]string{"Authorization": "Bearer e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			raw := readAll(resp)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}
		assertMessageShape(t, resp)
	})

	t.Run("Streaming_SSE", func(t *testing.T) {
		streamBody := []byte(`{"model":"auto","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"say hi"}]}`)
		resp := postMessages(t, baseURL, streamBody, map[string]string{"Authorization": "Bearer e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			raw := readAll(resp)
			t.Fatalf("status: got %d, want 200; body=%s", resp.StatusCode, raw)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
			t.Errorf("Content-Type: got %q, want text/event-stream prefix", ct)
		}
		assertStrictSSE(t, resp)
	})
}

// assertStrictSSE walks the SSE body with the strict frame state machine
// mirrored from internal/adapter/anthropic/integration_test.go:
//
//	expectingEvent -> expectingData -> expectingBlank -> expectingEvent
//
// Each transition is gated on exact byte prefixes ("event: " / "data: " /
// "") with a single space; any deviation fails with the offending line.
// It then asserts the canonical event ordering and that no error event
// appeared on the happy path.
func assertStrictSSE(t *testing.T, resp *http.Response) {
	t.Helper()

	knownEvents := map[string]struct{}{
		"message_start":       {},
		"content_block_start": {},
		"content_block_delta": {},
		"content_block_stop":  {},
		"message_delta":       {},
		"message_stop":        {},
		"ping":                {},
		"error":               {},
	}

	type frameState int
	const (
		expectingEvent frameState = iota
		expectingData
		expectingBlank
	)
	state := expectingEvent

	var (
		events   []string
		sawError bool
	)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var currentEvent string
	lineIdx := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineIdx++

		switch state {
		case expectingEvent:
			if !strings.HasPrefix(line, "event: ") {
				t.Fatalf("strict framing violation at line %d: state=expectingEvent, got=%q", lineIdx, line)
			}
			name := strings.TrimPrefix(line, "event: ")
			if _, ok := knownEvents[name]; !ok {
				t.Fatalf("strict framing violation at line %d: unknown event name %q", lineIdx, name)
			}
			currentEvent = name
			events = append(events, name)
			if name == "error" {
				sawError = true
			}
			state = expectingData

		case expectingData:
			if !strings.HasPrefix(line, "data: ") {
				t.Fatalf("strict framing violation at line %d: state=expectingData (after event %q), got=%q", lineIdx, currentEvent, line)
			}
			payload := strings.TrimPrefix(line, "data: ")
			var anyVal any
			if err := json.Unmarshal([]byte(payload), &anyVal); err != nil {
				t.Fatalf("strict framing violation at line %d: data payload not valid JSON: %v; payload=%s", lineIdx, err, payload)
			}
			state = expectingBlank

		case expectingBlank:
			if line != "" {
				t.Fatalf("strict framing violation at line %d: state=expectingBlank (after data of event %q), got=%q", lineIdx, currentEvent, line)
			}
			state = expectingEvent
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner: %v", err)
	}

	if state != expectingEvent {
		t.Errorf("stream ended mid-frame: final state=%d (want expectingEvent=%d)", state, expectingEvent)
	}

	if len(events) == 0 {
		t.Fatal("no events received")
	}
	if events[0] != "message_start" {
		t.Errorf("first event: got %q, want message_start", events[0])
	}
	if events[len(events)-1] != "message_stop" {
		t.Errorf("last event: got %q, want message_stop", events[len(events)-1])
	}
	want := []string{"content_block_start", "content_block_delta", "content_block_stop", "message_delta"}
	for _, w := range want {
		if !containsEvent(events, w) {
			t.Errorf("missing required event %q in sequence=%v", w, events)
		}
	}
	if sawError {
		t.Error("error event observed on happy path — kiro-cli or adapter regression")
	}
	t.Logf("streaming: %d frames, sequence=%v", len(events), events)
}

func containsEvent(events []string, want string) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// readAll drains a response body for error logging (best-effort).
func readAll(resp *http.Response) string {
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return buf.String()
}

// TestE2E_SurfaceGating_OllamaOnly boots with only the Ollama surface and
// proves /v1/messages is NOT mounted (404) while /api/chat IS mounted (any
// status other than 404 proves the route exists — we do not require a full
// Ollama round-trip here).
func TestE2E_SurfaceGating_OllamaOnly(t *testing.T) {
	gateOrSkip(t)
	baseURL, cleanup := bootGateway(t, map[string]string{"ENABLED_SURFACES": "ollama"})
	defer cleanup()

	t.Run("AnthropicNotMounted", func(t *testing.T) {
		resp := postMessages(t, baseURL, minimalAnthropicBody, map[string]string{"Authorization": "Bearer e2e-token"})
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("/v1/messages status with anthropic disabled: got %d, want 404", resp.StatusCode)
		}
	})

	t.Run("OllamaMounted", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		body := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/chat", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer e2e-token")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		// Route mounted == any status other than 404. We deliberately do
		// NOT assert 200 — full Ollama round-trip correctness is covered
		// elsewhere; here we only prove the surface is wired.
		if resp.StatusCode == http.StatusNotFound {
			t.Fatalf("/api/chat status with ollama enabled: got 404, want route mounted (any non-404)")
		}
	})
}

// TestE2E_SurfaceGating_TypoFailFast proves a misspelled surface name aborts
// startup non-zero AND names the offending value on stderr (D-16 fail-fast).
// It execs the binary directly (no /health poll) and waits for an early exit.
func TestE2E_SurfaceGating_TypoFailFast(t *testing.T) {
	gateOrSkip(t)
	_ = resolveKiro(t) // skip uniformly when kiro env is absent

	addr := freePort(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtBinary)
	cmd.Env = append(
		os.Environ(),
		"HTTP_ADDR="+addr,
		"ENABLED_SURFACES=anthrpic", // deliberate typo
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start binary: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		// Config load fails before the listener; the process must exit
		// non-zero. exec.Wait returns *exec.ExitError on non-zero exit.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected non-zero exit, got err=%v; stderr:\n%s", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "anthrpic") {
			t.Errorf("fail-fast stderr must name the offending surface %q; stderr:\n%s", "anthrpic", stderr.String())
		}
	case <-time.After(5 * time.Second):
		cancel()
		<-waitDone
		t.Fatal("binary did not exit within 5s on a bad ENABLED_SURFACES value (fail-fast broken?)")
	}
}

// TestE2E_SDK_RoundTrip is the opt-in Node @anthropic-ai/sdk harness for
// HUMAN-UAT steps 4-5. It skips cleanly when node is absent OR the harness
// is not installed (no tests/e2e/sdk/node_modules and OTTO_E2E_SDK unset).
// When ready it boots the gateway, points ANTHROPIC_BASE_URL at it, and runs
// the .mjs round-trip (non-stream + stream), asserting exit code 0.
func TestE2E_SDK_RoundTrip(t *testing.T) {
	gateOrSkip(t)

	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed — run: make e2e-sdk-setup")
	}
	// CWD is tests/e2e/, so the relative node_modules path is "sdk/node_modules".
	_, statErr := os.Stat("sdk/node_modules")
	if statErr != nil && os.Getenv("OTTO_E2E_SDK") != "1" {
		t.Skip("SDK harness not installed — run: make e2e-sdk-setup (or set OTTO_E2E_SDK=1)")
	}

	baseURL, cleanup := bootGateway(t, nil)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// The .mjs is referenced from the module root, so run it from "../..".
	cmd := exec.CommandContext(ctx, "node", "tests/e2e/sdk/sdk_roundtrip.mjs")
	cmd.Dir = moduleRoot
	cmd.Env = append(
		os.Environ(),
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY=e2e-token",
	)
	combined, runErr := cmd.CombinedOutput()
	t.Logf("sdk harness output:\n%s", string(combined))
	if runErr != nil {
		t.Fatalf("node sdk_roundtrip.mjs failed: %v", runErr)
	}
}
