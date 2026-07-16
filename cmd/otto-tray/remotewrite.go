//go:build darwin || windows

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/castai/promwrite"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// remoteWriter scrapes the local gateway's /metrics and remote-writes the
// selected series to Grafana Cloud. It is the always-on companion to runPoller:
// one goroutine, cancelled via the shared tray context. Every failure mode is a
// non-event — a down gateway, a Grafana error, or a panic in encoding must never
// take down the tray.
type remoteWriter struct {
	scrapeURL string       // e.g. http://127.0.0.1:18080/metrics
	gwHome    string       // config home; cfg re-read from here each cycle
	enabled   *atomic.Bool // live on/off, flipped by the Advanced-menu toggle
	httpc     *http.Client
}

func newRemoteWriter(scrapeURL, gwHome string, enabled *atomic.Bool) *remoteWriter {
	return &remoteWriter{
		scrapeURL: scrapeURL,
		gwHome:    gwHome,
		enabled:   enabled,
		httpc:     &http.Client{Timeout: 10 * time.Second},
	}
}

// runRemoteWriter blocks until ctx is cancelled. Each cycle it re-reads the
// config (so interval/endpoint/token edits take effect without a restart),
// sleeps the configured interval, then performs one scrape+push. Sleeping BEFORE
// the first tick gives the gateway time to come up after a co-launch. `sleep` is
// injectable so tests can drive cycles deterministically; it returns false when
// ctx is cancelled during the wait.
func runRemoteWriter(ctx context.Context, rw *remoteWriter, sleep func(context.Context, time.Duration) bool) {
	for {
		cfg := loadRemoteWriteConfig(rw.gwHome)
		if !sleep(ctx, cfg.Interval) {
			return
		}
		rw.tickOnce(ctx, cfg)
	}
}

// sleepCtx waits d or returns false as soon as ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = defaultRemoteWriteInterval
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// tickOnce performs one scrape+convert+push. It is defensive at every step: a
// recover() guard means a panic in parsing/encoding cannot escape the goroutine,
// and disabled/unconfigured/error paths all just return quietly (debug-logged,
// token scrubbed) to retry next interval.
func (rw *remoteWriter) tickOnce(ctx context.Context, cfg remoteWriteConfig) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("remote-write: recovered from panic", "panic", r)
		}
	}()

	if rw.enabled == nil || !rw.enabled.Load() {
		return // disabled — no-op
	}
	if !cfg.ready() {
		slog.Debug("remote-write: endpoint/user/token not configured; skipping")
		return
	}

	series, err := rw.scrapeAndConvert(ctx, cfg)
	if err != nil {
		// Gateway down / unreachable / bad body — expected and transient.
		slog.Debug("remote-write: scrape failed; will retry next interval", "err", scrubToken(err, cfg.Token))
		return
	}
	if len(series) == 0 {
		return
	}
	if err := rw.push(ctx, cfg, series); err != nil {
		slog.Debug("remote-write: push failed; dropping batch", "err", scrubToken(err, cfg.Token))
	}
}

// scrapeAndConvert GETs /metrics, parses the Prometheus text exposition format,
// filters by the prefix allowlist, and expands each metric family into
// promwrite TimeSeries stamped with external job/instance labels.
func (rw *remoteWriter) scrapeAndConvert(ctx context.Context, cfg remoteWriteConfig) ([]promwrite.TimeSeries, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rw.scrapeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build scrape request: %w", err)
	}
	resp, err := rw.httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape %s: %w", rw.scrapeURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape %s: status %d", rw.scrapeURL, resp.StatusCode)
	}

	// prometheus/common v0.66 requires an explicit name-validation scheme; a
	// zero-value TextParser panics ("scheme unset"). UTF8Validation is the
	// permissive default and accepts the gateway's gw_*/process_* names.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	mfs, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}

	now := time.Now()
	instance := resolveInstance(mfs)
	var out []promwrite.TimeSeries
	for name, mf := range mfs {
		if !allowedSeriesName(name, cfg.Prefixes) {
			continue
		}
		for _, m := range mf.GetMetric() {
			out = append(out, metricToTimeSeries(name, mf.GetType(), m, instance, now)...)
		}
	}
	return out, nil
}

// push sends the batch to Grafana Cloud's remote-write endpoint. promwrite owns
// the protobuf + snappy encoding and the Content-Type/Content-Encoding headers;
// we add HTTP basic auth (instance-id : API-token), which is what Grafana Cloud
// expects on /api/prom/push.
func (rw *remoteWriter) push(ctx context.Context, cfg remoteWriteConfig, series []promwrite.TimeSeries) error {
	client := promwrite.NewClient(cfg.URL, promwrite.HttpClient(rw.httpc))
	_, err := client.Write(
		ctx, &promwrite.WriteRequest{TimeSeries: series},
		promwrite.WriteHeaders(map[string]string{
			"Authorization": "Basic " + basicAuth(cfg.User, cfg.Token),
		}),
	)
	if err != nil {
		return fmt.Errorf("remote write: %w", err)
	}
	return nil
}

// resolveInstance derives the per-gateway `instance` label: the gateway_id from
// gw_build_info when present (stable across restarts), else the hostname, else
// "unknown". Read before prefix filtering so it works regardless of allowlist.
func resolveInstance(mfs map[string]*dto.MetricFamily) string {
	if mf, ok := mfs["gw_build_info"]; ok {
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "gateway_id" && l.GetValue() != "" {
					return l.GetValue()
				}
			}
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "unknown"
}

// metricToTimeSeries expands one dto.Metric into one or more remote-write series,
// handling all Prometheus types. Counters/gauges/untyped are a single series;
// summaries add _sum/_count/{quantile}; histograms add _sum/_count/_bucket{le}.
func metricToTimeSeries(name string, typ dto.MetricType, m *dto.Metric, instance string, now time.Time) []promwrite.TimeSeries {
	base := baseLabels(m, instance)
	mk := func(seriesName string, extra []promwrite.Label, val float64) promwrite.TimeSeries {
		labels := make([]promwrite.Label, 0, len(base)+len(extra)+1)
		labels = append(labels, promwrite.Label{Name: "__name__", Value: seriesName})
		labels = append(labels, base...)
		labels = append(labels, extra...)
		return promwrite.TimeSeries{Labels: labels, Sample: promwrite.Sample{Time: now, Value: val}}
	}

	switch typ {
	case dto.MetricType_GAUGE:
		return []promwrite.TimeSeries{mk(name, nil, m.GetGauge().GetValue())}
	case dto.MetricType_COUNTER:
		return []promwrite.TimeSeries{mk(name, nil, m.GetCounter().GetValue())}
	case dto.MetricType_UNTYPED:
		return []promwrite.TimeSeries{mk(name, nil, m.GetUntyped().GetValue())}
	case dto.MetricType_SUMMARY:
		s := m.GetSummary()
		out := []promwrite.TimeSeries{
			mk(name+"_sum", nil, s.GetSampleSum()),
			mk(name+"_count", nil, float64(s.GetSampleCount())),
		}
		for _, q := range s.GetQuantile() {
			out = append(out, mk(name, []promwrite.Label{{Name: "quantile", Value: formatFloat(q.GetQuantile())}}, q.GetValue()))
		}
		return out
	case dto.MetricType_HISTOGRAM:
		h := m.GetHistogram()
		out := []promwrite.TimeSeries{
			mk(name+"_sum", nil, h.GetSampleSum()),
			mk(name+"_count", nil, float64(h.GetSampleCount())),
		}
		for _, b := range h.GetBucket() {
			// Skip an explicit +Inf bucket if the parser emitted one — we add
			// the canonical +Inf (== sample_count) once below, and a duplicate
			// le="+Inf" series would be rejected by remote-write.
			if math.IsInf(b.GetUpperBound(), 1) {
				continue
			}
			out = append(out, mk(name+"_bucket", []promwrite.Label{{Name: "le", Value: formatFloat(b.GetUpperBound())}}, float64(b.GetCumulativeCount())))
		}
		// The implicit +Inf bucket equals the total sample count.
		out = append(out, mk(name+"_bucket", []promwrite.Label{{Name: "le", Value: "+Inf"}}, float64(h.GetSampleCount())))
		return out
	default:
		return nil
	}
}

// baseLabels builds the shared label set for a metric: the external
// job/instance identity plus the metric's own labels (slot, gateway_id, route…).
func baseLabels(m *dto.Metric, instance string) []promwrite.Label {
	out := make([]promwrite.Label, 0, len(m.GetLabel())+2)
	out = append(out, promwrite.Label{Name: "job", Value: "otto-gateway"})
	if instance != "" {
		out = append(out, promwrite.Label{Name: "instance", Value: instance})
	}
	for _, lp := range m.GetLabel() {
		out = append(out, promwrite.Label{Name: lp.GetName(), Value: lp.GetValue()})
	}
	return out
}

// formatFloat renders a bucket/quantile bound the way Prometheus does, with
// +Inf handled by the caller.
func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// basicAuth encodes user:pass for an HTTP Basic Authorization header.
func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// scrubToken removes the secret token from an error string before it is logged,
// so a token that somehow lands in an error (e.g. a URL echo) is never written
// to disk or the console.
func scrubToken(err error, token string) string {
	s := err.Error()
	if token != "" {
		s = strings.ReplaceAll(s, token, "[REDACTED]")
	}
	return s
}
