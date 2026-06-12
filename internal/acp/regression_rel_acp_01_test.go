// Package acp — regression for REL-ACP-01 (D-19-03).
//
// Pre-fix: acp.Stream.Result() returned s.result directly as a pointer.
// close() closes s.done BEFORE acquiring s.mu, so a Result() caller can
// observe s.done closed, acquire s.mu, return the pointer, release s.mu —
// all BEFORE close() reaches the StopReason write at stream.go:182. The
// caller's downstream fr.StopReason read then races close()'s in-flight
// write. `go test -race` reports the data race.
//
// Post-fix: Result() copies *s.result into a stack-local under s.mu and
// returns &copy. close()'s later mutations target s.result; the caller
// dereferences a frozen snapshot.
//
// goleak.VerifyNone confirms no closer or Result-caller goroutine leaks.
//
// Phase 19 Plan 01 — REL-ACP-01.
package acp

import (
	"runtime"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"otto-gateway/internal/canonical"
)

// TestRegression_REL_ACP_01_ResultRacesCloseStopReason exercises the
// close-vs-Result race specifically: N Result-caller goroutines and one
// closer goroutine all gate on a `ready` channel for a deterministic
// happens-before edge, then race. Each iteration uses a fresh *Stream
// constructed via the exported NewStreamForTest helper. Each Result caller
// records the StopReason it observed; the positive assertion
// (StopReason == canonical.StopEndTurn) is checked at every iteration so
// the test fails on the racy `StopUnknown` observation that the pre-fix
// pointer-aliased return permits when the caller's deref happens before
// close()'s StopReason write completes.
//
// Per CONTEXT.md D-19-03: iterations ≥ 100 per `go test` invocation; the
// race-loop CI gate is `-count=60` (60 × 100 = 6,000 race trials).
func TestRegression_REL_ACP_01_ResultRacesCloseStopReason(t *testing.T) {
	defer goleak.VerifyNone(t)

	const iterations = 100
	const resultCallers = 8

	for i := 0; i < iterations; i++ {
		s := NewStreamForTest("sess-acp-01")

		var got [resultCallers]canonical.StopReason
		var wg sync.WaitGroup
		ready := make(chan struct{})

		for j := 0; j < resultCallers; j++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-ready
				fr, _ := s.Result()
				if fr != nil {
					// Pointer deref AFTER s.mu.Unlock — this is the racing
					// read on the pre-fix code path.
					got[idx] = fr.StopReason
				}
			}(j)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-ready
			// runtime.Gosched yields the closer so the Result callers have
			// time to park on <-s.done before close() runs. CONTEXT D-19-03
			// specifies "yield/Gosched scheduling".
			runtime.Gosched()
			s.CloseForTest(&FinalResult{StopReason: canonical.StopEndTurn}, nil)
		}()

		// Deterministic start gun + drain.
		close(ready)
		wg.Wait()

		// Positive assertion (CONTEXT D-19-03 criterion §2): every Result
		// caller MUST observe StopEndTurn. The pre-fix racy observation is
		// StopUnknown (zero value), made possible because close()'s
		// StopReason write at stream.go:182 had not yet landed when Result
		// returned its aliased pointer.
		for j := 0; j < resultCallers; j++ {
			if got[j] != canonical.StopEndTurn {
				t.Fatalf("iter %d caller %d: StopReason = %v, want StopEndTurn",
					i, j, got[j])
			}
		}
	}
}
