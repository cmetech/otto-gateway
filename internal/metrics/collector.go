package metrics

import "github.com/prometheus/client_golang/prometheus"

// poolCollector is a pull-style prometheus.Collector: it snapshots the pool +
// session sources at scrape time (no background goroutine, no accumulated
// state). This mirrors how /health/pool and /admin read the same signals.
type poolCollector struct {
	pool     func() PoolStats
	sessions func() SessionStats

	size           *prometheus.Desc
	alive          *prometheus.Desc
	busy           *prometheus.Desc
	healthy        *prometheus.Desc
	spawnFailing   *prometheus.Desc
	lastSpawnErrTS *prometheus.Desc
	lastProgressTS *prometheus.Desc
	sessionsActive *prometheus.Desc

	// Track 4b monotonic counters.
	slotRespawns     *prometheus.Desc
	pingEscalations  *prometheus.Desc
	pingSuspendSkips *prometheus.Desc
	sessionsReaped   *prometheus.Desc
}

func newPoolCollector(pool func() PoolStats, sessions func() SessionStats) *poolCollector {
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
		slotRespawns: prometheus.NewDesc("gw_pool_slot_respawns_total",
			"Total lazy slot respawns since start.", nil, nil),
		pingEscalations: prometheus.NewDesc("gw_acp_ping_escalations_total",
			"Total liveness-ping failures escalated to a worker teardown.", nil, nil),
		pingSuspendSkips: prometheus.NewDesc("gw_acp_ping_suspend_skips_total",
			"Total liveness-ping cycles skipped after a detected suspend/resume.", nil, nil),
		sessionsReaped: prometheus.NewDesc("gw_sessions_reaped_total",
			"Total stateful sessions reaped for idleness.", nil, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{
		c.size, c.alive, c.busy, c.healthy, c.spawnFailing, c.lastSpawnErrTS,
		c.lastProgressTS, c.sessionsActive, c.slotRespawns, c.pingEscalations,
		c.pingSuspendSkips, c.sessionsReaped,
	} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool()
	sess := c.sessions()
	gauge := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v)
	}
	counter := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v)
	}
	gauge(c.size, float64(s.Size))
	gauge(c.alive, float64(s.Alive))
	gauge(c.busy, float64(s.Busy))
	gauge(c.healthy, b2f(s.Healthy))
	gauge(c.spawnFailing, b2f(s.SpawnFailing))
	gauge(c.lastSpawnErrTS, s.LastSpawnErrUnixSec)
	gauge(c.lastProgressTS, s.LastProgressUnixSec)
	gauge(c.sessionsActive, float64(sess.Active))
	counter(c.slotRespawns, float64(s.SlotRespawns))
	counter(c.pingEscalations, float64(s.PingEscalations))
	counter(c.pingSuspendSkips, float64(s.PingSuspendSkips))
	counter(c.sessionsReaped, float64(sess.Reaped))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
