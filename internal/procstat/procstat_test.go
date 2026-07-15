package procstat

import (
	"os"
	"runtime"
	"testing"
)

// TestRead_InvalidPID: a non-positive pid is never readable on any platform.
func TestRead_InvalidPID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		if s := Read(pid); s.OK {
			t.Errorf("Read(%d).OK = true; want false", pid)
		}
	}
}

// TestSelf_PlatformContract: on the supported platforms (linux, windows) the
// gateway can read its own process and reports sane values; elsewhere (darwin
// dev box) the read is cleanly unavailable. Either way, when OK is false the
// numeric fields must be zero so a consumer never shows a fabricated reading.
func TestSelf_PlatformContract(t *testing.T) {
	s := Self()
	supported := runtime.GOOS == "linux" || runtime.GOOS == "windows"

	if supported {
		if !s.OK {
			t.Fatalf("Self().OK = false on %s; want true", runtime.GOOS)
		}
		if s.RSSBytes == 0 {
			t.Error("Self().RSSBytes = 0; want a live process to have resident memory")
		}
		if s.CPUSeconds < 0 {
			t.Errorf("Self().CPUSeconds = %v; want >= 0", s.CPUSeconds)
		}
	} else {
		if s.OK {
			t.Errorf("Self().OK = true on unsupported %s; want false", runtime.GOOS)
		}
		if s.RSSBytes != 0 || s.CPUSeconds != 0 {
			t.Errorf("Self() on !OK platform has nonzero fields: %+v", s)
		}
	}
}

// TestRead_SelfMatchesGetpid: Read(os.Getpid()) and Self() agree on OK.
func TestRead_SelfMatchesGetpid(t *testing.T) {
	if Read(os.Getpid()).OK != Self().OK {
		t.Error("Read(os.Getpid()).OK != Self().OK")
	}
}
