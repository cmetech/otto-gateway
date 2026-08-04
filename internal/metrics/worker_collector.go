package metrics

import (
	"otto-gateway/internal/procstat"

	"github.com/prometheus/client_golang/prometheus"
)

// WorkerProc is the metrics-package projection of a live pool worker: a stable
// slot label, OS pid, and pool-owned activity. The cmd wiring adapts
// pool.WorkerProc into this so this package never imports internal/pool (same
// boundary discipline as PoolStats). The slot label — bounded by POOL_SIZE — is
// the series key; the pid is only used to read /proc (or the Win32 API) and is
// never a label, because it churns on every respawn and would blow up TSDB
// cardinality.
type WorkerProc struct {
	Slot                   string
	Pid                    int
	UserRequestsSinceSpawn uint64
	IdleSeconds            float64
}

// workerCollector is a pull-style prometheus.Collector: at scrape time it lists
// the live workers and reads each one's CPU/RSS through procstat (cgo-free,
// per-OS). No background goroutine, no accumulated state — mirroring
// poolCollector. On platforms where procstat cannot read a process (darwin, or a
// pid that just exited), CPU/RSS are omitted for that worker while pool-projected
// request activity remains available.
type workerCollector struct {
	procs func() []WorkerProc
	read  func(pid int) procstat.Sample

	cpu *prometheus.Desc
	rss *prometheus.Desc

	userRequests *prometheus.Desc
	idleSeconds  *prometheus.Desc
}

func newWorkerCollector(procs func() []WorkerProc, read func(pid int) procstat.Sample) *workerCollector {
	return &workerCollector{
		procs: procs,
		read:  read,
		cpu: prometheus.NewDesc(
			"gw_worker_cpu_seconds_total",
			"Cumulative CPU time (user+system) of each kiro-cli worker subprocess, by pool slot.",
			[]string{"slot"}, nil,
		),
		rss: prometheus.NewDesc(
			"gw_worker_resident_memory_bytes",
			"Resident memory (RSS / working set) of each kiro-cli worker subprocess, by pool slot.",
			[]string{"slot"}, nil,
		),
		userRequests: prometheus.NewDesc(
			"gw_worker_user_requests_since_spawn",
			"Completed user requests served by each worker since spawn, by pool slot.",
			[]string{"slot"}, nil,
		),
		idleSeconds: prometheus.NewDesc(
			"gw_worker_idle_seconds",
			"Seconds since each worker completed its most recent user request, by pool slot.",
			[]string{"slot"}, nil,
		),
	}
}

func (c *workerCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.cpu
	ch <- c.rss
	ch <- c.userRequests
	ch <- c.idleSeconds
}

func (c *workerCollector) Collect(ch chan<- prometheus.Metric) {
	if c.procs == nil {
		return
	}
	for _, w := range c.procs() {
		ch <- prometheus.MustNewConstMetric(c.userRequests, prometheus.GaugeValue, float64(w.UserRequestsSinceSpawn), w.Slot)
		ch <- prometheus.MustNewConstMetric(c.idleSeconds, prometheus.GaugeValue, w.IdleSeconds, w.Slot)
		if c.read == nil {
			continue
		}
		s := c.read(w.Pid)
		if !s.OK {
			continue
		}
		ch <- prometheus.MustNewConstMetric(c.cpu, prometheus.CounterValue, s.CPUSeconds, w.Slot)
		ch <- prometheus.MustNewConstMetric(c.rss, prometheus.GaugeValue, float64(s.RSSBytes), w.Slot)
	}
}
