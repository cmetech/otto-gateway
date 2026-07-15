//go:build linux

package procstat

import "github.com/prometheus/procfs"

// Read returns the CPU + RSS usage for pid from /proc/<pid>/stat. It reuses the
// already-vendored prometheus/procfs so the userHZ→seconds and pages→bytes
// conversions match what the stock Prometheus process collector reports for the
// gateway's own process. Any read error (pid exited, /proc unavailable) yields
// Sample{OK: false} rather than a fabricated zero.
func Read(pid int) Sample {
	if pid <= 0 {
		return Sample{}
	}
	p, err := procfs.NewProc(pid)
	if err != nil {
		return Sample{}
	}
	st, err := p.Stat()
	if err != nil {
		return Sample{}
	}
	rss := st.ResidentMemory() // bytes
	if rss < 0 {
		rss = 0
	}
	return Sample{
		CPUSeconds: st.CPUTime(),
		RSSBytes:   uint64(rss),
		OK:         true,
	}
}
