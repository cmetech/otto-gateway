//go:build windows

package procstat

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// procGetProcessMemoryInfo is loaded from psapi.dll the same way the stock
// Prometheus process collector does — x/sys/windows exposes GetProcessTimes but
// not GetProcessMemoryInfo, so we bind it lazily ourselves.
var (
	modpsapi                 = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

// processMemoryCounters mirrors PROCESS_MEMORY_COUNTERS. Only WorkingSetSize is
// consumed (resident bytes); the remaining fields are declared so the struct
// size passed to GetProcessMemoryInfo is correct.
// https://learn.microsoft.com/windows/win32/api/psapi/ns-psapi-process_memory_counters
type processMemoryCounters struct {
	cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

// Read returns CPU (kernel+user) seconds and working-set bytes for pid via
// OpenProcess + GetProcessTimes + GetProcessMemoryInfo. It opens the target with
// the least-privileged PROCESS_QUERY_LIMITED_INFORMATION right (sufficient for
// both queries on the gateway's own child processes) and always closes the
// handle. Any failure yields Sample{OK: false}.
func Read(pid int) Sample {
	if pid <= 0 {
		return Sample{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Sample{}
	}
	defer windows.CloseHandle(h)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return Sample{}
	}

	mem := processMemoryCounters{}
	mem.cb = uint32(unsafe.Sizeof(mem))
	r1, _, _ := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&mem)),
		uintptr(mem.cb),
	)
	if r1 == 0 {
		return Sample{}
	}

	return Sample{
		CPUSeconds: fileTimeToSeconds(kernel) + fileTimeToSeconds(user),
		RSSBytes:   uint64(mem.WorkingSetSize),
		OK:         true,
	}
}

// fileTimeToSeconds converts a FILETIME (100-nanosecond ticks) to seconds.
func fileTimeToSeconds(ft windows.Filetime) float64 {
	return float64(uint64(ft.HighDateTime)<<32+uint64(ft.LowDateTime)) / 1e7
}
