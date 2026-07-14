package metrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"otto-gateway/internal/metrics"
)

func scrape(t *testing.T, m *metrics.Metrics) string {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape: want 200, got %d", rec.Code)
	}
	return rec.Body.String()
}

// TestMetrics_PoolAndSessionGauges: the pull-collector reports pool + session
// gauges from the injected sources at scrape time.
func TestMetrics_PoolAndSessionGauges(t *testing.T) {
	m := metrics.New(
		func() metrics.PoolStats {
			return metrics.PoolStats{Size: 4, Alive: 3, Busy: 1, Healthy: true, SpawnFailing: false}
		},
		func() int { return 2 },
	)
	body := scrape(t, m)
	for _, want := range []string{
		"gw_pool_size 4",
		"gw_pool_alive 3",
		"gw_pool_busy 1",
		"gw_pool_healthy 1",
		"gw_pool_spawn_failing 0",
		"gw_sessions_active 2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q\n---\n%s", want, body)
		}
	}
}

// TestMetrics_FreeRuntimeMetrics: promhttp gives Go-runtime + process metrics
// for free.
func TestMetrics_FreeRuntimeMetrics(t *testing.T) {
	m := metrics.New(func() metrics.PoolStats { return metrics.PoolStats{} }, func() int { return 0 })
	body := scrape(t, m)
	if !strings.Contains(body, "go_goroutines") {
		t.Errorf("scrape missing free go_goroutines metric\n%s", body)
	}
}

// TestMetrics_Middleware_RecordsRequestWithRoutePattern: the request middleware
// increments the request counter labeled by chi's RoutePattern (bounded
// cardinality), not the raw path.
func TestMetrics_Middleware_RecordsRequestWithRoutePattern(t *testing.T) {
	m := metrics.New(func() metrics.PoolStats { return metrics.PoolStats{} }, func() int { return 0 })

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/v1/messages", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/v1/messages", nil))

	body := scrape(t, m)
	// counter labeled with the route pattern + status, value 1
	if !strings.Contains(body, `gw_http_requests_total{method="GET",route="/v1/messages",status="200"} 1`) {
		t.Errorf("request counter not recorded with RoutePattern label\n%s", body)
	}
	if !strings.Contains(body, "gw_http_request_duration_seconds") {
		t.Errorf("duration histogram not present\n%s", body)
	}
}

// TestMetrics_Middleware_SkipsMetricsPath: scraping /metrics must not be
// counted as an application request (no self-measurement / scrape spam).
func TestMetrics_Middleware_SkipsMetricsPath(t *testing.T) {
	m := metrics.New(func() metrics.PoolStats { return metrics.PoolStats{} }, func() int { return 0 })

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Handle("/metrics", m.Handler())

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))

	body := scrape(t, m)
	if strings.Contains(body, `route="/metrics"`) {
		t.Errorf("/metrics scrape must not be counted as a request\n%s", body)
	}
}
