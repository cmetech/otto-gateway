package metrics

import "github.com/prometheus/client_golang/prometheus"

// poolCollector is a pull-style prometheus.Collector: it snapshots the pool +
// session sources at scrape time (no background goroutine, no accumulated
// state). This mirrors how /health/pool and /admin read the same signals.
type poolCollector struct {
	pool     func() PoolStats
	sessions func() int

	size           *prometheus.Desc
	alive          *prometheus.Desc
	busy           *prometheus.Desc
	healthy        *prometheus.Desc
	spawnFailing   *prometheus.Desc
	lastSpawnErrTS *prometheus.Desc
	lastProgressTS *prometheus.Desc
	sessionsActive *prometheus.Desc
}

func newPoolCollector(pool func() PoolStats, sessions func() int) *poolCollector {
	return &poolCollector{
		pool:     pool,
		sessions: sessions,
		size:     prometheus.NewDesc("gw_pool_size", "Configured pool size (warm slots).", nil, nil),
		alive:    prometheus.NewDesc("gw_pool_alive", "Slots currently alive (not dead).", nil, nil),
		busy:     prometheus.NewDesc("gw_pool_busy", "Slots currently checked out (serving).", nil, nil),
		healthy:  prometheus.NewDesc("gw_pool_healthy", "1 if the pool can serve (Size==0 or Alive>0), else 0.", nil, nil),
		spawnFailing: prometheus.NewDesc("gw_pool_spawn_failing",
			"1 if a genuine respawn failure occurred recently (within 2x PingInterval), else 0.", nil, nil),
		lastSpawnErrTS: prometheus.NewDesc("gw_pool_last_spawn_error_timestamp_seconds",
			"Unix time of the most recent genuine spawn error; 0 if none.", nil, nil),
		lastProgressTS: prometheus.NewDesc("gw_pool_last_progress_timestamp_seconds",
			"Unix time of the most recent pool forward-progress event (chunk/ping/release).", nil, nil),
		sessionsActive: prometheus.NewDesc("gw_sessions_active", "Active stateful sessions.", nil, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.size
	ch <- c.alive
	ch <- c.busy
	ch <- c.healthy
	ch <- c.spawnFailing
	ch <- c.lastSpawnErrTS
	ch <- c.lastProgressTS
	ch <- c.sessionsActive
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool()
	g := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	g(c.size, float64(s.Size))
	g(c.alive, float64(s.Alive))
	g(c.busy, float64(s.Busy))
	g(c.healthy, b2f(s.Healthy))
	g(c.spawnFailing, b2f(s.SpawnFailing))
	g(c.lastSpawnErrTS, s.LastSpawnErrUnixSec)
	g(c.lastProgressTS, s.LastProgressUnixSec)
	g(c.sessionsActive, float64(c.sessions()))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
