//go:build darwin || windows

package main

import (
	"testing"
)

// TestRegression_REL_TRAY_07_SupportBundleBounds demonstrates that the support
// bundle has no effective size cap on the current-day live log files and no
// overall execution timeout that cleans up staging on SIGKILL.
//
// Pre-fix observable (three failure modes):
//
//  1. Size cap exemption: scripts/otto-gw:1864-1873 copies otto-gateway.log and
//     chat-trace.log unconditionally (outside the --max-mb cap loop at :1957-1989
//     which only trims rotated .log.gz files). A 200MB log day blows past any cap.
//
//  2. Timeout + leak: runWrapper (runner.go:29-33) uses a 30s context.
//     On SIGKILL the bash EXIT trap never runs (trap only runs on normal exit),
//     leaving the staging dir in $TMPDIR and in-flight redact/tar children
//     running. cmd.Run() then blocks until those children finish.
//
//  3. PowerShell equivalent: otto-gw.ps1:1489-1494 and :1586-1596 have the same
//     exemption for live log copies before the cap enforcement block.
//
// Post-fix: --max-mb cap applies to ALL files (live logs included), overall
// timeout triggers staging cleanup, WaitDelay ensures cmd.Run() returns promptly.
//
// Note: The full reproducer requires invoking the real wrapper with a large fake
// log dir and measuring the resulting bundle size — this requires either darwin or
// Windows and a real wrapper binary. The test body documents the assertion shape
// and is left as a t.Skip stub for Phase 16 to activate.
func TestRegression_REL_TRAY_07_SupportBundleBounds(t *testing.T) {
	t.Skip("REL-TRAY-07 (T-7): regression test — unskip in Phase 16 fix commit")

	// Pre-fix reproducer outline (activated in Phase 16):
	//
	// 1. Build a fake log dir containing a large current-day log file:
	//      fakeLogDir := t.TempDir()
	//      logFile := filepath.Join(fakeLogDir, "otto-gateway.log")
	//      f, _ := os.Create(logFile)
	//      io.Copy(f, io.LimitReader(rand.Reader, 100<<20)) // 100MB
	//      f.Close()
	//
	// 2. Set OTTO_LOG env var to point at the fake log file:
	//      t.Setenv("OTTO_LOG", logFile)
	//
	// 3. Invoke the wrapper via wrapperCommand with the "support" verb and
	//    --max-mb 10 (cap at 10MB to make the assertion fast):
	//      installRoot := t.TempDir()
	//      res := runWrapper(installRoot, "support")
	//
	// 4. Find the resulting bundle path on stdout (last non-empty line):
	//      bundlePath := lastNonEmptyLine(res.Stdout)
	//
	// 5. Stat the bundle and assert its size EXCEEDS the cap (pre-fix observable):
	//      info, _ := os.Stat(bundlePath)
	//      if info.Size() <= 10*1024*1024 {
	//          t.Logf("post-fix: bundle %d bytes is within cap", info.Size())
	//      } else {
	//          t.Logf("pre-fix: bundle %d bytes exceeds --max-mb 10 cap (live log not trimmed)", info.Size())
	//      }
	//
	// The key assertion is that live-log copies are outside the cap loop and
	// that staging cleanup on timeout does not happen with the current EXIT
	// trap approach (traps don't run on SIGKILL).
}
