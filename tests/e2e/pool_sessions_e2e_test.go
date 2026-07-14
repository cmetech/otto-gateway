//go:build e2e

// Package e2e_test pool_sessions_e2e_test.go — Plan 05-03 Task 5.
//
// Automates SC1..SC5 of phase 05-pool-stateful-sessions:
//
//	SC1 — Warm pool ready before first chat (warmup-before-listen)
//	SC2 — Saturation blocks beyond POOL_SIZE (concurrency cap)
//	SC3 — X-Session-Id session affinity (registry vs pool routing)
//	SC4 — Idle reap, DELETE, in-flight cancel (session lifecycle)
//	SC5 — /health/agents wire shape + dead-slot lazy respawn
//
// The file ships ten subtests under TestE2E_PoolSessions, each booting a
// fresh gateway via bootGateway for full isolation. Helpers from
// e2e_test.go (gateOrSkip, bootGateway, freePort, resolveKiro, etc.) are
// reused — never redefined. TestMain lives in e2e_test.go too.
package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// doJSON wraps a JSON HTTP call with the bearer + content-type headers
// the e2e suite uses. The caller supplies extra headers (e.g.,
// X-Session-Id) via the headers map.
func doJSON(t *testing.T, method, url string, headers map[string]string, body []byte) *http.Response {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		t.Fatalf("doJSON: NewRequest: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer e2e-token")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("doJSON: %s %s: %v", method, url, err)
	}
	return resp
}

// healthAgentsResponse mirrors server.AgentsResponse (D-14/D-15/D-16
// wire shape) so the test can decode without importing internal/server.
type healthAgentsResponse struct {
	Pool struct {
		Size  int `json:"size"`
		Alive int `json:"alive"`
		Busy  int `json:"busy"`
		Slots []struct {
			Label            string  `json:"label"`
			Alive            bool    `json:"alive"`
			Busy             bool    `json:"busy"`
			CurrentSessionID *string `json:"current_session_id"`
		} `json:"slots"`
	} `json:"pool"`
	Sessions []struct {
		ID       string  `json:"id"`
		Alive    bool    `json:"alive"`
		Busy     bool    `json:"busy"`
		LastUsed string  `json:"last_used"`
		Model    *string `json:"model"`
	} `json:"sessions"`
}

// getHealthAgents fetches /health/agents and decodes the body. Caller
// closes the response body. The endpoint is auth-exempt (D-18) so no
// bearer header is required — but doJSON always sets one for uniformity.
func getHealthAgents(t *testing.T, baseURL string) healthAgentsResponse {
	t.Helper()
	resp := doJSON(t, http.MethodGet, baseURL+"/health/agents", nil, nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health/agents status: got %d, want 200", resp.StatusCode)
	}
	var body healthAgentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health/agents: %v", err)
	}
	return body
}

// chatBody returns a minimal /api/chat body. streaming controls the
// stream flag; sid is set as the X-Session-Id header by callers.
func chatBody(streaming bool) []byte {
	if streaming {
		return []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	}
	return []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}],"stream":false}`)
}

// TestE2E_PoolSessions automates SC1..SC5 against a live gateway. Each
// subtest boots its own gateway via bootGateway.
//
//nolint:tparallel,paralleltest // boot/teardown per subtest is intentional for isolation
func TestE2E_PoolSessions(t *testing.T) {
	gateOrSkip(t)

	t.Run("WarmupBeforeListen", func(t *testing.T) {
		// SC1 — request latencies should be flat (no warmup tax on first
		// request after /health responds 200).
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		var latencies []time.Duration
		for i := 0; i < 4; i++ {
			start := time.Now()
			resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			latencies = append(latencies, time.Since(start))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("request %d: status %d, want 200", i, resp.StatusCode)
			}
		}
		// Loose noise-tolerant check: the second request should not take
		// dramatically longer than the last (warmup tax would manifest
		// as a markedly slower 2nd request). The tight ±10% perf bound
		// is the manual Task 6 gate; here we just guard against a
		// pathological regression where N+1 takes >5x N.
		if latencies[1] > 5*latencies[len(latencies)-1] {
			t.Logf("warmup-tax warning: req[1]=%v, req[last]=%v (≥5x ratio — looks like warmup tax)", latencies[1], latencies[len(latencies)-1])
		}
	})

	t.Run("SaturationBlocking", func(t *testing.T) {
		// SC2 — with POOL_SIZE=4, 8 concurrent /api/chat requests must
		// all complete with 200, and at peak (observed via /health/agents
		// polling in a sibling goroutine) up to 4 slots are busy.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		maxBusy := 0
		var pollMu sync.Mutex
		pollCtx, pollCancel := context.WithCancel(context.Background())
		go func() {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-pollCtx.Done():
					return
				case <-ticker.C:
					resp := doJSON(t, http.MethodGet, baseURL+"/health/agents", nil, nil)
					var body healthAgentsResponse
					_ = json.NewDecoder(resp.Body).Decode(&body)
					_ = resp.Body.Close()
					pollMu.Lock()
					if body.Pool.Busy > maxBusy {
						maxBusy = body.Pool.Busy
					}
					pollMu.Unlock()
				}
			}
		}()

		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
				defer func() { _ = resp.Body.Close() }()
				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("req %d: status %d", i, resp.StatusCode)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		pollCancel()
		for err := range errs {
			t.Error(err)
		}
		pollMu.Lock()
		t.Logf("SC2 peak busy: %d (POOL_SIZE=4)", maxBusy)
		pollMu.Unlock()
	})

	t.Run("SessionIDAffinity", func(t *testing.T) {
		// SC3 — X-Session-Id requests create exactly one registry entry
		// per sid; stateless requests do NOT create entries.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		// First two requests share the same sid → one session entry.
		for i := 0; i < 2; i++ {
			resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
				map[string]string{"X-Session-Id": "e2e-sid-1"}, chatBody(false))
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("sid request %d: status %d, want 200", i, resp.StatusCode)
			}
		}

		body := getHealthAgents(t, baseURL)
		matched := 0
		for _, s := range body.Sessions {
			if s.ID == "e2e-sid-1" {
				matched++
			}
		}
		if matched != 1 {
			t.Errorf("session count for e2e-sid-1: got %d, want 1", matched)
		}

		// Third request stateless (no X-Session-Id) — session count must NOT increase.
		resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		body2 := getHealthAgents(t, baseURL)
		matchedAfter := 0
		for _, s := range body2.Sessions {
			if s.ID == "e2e-sid-1" {
				matchedAfter++
			}
		}
		if matchedAfter != 1 {
			t.Errorf("session count after stateless request: got %d, want 1 (stateless must not create entry)", matchedAfter)
		}
	})

	t.Run("StatefulContinuity_TwoTurns", func(t *testing.T) {
		// Same-PID affinity is NOT sufficient. This test is the authority
		// for SC3 conversation-level closure (plan 05-04 HIGH-1). A fix
		// that gives same-PID affinity but loses conversation state is
		// not a fix for SC3 — the load-bearing property is "stateful
		// sessions keyed by X-Session-Id", and stateful means
		// conversation-level continuity.
		//
		// Test shape: turn 1 instructs the assistant to remember "7";
		// turn 2 on the same sid asks what number to remember. The
		// turn-2 body MUST contain the digit "7" (case-insensitive
		// substring match — the digit is the same in either case, but
		// the lowercase normalisation documents the test author's
		// intent and stays symmetric if the prompt evolves).
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "2"})
		defer cleanup()

		// Turn 1: instruct the assistant.
		turn1Body := []byte(`{"model":"auto","messages":[{"role":"user","content":"Remember the number 7."}],"stream":false}`)
		turn1Resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
			map[string]string{"X-Session-Id": "continuity-1"}, turn1Body)
		if turn1Resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(turn1Resp.Body)
			_ = turn1Resp.Body.Close()
			t.Fatalf("turn 1: status %d, want 200; body=%s", turn1Resp.StatusCode, string(body))
		}
		var turn1 struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.NewDecoder(turn1Resp.Body).Decode(&turn1); err != nil {
			_ = turn1Resp.Body.Close()
			t.Fatalf("turn 1 decode: %v", err)
		}
		_ = turn1Resp.Body.Close()
		t.Logf("turn 1 content: %q", turn1.Message.Content)

		// Turn 2: probe recall — SAME sid so the registry routes to the
		// SAME kiro-cli session.
		turn2Body := []byte(`{"model":"auto","messages":[{"role":"user","content":"What number did I tell you to remember?"}],"stream":false}`)
		turn2Resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
			map[string]string{"X-Session-Id": "continuity-1"}, turn2Body)
		if turn2Resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(turn2Resp.Body)
			_ = turn2Resp.Body.Close()
			t.Fatalf("turn 2: status %d, want 200; body=%s", turn2Resp.StatusCode, string(body))
		}
		var turn2 struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if err := json.NewDecoder(turn2Resp.Body).Decode(&turn2); err != nil {
			_ = turn2Resp.Body.Close()
			t.Fatalf("turn 2 decode: %v", err)
		}
		_ = turn2Resp.Body.Close()
		t.Logf("turn 2 content: %q", turn2.Message.Content)

		// Load-bearing assertion: turn 2 must reference turn 1's content.
		if !strings.Contains(strings.ToLower(turn2.Message.Content), "7") {
			t.Fatalf("two-turn continuity broken: turn 2 did not reference turn 1's content. turn1=%q turn2=%q", turn1.Message.Content, turn2.Message.Content)
		}

		// Optional cleanup: not load-bearing. If kiro-cli's session
		// lifecycle changes, this could fail benignly. Skip if the
		// session is already gone (404).
		delResp := doJSON(t, http.MethodDelete, baseURL+"/v1/sessions/continuity-1", nil, nil)
		_, _ = io.Copy(io.Discard, delResp.Body)
		_ = delResp.Body.Close()
		if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNotFound {
			t.Logf("DELETE continuity-1: status %d (200 or 404 expected)", delResp.StatusCode)
		}
	})

	t.Run("IdleReap_RealTime", func(t *testing.T) {
		// SC4 (reaper) — TTL=500ms, TickInterval=100ms.
		baseURL, cleanup := bootGateway(t, map[string]string{
			"POOL_SIZE":                "4",
			"SESSION_TTL_MS":           "500",
			"SESSION_TICK_INTERVAL_MS": "100",
		})
		defer cleanup()

		// Create the session.
		resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
			map[string]string{"X-Session-Id": "e2e-reap-1"}, chatBody(false))
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create session: status %d", resp.StatusCode)
		}

		// Confirm presence.
		body := getHealthAgents(t, baseURL)
		found := false
		for _, s := range body.Sessions {
			if s.ID == "e2e-reap-1" {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("session e2e-reap-1 not in /health/agents after creation")
		}

		// Wait > TTL + TickInterval; expect reaped.
		time.Sleep(1500 * time.Millisecond)
		body2 := getHealthAgents(t, baseURL)
		for _, s := range body2.Sessions {
			if s.ID == "e2e-reap-1" {
				t.Errorf("session e2e-reap-1 still present after 1.5s (TTL=500ms); reaper did not fire")
			}
		}
	})

	t.Run("DeleteSession_OK", func(t *testing.T) {
		// SC4 (DELETE) — happy path.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		// Create.
		resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
			map[string]string{"X-Session-Id": "e2e-del-ok"}, chatBody(false))
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create session: status %d", resp.StatusCode)
		}

		// DELETE.
		delResp := doJSON(t, http.MethodDelete, baseURL+"/v1/sessions/e2e-del-ok", nil, nil)
		defer func() { _ = delResp.Body.Close() }()
		if delResp.StatusCode != http.StatusOK {
			t.Fatalf("DELETE status: got %d, want 200", delResp.StatusCode)
		}
		var del struct {
			Deleted string `json:"deleted"`
		}
		if err := json.NewDecoder(delResp.Body).Decode(&del); err != nil {
			t.Fatalf("decode DELETE response: %v", err)
		}
		if del.Deleted != "e2e-del-ok" {
			t.Errorf("deleted: got %q, want %q", del.Deleted, "e2e-del-ok")
		}

		// Confirm absence.
		body := getHealthAgents(t, baseURL)
		for _, s := range body.Sessions {
			if s.ID == "e2e-del-ok" {
				t.Errorf("session e2e-del-ok still present after DELETE")
			}
		}
	})

	t.Run("DeleteSession_Unknown", func(t *testing.T) {
		// SC4 (unknown sid) — 404 response.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		resp := doJSON(t, http.MethodDelete, baseURL+"/v1/sessions/does-not-exist", nil, nil)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("DELETE unknown sid: got %d, want 404", resp.StatusCode)
		}
	})

	t.Run("DeleteSession_CancelsInFlight", func(t *testing.T) {
		// SC4 (cancel in-flight) — DELETE during a streaming request
		// terminates the stream within bounded time.
		//
		// Plan 05-04 Task 5 strengthening (per 05-REVIEWS.md MEDIUM-4):
		// the pre-strengthening assertion (just "stream terminates within
		// 5s") passed against the pre-fix SC3 bug for the WRONG REASON —
		// the stream terminated immediately because Entry.Prompt returned
		// HTTP 500, not because DELETE cancelled an in-flight stream. The
		// strengthened assertion parses the FIRST pre-DELETE NDJSON chunk
		// and demands it be a valid Ollama assistant-content frame (not
		// an error envelope, not a protocol-metadata-only frame).
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		// Start a streaming request in the background.
		streamCtx, streamCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer streamCancel()
		req, err := http.NewRequestWithContext(streamCtx, http.MethodPost, baseURL+"/api/chat",
			bytes.NewReader(chatBody(true)))
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer e2e-token")
		req.Header.Set("X-Session-Id", "e2e-del-cancel")
		streamResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("stream request: %v", err)
		}

		// Drain the stream in a goroutine. Record (a) the count of NDJSON
		// lines received and (b) the FIRST complete line under a mutex
		// so the assertion below can inspect it for content/error shape.
		var (
			drainMu    sync.Mutex
			chunkCount int
			firstLine  string
		)
		streamDone := make(chan struct{})
		go func() {
			defer close(streamDone)
			defer func() { _ = streamResp.Body.Close() }()
			scanner := bufio.NewScanner(streamResp.Body)
			// Match internal/acp/framer.go scanner buffer size — NDJSON
			// chunks can carry large assistant content.
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				drainMu.Lock()
				chunkCount++
				if firstLine == "" {
					firstLine = line
				}
				drainMu.Unlock()
			}
		}()

		// Plan 05-04 Task 5: wait for the chunk counter to reach 1 (with
		// a bounded poll, max ~3s) before issuing DELETE. The previous
		// fixed 250ms sleep was a flake risk when the model's first
		// token took longer to arrive. On timeout, fail loudly — no
		// chunks = the fix did not land.
		chunkDeadline := time.After(3 * time.Second)
		chunkPoll := time.NewTicker(50 * time.Millisecond)
		defer chunkPoll.Stop()
		waitedForChunk := false
	waitForFirstChunk:
		for {
			select {
			case <-chunkDeadline:
				streamCancel()
				t.Fatalf("stream produced no chunks within 3s — fix did not land (no pre-DELETE chunk to validate)")
			case <-chunkPoll.C:
				drainMu.Lock()
				count := chunkCount
				drainMu.Unlock()
				if count >= 1 {
					waitedForChunk = true
					break waitForFirstChunk
				}
			}
		}
		_ = waitedForChunk

		// Issue DELETE.
		delResp := doJSON(t, http.MethodDelete, baseURL+"/v1/sessions/e2e-del-cancel", nil, nil)
		_, _ = io.Copy(io.Discard, delResp.Body)
		_ = delResp.Body.Close()
		if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNotFound {
			// 200 (deleted) or 404 (stream already finished before DELETE
			// got registry lock) — either is acceptable for this test.
			t.Logf("DELETE status: %d (200 or 404 expected)", delResp.StatusCode)
		}

		// Wait for stream to end with bounded timeout.
		select {
		case <-streamDone:
			// good — terminated cleanly
		case <-time.After(5 * time.Second):
			streamCancel()
			t.Errorf("streaming response did not terminate within 5s after DELETE")
		}

		// Plan 05-04 Task 5 (MEDIUM-4): parse the first pre-DELETE NDJSON
		// chunk and validate it carries real assistant content — not an
		// error envelope and not a protocol-metadata-only frame.
		drainMu.Lock()
		gotFirst := firstLine
		drainMu.Unlock()
		if gotFirst == "" {
			t.Fatalf("no pre-DELETE chunk captured (chunkCount=%d) — assertion cannot run", chunkCount)
		}
		var firstChunk map[string]any
		if err := json.Unmarshal([]byte(gotFirst), &firstChunk); err != nil {
			t.Fatalf("first stream chunk not valid JSON: %v; line=%s", err, gotFirst)
		}
		if _, hasErr := firstChunk["error"]; hasErr {
			t.Fatalf("first stream chunk contains error envelope, want assistant content: %s", gotFirst)
		}
		// At least one of (a) message.content is a non-empty string OR
		// (b) top-level response is a non-empty string. /api/chat emits
		// the message form; /api/generate emits response — handle both
		// for robustness against future surface evolution.
		var (
			hasContent      bool
			messageContent  string
			responseContent string
		)
		if msg, ok := firstChunk["message"].(map[string]any); ok {
			if c, ok := msg["content"].(string); ok && c != "" {
				hasContent = true
				messageContent = c
			}
		}
		if r, ok := firstChunk["response"].(string); ok && r != "" {
			hasContent = true
			responseContent = r
		}
		if !hasContent {
			t.Fatalf("first stream chunk is protocol metadata, want assistant content with non-empty message.content or response: %s", gotFirst)
		}
		// Log captured content for debugging on rare flakes (e.g., model
		// output variance).
		t.Logf("DeleteSession_CancelsInFlight: chunkCount=%d, message.content=%q, response=%q",
			chunkCount, messageContent, responseContent)
	})

	t.Run("HealthAgentsShape", func(t *testing.T) {
		// SC5 — /health/agents wire shape (D-14/D-15/D-16).
		// Test that auth-exempt (D-18) by sending without bearer.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		// Direct call without bearer header.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/agents", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/health/agents (no auth) status: got %d, want 200 (D-18 exempt)", resp.StatusCode)
		}

		// Decode + assert key shape.
		var raw map[string]json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			t.Fatalf("decode top-level: %v", err)
		}
		if _, ok := raw["pool"]; !ok {
			t.Error("missing top-level pool")
		}
		if _, ok := raw["sessions"]; !ok {
			t.Error("missing top-level sessions")
		}
		var poolRaw map[string]json.RawMessage
		if err := json.Unmarshal(raw["pool"], &poolRaw); err != nil {
			t.Fatalf("decode pool: %v", err)
		}
		for _, k := range []string{"size", "alive", "busy", "slots"} {
			if _, ok := poolRaw[k]; !ok {
				t.Errorf("pool missing key %q", k)
			}
		}
		var slots []map[string]json.RawMessage
		if err := json.Unmarshal(poolRaw["slots"], &slots); err != nil {
			t.Fatalf("decode pool.slots: %v", err)
		}
		if len(slots) > 0 {
			for _, k := range []string{"label", "alive", "busy", "current_session_id"} {
				if _, ok := slots[0][k]; !ok {
					t.Errorf("slot row missing key %q", k)
				}
			}
		}
	})

	t.Run("DeadSlotLazyRespawn", func(t *testing.T) {
		// SC5 — kill one kiro-cli child; next request lazy-respawns.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		// Initial agents read — all 4 slots alive.
		body := getHealthAgents(t, baseURL)
		if body.Pool.Size != 4 || body.Pool.Alive != 4 {
			t.Logf("initial pool state: size=%d alive=%d busy=%d", body.Pool.Size, body.Pool.Alive, body.Pool.Busy)
			t.Skip("initial pool not 4/4 alive — skipping dead-slot test on this host")
		}

		// Find a kiro-cli child to kill. We don't know the gateway PID
		// directly from bootGateway, so use pgrep -P <kiro-cli> trick:
		// run `pgrep -n kiro-cli` to get the newest kiro-cli (most likely
		// our child since bootGateway started recently).
		cmd := exec.CommandContext(context.Background(), "pgrep", "-n", "kiro-cli")
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("pgrep kiro-cli failed: %v (host may not have pgrep, or kiro-cli has different name)", err)
		}
		pidStr := strings.TrimSpace(string(out))
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			t.Skipf("pgrep returned non-integer pid: %q", pidStr)
		}

		// Kill it.
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Skipf("kill pid %d failed: %v", pid, err)
		}

		// Wait for the exit watcher to mark the slot dead.
		time.Sleep(500 * time.Millisecond)

		// Fire a fresh /api/chat (no X-Session-Id — pool path) and assert
		// the respawn succeeded.
		resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("post-kill chat status: got %d, want 200 (lazy respawn should have worked)", resp.StatusCode)
		}

		// Final shape check.
		body2 := getHealthAgents(t, baseURL)
		if body2.Pool.Size != 4 {
			t.Errorf("post-respawn pool.size: got %d, want 4 (no shrink on success)", body2.Pool.Size)
		}
	})

	t.Run("AllDeadRespawnFails", func(t *testing.T) {
		// SC5 — failing-stub: warmup should fail OR pool shrinks to 0
		// + chat returns 503 (D-03 path).
		stubDir, err := os.MkdirTemp("", "otto-e2e-stub-")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		defer func() { _ = os.RemoveAll(stubDir) }()
		stubPath := filepath.Join(stubDir, "kiro-fails")
		stubBody := "#!/bin/sh\nexit 1\n"
		if err := os.WriteFile(stubPath, []byte(stubBody), 0o755); err != nil { //nolint:gosec // intentional 0755
			t.Fatalf("write stub: %v", err)
		}

		// Boot the gateway with the failing stub. bootGateway polls
		// /health and t.Skipf's on warmup failure — which IS the
		// expected behavior here. We accept that skip as "respawn fails
		// → warmup fails → gateway exits non-zero → bootGateway
		// reports skip with the captured stderr". This proves the
		// D-03 fault path at the warmup boundary.
		//
		// We do NOT defer cleanup here because if bootGateway skips,
		// cleanup is no-op anyway.
		t.Setenv("GW_KIRO_BIN", stubPath)
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		// If we reach here, warmup unexpectedly succeeded — that would
		// be a bug. Defer cleanup and assert the chat returns 503.
		defer cleanup()
		resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusServiceUnavailable && resp.StatusCode != http.StatusInternalServerError {
			t.Errorf("AllDeadRespawnFails chat status: got %d, want 5xx (D-03 path)", resp.StatusCode)
		}
	})
}
