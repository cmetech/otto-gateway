//go:build !linux && !windows

package procstat

// Read is the fallback for platforms where a cgo-free per-process CPU/RSS read
// is not available — most importantly darwin (the dev/build box), where the
// working-set size lives behind mach task_info and cannot be obtained without
// cgo. It always reports Sample{OK: false}; consumers render the metric as
// unavailable. Keeping this a clean no-op preserves the vanilla cross-compile.
func Read(pid int) Sample {
	_ = pid
	return Sample{}
}
