//go:build darwin || windows

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/castai/promwrite"
)

const sampleMetrics = `# HELP gw_build_info Gateway build identity.
# TYPE gw_build_info gauge
gw_build_info{gateway_id="gw-abc",version="v1"} 1
# TYPE gw_worker_cpu_seconds_total counter
gw_worker_cpu_seconds_total{slot="slot-0"} 12.5
# TYPE gw_worker_resident_memory_bytes gauge
gw_worker_resident_memory_bytes{slot="slot-0"} 2.097152e+08
# TYPE gw_http_request_duration_seconds histogram
gw_http_request_duration_seconds_bucket{le="0.1"} 1
gw_http_request_duration_seconds_bucket{le="0.5"} 3
gw_http_request_duration_seconds_bucket{le="+Inf"} 5
gw_http_request_duration_seconds_sum 2.5
gw_http_request_duration_seconds_count 5
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 8.8e+07
# TYPE go_goroutines gauge
go_goroutines 42
`

func metricsServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// seriesByName indexes converted series by their __name__ label for assertions.
func seriesByName(series []promwrite.TimeSeries) map[string][]promwrite.TimeSeries {
	out := map[string][]promwrite.TimeSeries{}
	for _, ts := range series {
		for _, l := range ts.Labels {
			if l.Name == "__name__" {
				out[l.Value] = append(out[l.Value], ts)
			}
		}
	}
	return out
}

func labelValue(ts promwrite.TimeSeries, name string) string {
	for _, l := range ts.Labels {
		if l.Name == name {
			return l.Value
		}
	}
	return ""
}

// TestScrapeAndConvert_FilterExpandLabel: default prefixes drop go_*, keep
// gw_*/process_*, histograms expand to _sum/_count/_bucket{le,+Inf}, and every
// series carries job + instance (instance = gateway_id from gw_build_info).
func TestScrapeAndConvert_FilterExpandLabel(t *testing.T) {
	srv := metricsServer(t, sampleMetrics, http.StatusOK)
	defer srv.Close()

	rw := newRemoteWriter(srv.URL, t.TempDir(), enabledFlag(true))
	cfg := remoteWriteConfig{Prefixes: defaultSeriesPrefixes}
	series, err := rw.scrapeAndConvert(context.Background(), cfg)
	if err != nil {
		t.Fatalf("scrapeAndConvert: %v", err)
	}
	byName := seriesByName(series)

	if _, ok := byName["go_goroutines"]; ok {
		t.Error("go_goroutines should be filtered out by the default allowlist")
	}
	cpu := byName["gw_worker_cpu_seconds_total"]
	if len(cpu) != 1 {
		t.Fatalf("gw_worker_cpu_seconds_total: want 1 series, got %d", len(cpu))
	}
	if got := labelValue(cpu[0], "slot"); got != "slot-0" {
		t.Errorf("slot label = %q; want slot-0", got)
	}
	if got := labelValue(cpu[0], "job"); got != "otto-gateway" {
		t.Errorf("job label = %q; want otto-gateway", got)
	}
	if got := labelValue(cpu[0], "instance"); got != "gw-abc" {
		t.Errorf("instance label = %q; want gw-abc (from gw_build_info)", got)
	}
	if cpu[0].Sample.Value != 12.5 {
		t.Errorf("cpu value = %v; want 12.5", cpu[0].Sample.Value)
	}

	// Histogram expansion.
	if len(byName["gw_http_request_duration_seconds_sum"]) != 1 {
		t.Error("missing histogram _sum series")
	}
	if len(byName["gw_http_request_duration_seconds_count"]) != 1 {
		t.Error("missing histogram _count series")
	}
	buckets := byName["gw_http_request_duration_seconds_bucket"]
	var sawInf, sawFinite bool
	for _, b := range buckets {
		switch labelValue(b, "le") {
		case "+Inf":
			sawInf = true
		case "":
			t.Error("bucket series missing le label")
		default:
			sawFinite = true
		}
	}
	if !sawFinite || !sawInf {
		t.Errorf("histogram buckets incomplete: finite=%v inf=%v (n=%d)", sawFinite, sawInf, len(buckets))
	}
	// Exactly one +Inf bucket (no duplicate from the parser).
	infCount := 0
	for _, b := range buckets {
		if labelValue(b, "le") == "+Inf" {
			infCount++
		}
	}
	if infCount != 1 {
		t.Errorf("+Inf buckets = %d; want exactly 1", infCount)
	}
}

// TestScrapeAndConvert_GatewayDown: a dead scrape target is a plain error (the
// caller treats it as "skip this tick"), never a panic.
func TestScrapeAndConvert_GatewayDown(t *testing.T) {
	srv := metricsServer(t, "", http.StatusOK)
	url := srv.URL
	srv.Close() // now refuses connections

	rw := newRemoteWriter(url, t.TempDir(), enabledFlag(true))
	_, err := rw.scrapeAndConvert(context.Background(), remoteWriteConfig{Prefixes: defaultSeriesPrefixes})
	if err == nil {
		t.Fatal("want error when the gateway is down")
	}
}

// TestScrapeAndConvert_Non200: a non-200 scrape is an error, not a parse of an
// error page.
func TestScrapeAndConvert_Non200(t *testing.T) {
	srv := metricsServer(t, "nope", http.StatusServiceUnavailable)
	defer srv.Close()
	rw := newRemoteWriter(srv.URL, t.TempDir(), enabledFlag(true))
	if _, err := rw.scrapeAndConvert(context.Background(), remoteWriteConfig{}); err == nil {
		t.Fatal("want error on non-200 scrape")
	}
}

// TestTickOnce_EndToEnd_BasicAuth: enabled + configured drives a full
// scrape→push, and the fake Grafana endpoint sees basic auth + snappy encoding.
func TestTickOnce_EndToEnd_BasicAuth(t *testing.T) {
	gw := metricsServer(t, sampleMetrics, http.StatusOK)
	defer gw.Close()

	var gotAuth, gotEncoding string
	var pushes atomic.Int32
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pushes.Add(1)
		gotAuth = r.Header.Get("Authorization")
		gotEncoding = r.Header.Get("Content-Encoding")
		w.WriteHeader(http.StatusOK)
	}))
	defer grafana.Close()

	rw := newRemoteWriter(gw.URL, t.TempDir(), enabledFlag(true))
	cfg := remoteWriteConfig{
		URL:      grafana.URL,
		User:     "3370048",
		Token:    "glc_secrettoken",
		Prefixes: defaultSeriesPrefixes,
	}
	rw.tickOnce(context.Background(), cfg)

	if pushes.Load() != 1 {
		t.Fatalf("grafana pushes = %d; want 1", pushes.Load())
	}
	wantAuth := "Basic " + basicAuth("3370048", "glc_secrettoken")
	if gotAuth != wantAuth {
		t.Errorf("Authorization = %q; want %q", gotAuth, wantAuth)
	}
	if gotEncoding != "snappy" {
		t.Errorf("Content-Encoding = %q; want snappy", gotEncoding)
	}
}

// TestTickOnce_DisabledNoOp: with the toggle off, no scrape happens at all.
func TestTickOnce_DisabledNoOp(t *testing.T) {
	var scrapes atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		scrapes.Add(1)
		_, _ = w.Write([]byte(sampleMetrics))
	}))
	defer gw.Close()

	rw := newRemoteWriter(gw.URL, t.TempDir(), enabledFlag(false)) // disabled
	rw.tickOnce(context.Background(), remoteWriteConfig{URL: "x", User: "u", Token: "t"})
	if scrapes.Load() != 0 {
		t.Errorf("scrapes = %d; want 0 when disabled", scrapes.Load())
	}
}

// TestTickOnce_UnconfiguredNoOp: enabled but no endpoint/token ⇒ no scrape.
func TestTickOnce_UnconfiguredNoOp(t *testing.T) {
	var scrapes atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		scrapes.Add(1)
		_, _ = w.Write([]byte(sampleMetrics))
	}))
	defer gw.Close()
	rw := newRemoteWriter(gw.URL, t.TempDir(), enabledFlag(true))
	rw.tickOnce(context.Background(), remoteWriteConfig{}) // not ready
	if scrapes.Load() != 0 {
		t.Errorf("scrapes = %d; want 0 when unconfigured", scrapes.Load())
	}
}

// TestTickOnce_PushFailSwallowed: a 500 from Grafana must not propagate/panic.
func TestTickOnce_PushFailSwallowed(t *testing.T) {
	gw := metricsServer(t, sampleMetrics, http.StatusOK)
	defer gw.Close()
	grafana := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer grafana.Close()

	rw := newRemoteWriter(gw.URL, t.TempDir(), enabledFlag(true))
	cfg := remoteWriteConfig{URL: grafana.URL, User: "u", Token: "t", Prefixes: defaultSeriesPrefixes}
	// Must simply return (no panic, no fatal).
	rw.tickOnce(context.Background(), cfg)
}

// TestTickOnce_PanicRecovered: a nil HTTP client would panic inside Do; the
// recover() guard must contain it so the tray goroutine survives.
func TestTickOnce_PanicRecovered(t *testing.T) {
	rw := &remoteWriter{scrapeURL: "http://127.0.0.1:0/metrics", gwHome: t.TempDir(), enabled: enabledFlag(true), httpc: nil}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped tickOnce: %v", r)
		}
	}()
	rw.tickOnce(context.Background(), remoteWriteConfig{URL: "x", User: "u", Token: "t"})
}

// TestResolveMetricsRWEnabled_Precedence: tray.json override wins; nil falls
// back to the env default.
func TestResolveMetricsRWEnabled_Precedence(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("GW_METRICS_REMOTE_WRITE_ENABLED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := resolveMetricsRWEnabled(TrayConfig{}, home); !got {
		t.Error("nil override + env=true ⇒ want true")
	}
	if got := resolveMetricsRWEnabled(TrayConfig{MetricsRemoteWriteEnabled: boolPtr(false)}, home); got {
		t.Error("override=false must win over env=true")
	}
	if got := resolveMetricsRWEnabled(TrayConfig{MetricsRemoteWriteEnabled: boolPtr(true)}, t.TempDir()); !got {
		t.Error("override=true with no env ⇒ want true")
	}
}

// TestConfig_Parsers covers interval clamping, prefix defaults/wildcard, env
// bool, and the allowlist matcher.
func TestConfig_Parsers(t *testing.T) {
	if d := parseIntervalSeconds(""); d != defaultRemoteWriteInterval {
		t.Errorf("empty interval = %v; want default", d)
	}
	if d := parseIntervalSeconds("1"); d != minRemoteWriteInterval {
		t.Errorf("interval 1s should clamp to %v, got %v", minRemoteWriteInterval, d)
	}
	if d := parseIntervalSeconds("45"); d != 45*time.Second {
		t.Errorf("interval 45 = %v; want 45s", d)
	}
	if p := parseSeriesPrefixes(""); len(p) != 2 {
		t.Errorf("empty prefixes should default, got %v", p)
	}
	if p := parseSeriesPrefixes("*"); p != nil {
		t.Errorf(`"*" should disable filtering (nil), got %v`, p)
	}
	if !parseEnvBool("YES") || parseEnvBool("nope") {
		t.Error("parseEnvBool: YES→true, nope→false")
	}
	if !allowedSeriesName("gw_x", defaultSeriesPrefixes) || allowedSeriesName("go_x", defaultSeriesPrefixes) {
		t.Error("allowlist: gw_ in, go_ out")
	}
	if !allowedSeriesName("anything", nil) {
		t.Error("nil allowlist should pass everything")
	}
}

// TestScrubToken: the secret never survives into a log/error string.
func TestScrubToken(t *testing.T) {
	err := errors.New("push to https://x/api/prom/push failed token=glc_supersecret")
	got := scrubToken(err, "glc_supersecret")
	if got == err.Error() || strings.Contains(got, "glc_supersecret") {
		t.Errorf("token not scrubbed: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected redaction marker, got %q", got)
	}
}

func boolPtr(b bool) *bool { return &b }

// enabledFlag returns an *atomic.Bool preset to b for newRemoteWriter.
func enabledFlag(b bool) *atomic.Bool {
	var v atomic.Bool
	v.Store(b)
	return &v
}
