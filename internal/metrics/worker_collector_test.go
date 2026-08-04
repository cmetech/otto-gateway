package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"otto-gateway/internal/procstat"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// scrapeReg renders a registry to the Prometheus text exposition format via the
// same promhttp path a real scrape uses, so assertions match the on-the-wire
// `metric{label="v"} value` shape rather than protobuf internals.
func scrapeReg(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: want 200, got %d", rec.Code)
	}
	return rec.Body.String()
}

// TestWorkerCollector_EmitsPerSlotCPUAndRSS: a worker with a readable sample
// contributes both series, labelled by slot (not pid).
func TestWorkerCollector_EmitsPerSlotCPUAndRSS(t *testing.T) {
	procs := func() []WorkerProc {
		return []WorkerProc{
			{Slot: "slot-0", Pid: 111, UserRequestsSinceSpawn: 1, IdleSeconds: 901},
			{Slot: "slot-1", Pid: 222, UserRequestsSinceSpawn: 3, IdleSeconds: 0},
		}
	}
	read := func(pid int) procstat.Sample {
		switch pid {
		case 111:
			return procstat.Sample{CPUSeconds: 12.5, RSSBytes: 100 << 20, OK: true}
		case 222:
			return procstat.Sample{CPUSeconds: 3, RSSBytes: 50 << 20, OK: true}
		default:
			return procstat.Sample{}
		}
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(newWorkerCollector(procs, read))
	body := scrapeReg(t, reg)

	for _, want := range []string{
		`# HELP gw_worker_user_requests_since_spawn Successful user-path session/new calls served by each worker since spawn, by pool slot.`,
		`gw_worker_cpu_seconds_total{slot="slot-0"} 12.5`,
		`gw_worker_cpu_seconds_total{slot="slot-1"} 3`,
		`gw_worker_resident_memory_bytes{slot="slot-0"} 1.048576e+08`,
		`gw_worker_resident_memory_bytes{slot="slot-1"} 5.24288e+07`,
		`gw_worker_user_requests_since_spawn{slot="slot-0"} 1`,
		`gw_worker_idle_seconds{slot="slot-0"} 901`,
		`gw_worker_user_requests_since_spawn{slot="slot-1"} 3`,
		`gw_worker_idle_seconds{slot="slot-1"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n%s", want, body)
		}
	}
	// pid must never appear as a label value.
	if strings.Contains(body, `slot="111"`) || strings.Contains(body, `pid=`) {
		t.Errorf("pid leaked into a label\n%s", body)
	}
}

// TestWorkerCollector_ActivitySurvivesUnreadableProcess: CPU/RSS require an OS
// sample, while pool-projected activity gauges remain available even when the
// process reader cannot sample a worker.
func TestWorkerCollector_ActivitySurvivesUnreadableProcess(t *testing.T) {
	procs := func() []WorkerProc {
		return []WorkerProc{
			{Slot: "slot-0", Pid: 111, UserRequestsSinceSpawn: 4, IdleSeconds: 60},
			{Slot: "slot-dead", Pid: 999, UserRequestsSinceSpawn: 5, IdleSeconds: 120},
		}
	}
	read := func(pid int) procstat.Sample {
		if pid == 111 {
			return procstat.Sample{CPUSeconds: 1, RSSBytes: 1 << 20, OK: true}
		}
		return procstat.Sample{} // OK=false
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(newWorkerCollector(procs, read))
	body := scrapeReg(t, reg)

	if !strings.Contains(body, `slot="slot-0"`) {
		t.Errorf("readable worker missing\n%s", body)
	}
	for _, want := range []string{
		`gw_worker_user_requests_since_spawn{slot="slot-dead"} 5`,
		`gw_worker_idle_seconds{slot="slot-dead"} 120`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("unreadable worker activity missing %q\n%s", want, body)
		}
	}
	for _, absent := range []string{
		`gw_worker_cpu_seconds_total{slot="slot-dead"}`,
		`gw_worker_resident_memory_bytes{slot="slot-dead"}`,
	} {
		if strings.Contains(body, absent) {
			t.Errorf("unreadable worker OS sample unexpectedly emitted %q\n%s", absent, body)
		}
	}
}

// TestWorkerCollector_NilClosures: an inert collector (nil procs) is safe and
// emits nothing — matches the nil-workers path in New.
func TestWorkerCollector_NilClosures(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(newWorkerCollector(nil, nil))
	body := scrapeReg(t, reg)
	if strings.Contains(body, "gw_worker_") {
		t.Errorf("nil collector emitted series\n%s", body)
	}
}
