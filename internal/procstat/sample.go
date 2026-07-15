// Package procstat reads a single process's CPU and resident-memory usage by
// PID, cgo-free, so the gateway can surface its own and each kiro-cli worker's
// resource footprint without pulling in a cgo dependency (which would break the
// single-static-binary cross-compile guarantee — go_port_brief.md §3.4).
//
// Platform support follows the deploy targets: Linux and Windows are fully
// supported (via /proc and the Win32 psapi/kernel32 APIs respectively); every
// other GOOS — notably darwin, the dev/build box — returns an unpopulated
// Sample (OK=false), because a cgo-free per-process RSS read there requires
// mach task_info. Callers treat !OK as "unavailable on this platform" and
// render nothing rather than a wrong zero.
package procstat

import "os"

// Sample is a point-in-time reading of one process's resource usage.
//
// CPUSeconds is cumulative CPU time (user + system) in seconds since the
// process started — a monotonically increasing counter, so a live CPU percent
// is derived by the consumer as Δcpu / Δwall. RSSBytes is the instantaneous
// resident set size (Linux) / working set (Windows) in bytes.
//
// OK is false when the platform is unsupported or the PID could not be read
// (exited, permission denied). When OK is false the numeric fields are zero and
// must not be interpreted as a real measurement.
type Sample struct {
	CPUSeconds float64
	RSSBytes   uint64
	OK         bool
}

// Self reads the calling gateway process's own usage. On unsupported platforms
// it returns Sample{OK: false}, same as Read.
func Self() Sample {
	return Read(os.Getpid())
}
