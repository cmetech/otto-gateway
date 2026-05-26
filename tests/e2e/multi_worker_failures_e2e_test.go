//go:build e2e

// Package e2e_test multi_worker_failures_e2e_test.go — Plan 05-05 functional-coverage pivot.
//
// Validates failure modes that uniquely emerge under multi-worker pool
// (POOL_SIZE > 1). These tests stand in for the deferred perf+RSS gate:
// per project owner direction (2026-05-26), the v1.5 closure criterion is
// functional reliability across multiple workers, not throughput parity
// with the Node reference.
//
// Subtests under TestE2E_MultiWorker_FailureModes:
//
//   MultiSession_ConcurrentAffinity — N distinct sids fired concurrently;
//     each request must return 200, and the registry must show exactly N
//     entries (no state bleed, no session loss).
//
//   Pool_Session_Coexistence_UnderLoad — concurrent stateless pool traffic
//     and stateful session traffic; both must complete without one starving
//     the other.
//
//   ConcurrentSameSid_OneSession — N concurrent requests for the same NEW
//     sid; exactly ONE registry entry must result (Plan 05-02 Pitfall-4
//     race resolution verified at e2e level).
//
//   MultipleDeadSlotsParallel — kill two kiro-cli children in parallel;
//     subsequent requests must succeed via lazy respawn without cascade.
//
//   Reaper_DoesNotReapActiveSession — hold a session active across a
//     reaper tick; the session must NOT be reaped (per-entry TryLock
//     skip-in-flight discipline verified live).
//
// Helpers (gateOrSkip, bootGateway, doJSON, getHealthAgents, chatBody)
// are reused from e2e_test.go and pool_sessions_e2e_test.go.
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

//nolint:tparallel,paralleltest // boot/teardown per subtest is intentional for isolation
func TestE2E_MultiWorker_FailureModes(t *testing.T) {
	gateOrSkip(t)

	t.Run("MultiSession_ConcurrentAffinity", func(t *testing.T) {
		// Four distinct sids fired concurrently against POOL_SIZE=4.
		// Each request must return 200; the registry must end with
		// exactly 4 entries; turn-2 for each sid (sequential after the
		// concurrent burst) must reference its own turn-1 content, not
		// some other sid's. Confirms stateful isolation under load.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		sids := []string{"iso-A", "iso-B", "iso-C", "iso-D"}
		secrets := []string{"3", "7", "11", "19"}

		// Phase 1: concurrent turn-1 — each sid instructs the assistant
		// to remember a distinct number.
		var wg sync.WaitGroup
		errs := make(chan error, len(sids))
		for i, sid := range sids {
			wg.Add(1)
			go func(i int, sid string) {
				defer wg.Done()
				body := fmt.Sprintf(`{"model":"auto","messages":[{"role":"user","content":"Remember the number %s."}],"stream":false}`, secrets[i])
				resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
					map[string]string{"X-Session-Id": sid}, []byte(body))
				defer func() { _ = resp.Body.Close() }()
				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("turn-1 sid=%s status=%d", sid, resp.StatusCode)
				}
			}(i, sid)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}

		// Registry must show exactly len(sids) entries with our sids.
		body := getHealthAgents(t, baseURL)
		seen := map[string]bool{}
		for _, s := range body.Sessions {
			seen[s.ID] = true
		}
		for _, sid := range sids {
			if !seen[sid] {
				t.Errorf("session %q missing from registry after concurrent turn-1", sid)
			}
		}
		// No extras (besides our 4).
		extra := 0
		for id := range seen {
			isOurs := false
			for _, sid := range sids {
				if id == sid {
					isOurs = true
					break
				}
			}
			if !isOurs {
				extra++
				t.Logf("unexpected session in registry: %q", id)
			}
		}
		if extra > 0 {
			t.Errorf("registry has %d extra unexpected sessions", extra)
		}

		// Phase 2: sequential turn-2 per sid — each must recall its own
		// number, not another sid's. Sequential (not concurrent) keeps
		// the assertion deterministic and tightly attributable.
		for i, sid := range sids {
			turn2 := []byte(`{"model":"auto","messages":[{"role":"user","content":"What number did I tell you to remember?"}],"stream":false}`)
			resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
				map[string]string{"X-Session-Id": sid}, turn2)
			if resp.StatusCode != http.StatusOK {
				_ = resp.Body.Close()
				t.Fatalf("turn-2 sid=%s status=%d", sid, resp.StatusCode)
			}
			var got struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				_ = resp.Body.Close()
				t.Fatalf("turn-2 sid=%s decode: %v", sid, err)
			}
			_ = resp.Body.Close()
			want := secrets[i]
			content := strings.ToLower(got.Message.Content)
			if !strings.Contains(content, want) {
				t.Errorf("isolation failure: sid=%s expected recall of %q, got %q", sid, want, got.Message.Content)
			}
			// Cross-check: must NOT contain any other sid's secret.
			for j, other := range secrets {
				if j == i {
					continue
				}
				if strings.Contains(content, other) {
					t.Errorf("state bleed: sid=%s recalled %q which belongs to sid=%s", sid, other, sids[j])
				}
			}
		}
	})

	t.Run("Pool_Session_Coexistence_UnderLoad", func(t *testing.T) {
		// POOL_SIZE=4 + 2 stateful sids; fire 6 concurrent stateless
		// pool requests AND 4 stateful requests across the 2 sids
		// simultaneously. Both paths must complete with 200; no
		// path may starve the other.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		const statelessReqs = 6
		const statefulReqsPerSid = 2
		sids := []string{"coex-1", "coex-2"}
		totalRequests := statelessReqs + len(sids)*statefulReqsPerSid

		var wg sync.WaitGroup
		errs := make(chan error, totalRequests)

		// Stateless burst.
		for i := 0; i < statelessReqs; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
				defer func() { _ = resp.Body.Close() }()
				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("stateless[%d] status=%d", i, resp.StatusCode)
				}
			}(i)
		}

		// Stateful burst per sid.
		for _, sid := range sids {
			for j := 0; j < statefulReqsPerSid; j++ {
				wg.Add(1)
				go func(sid string, j int) {
					defer wg.Done()
					resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
						map[string]string{"X-Session-Id": sid}, chatBody(false))
					defer func() { _ = resp.Body.Close() }()
					_, _ = io.Copy(io.Discard, resp.Body)
					if resp.StatusCode != http.StatusOK {
						errs <- fmt.Errorf("stateful sid=%s[%d] status=%d", sid, j, resp.StatusCode)
					}
				}(sid, j)
			}
		}

		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}

		// Sanity: registry should show exactly the 2 stateful sids,
		// pool should still be size=4 alive.
		body := getHealthAgents(t, baseURL)
		if body.Pool.Size != 4 || body.Pool.Alive != 4 {
			t.Errorf("post-load pool: size=%d alive=%d, want 4/4", body.Pool.Size, body.Pool.Alive)
		}
		sidCount := 0
		for _, s := range body.Sessions {
			for _, want := range sids {
				if s.ID == want {
					sidCount++
				}
			}
		}
		if sidCount != len(sids) {
			t.Errorf("registry session count: got %d, want %d", sidCount, len(sids))
		}
	})

	t.Run("ConcurrentSameSid_OneSession", func(t *testing.T) {
		// Plan 05-02 Pitfall-4 race resolution verified at e2e level.
		// Fire N concurrent requests for the SAME new sid. After all
		// settle, the registry must show exactly ONE entry — even
		// though up to N goroutines tried to lazy-create simultaneously.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		const n = 6
		sid := "race-once"

		var wg sync.WaitGroup
		errs := make(chan error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
					map[string]string{"X-Session-Id": sid}, chatBody(false))
				defer func() { _ = resp.Body.Close() }()
				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("req[%d] status=%d", i, resp.StatusCode)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}

		body := getHealthAgents(t, baseURL)
		matched := 0
		for _, s := range body.Sessions {
			if s.ID == sid {
				matched++
			}
		}
		if matched != 1 {
			t.Errorf("concurrent same-sid: got %d registry entries, want 1 (race resolution failed)", matched)
		}
	})

	t.Run("MultipleDeadSlotsParallel", func(t *testing.T) {
		// Kill TWO kiro-cli children in parallel (different from the
		// single-kill DeadSlotLazyRespawn). The exit watcher must mark
		// both slots dead, and subsequent requests must lazily respawn
		// without cascade failure or pool shrink to 0.
		baseURL, cleanup := bootGateway(t, map[string]string{"POOL_SIZE": "4"})
		defer cleanup()

		body := getHealthAgents(t, baseURL)
		if body.Pool.Size != 4 || body.Pool.Alive != 4 {
			t.Skipf("initial pool not 4/4 alive (size=%d alive=%d) — skipping", body.Pool.Size, body.Pool.Alive)
		}

		// Collect kiro-cli pids (newest 2).
		cmd := exec.CommandContext(context.Background(), "pgrep", "-n", "kiro-cli")
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("pgrep kiro-cli failed: %v", err)
		}
		_ = out
		// Get a list of pids via pgrep (all matching), then take the 2
		// most recently spawned.
		listCmd := exec.CommandContext(context.Background(), "pgrep", "kiro-cli")
		listOut, err := listCmd.Output()
		if err != nil {
			t.Skipf("pgrep -list kiro-cli failed: %v", err)
		}
		var pids []int
		for _, line := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
			if pid, perr := strconv.Atoi(strings.TrimSpace(line)); perr == nil {
				pids = append(pids, pid)
			}
		}
		if len(pids) < 2 {
			t.Skipf("need ≥2 kiro-cli children to kill, got %d", len(pids))
		}
		// Take the 2 highest pids (most recently spawned — most likely
		// our gateway's children).
		victims := pids[len(pids)-2:]

		// Kill in parallel.
		var kwg sync.WaitGroup
		for _, pid := range victims {
			kwg.Add(1)
			go func(pid int) {
				defer kwg.Done()
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}(pid)
		}
		kwg.Wait()

		// Wait for exit watchers.
		time.Sleep(800 * time.Millisecond)

		// Saturate the pool with POOL_SIZE * 2 concurrent requests so
		// every slot (alive or dead) is acquired at least once — lazy
		// respawn only fires when a caller actually picks a dead slot.
		// Two requests would deterministically prefer the two alive
		// slots and leave the dead ones unrecovered.
		const saturate = 8
		var wg sync.WaitGroup
		errs := make(chan error, saturate)
		for i := 0; i < saturate; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				resp := doJSON(t, http.MethodPost, baseURL+"/api/chat", nil, chatBody(false))
				defer func() { _ = resp.Body.Close() }()
				_, _ = io.Copy(io.Discard, resp.Body)
				if resp.StatusCode != http.StatusOK {
					errs <- fmt.Errorf("post-kill req[%d] status=%d", i, resp.StatusCode)
				}
			}(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}

		// Pool must still be size=4 (no shrink on successful respawn).
		body2 := getHealthAgents(t, baseURL)
		if body2.Pool.Size != 4 {
			t.Errorf("post-respawn pool.size: got %d, want 4 (no shrink expected)", body2.Pool.Size)
		}
		// All 4 must be alive — every slot was exercised by the
		// saturating burst, so any dead slot would have respawned.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			b := getHealthAgents(t, baseURL)
			if b.Pool.Alive == 4 {
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		final := getHealthAgents(t, baseURL)
		t.Errorf("pool did not return to 4/4 alive within 5s after saturating burst: size=%d alive=%d busy=%d",
			final.Pool.Size, final.Pool.Alive, final.Pool.Busy)
	})

	t.Run("Reaper_DoesNotReapActiveSession", func(t *testing.T) {
		// TTL=400ms; fire repeated requests for the same sid across a
		// 1.5s window (longer than TTL). The session must NEVER be
		// reaped between requests — each request bumps last_used, and
		// the reaper's TryLock-skip-in-flight discipline must prevent
		// reap during the brief moments the entry is locked.
		//
		// Negative control: a DIFFERENT sid created at t=0 and never
		// touched again should be reaped before t=1.5s.
		baseURL, cleanup := bootGateway(t, map[string]string{
			"POOL_SIZE":                "4",
			"SESSION_TTL_MS":           "400",
			"SESSION_TICK_INTERVAL_MS": "100",
		})
		defer cleanup()

		const activeSid = "active-keep"
		const idleSid = "idle-drop"

		// Seed both.
		for _, sid := range []string{activeSid, idleSid} {
			resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
				map[string]string{"X-Session-Id": sid}, chatBody(false))
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("seed sid=%s status=%d", sid, resp.StatusCode)
			}
		}

		// Drive activeSid every ~200ms for ~1.6s (8 hits, well past TTL=400ms).
		hits := 8
		for i := 0; i < hits; i++ {
			resp := doJSON(t, http.MethodPost, baseURL+"/api/chat",
				map[string]string{"X-Session-Id": activeSid}, chatBody(false))
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("keepalive[%d] sid=%s status=%d", i, activeSid, resp.StatusCode)
			}
			time.Sleep(200 * time.Millisecond)
		}

		// Active sid must still be present.
		body := getHealthAgents(t, baseURL)
		activeFound, idleFound := false, false
		for _, s := range body.Sessions {
			if s.ID == activeSid {
				activeFound = true
			}
			if s.ID == idleSid {
				idleFound = true
			}
		}
		if !activeFound {
			t.Errorf("active session %q was reaped despite repeated use (TTL=400ms, hit interval=200ms)", activeSid)
		}
		// Idle sid (untouched since t=0, ~1.6s ago) must be gone.
		if idleFound {
			t.Errorf("idle session %q survived past TTL=400ms — reaper did not fire", idleSid)
		}
	})
}
