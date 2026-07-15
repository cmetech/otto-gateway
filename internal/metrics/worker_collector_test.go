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
		return []WorkerProc{{Slot: "slot-0", Pid: 111}, {Slot: "slot-1", Pid: 222}}
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
		`gw_worker_cpu_seconds_total{slot="slot-0"} 12.5`,
		`gw_worker_cpu_seconds_total{slot="slot-1"} 3`,
		`gw_worker_resident_memory_bytes{slot="slot-0"} 1.048576e+08`,
		`gw_worker_resident_memory_bytes{slot="slot-1"} 5.24288e+07`,
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

// TestWorkerCollector_SkipsUnreadable: a worker whose sample is !OK (dead,
// permission denied, or an unsupported platform) contributes no series.
func TestWorkerCollector_SkipsUnreadable(t *testing.T) {
	procs := func() []WorkerProc {
		return []WorkerProc{{Slot: "slot-0", Pid: 111}, {Slot: "slot-dead", Pid: 999}}
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
	if strings.Contains(body, `slot="slot-dead"`) {
		t.Errorf("unreadable worker should be skipped\n%s", body)
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
