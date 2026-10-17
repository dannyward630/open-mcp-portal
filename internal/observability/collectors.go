package observability

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolCollector exposes a pgx pool's statistics, read at scrape time.
type PoolCollector struct {
	stat func() *pgxpool.Stat

	acquired, idle, total, max  *prometheus.Desc
	acquires, acquireWait, wait *prometheus.Desc
}

// NewPoolCollector reads the pool's Stat on every scrape (nil pool: the
// collector only describes its metrics).
func NewPoolCollector(pool *pgxpool.Pool) *PoolCollector {
	d := func(name, help string) *prometheus.Desc { return prometheus.NewDesc(name, help, nil, nil) }
	c := &PoolCollector{
		acquired:    d("omp_db_pool_acquired_connections", "PostgreSQL connections in use."),
		idle:        d("omp_db_pool_idle_connections", "Idle PostgreSQL connections in the pool."),
		total:       d("omp_db_pool_connections", "PostgreSQL connections in the pool (in use, idle and opening)."),
		max:         d("omp_db_pool_max_connections", "The pool's connection limit."),
		acquires:    d("omp_db_pool_acquires_total", "Connections acquired from the pool."),
		acquireWait: d("omp_db_pool_empty_acquires_total", "Acquires that had to wait because no connection was idle."),
		wait:        d("omp_db_pool_acquire_wait_seconds_total", "Total time spent acquiring connections."),
	}
	if pool != nil {
		c.stat = pool.Stat
	}
	return c
}

func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{c.acquired, c.idle, c.total, c.max, c.acquires, c.acquireWait, c.wait} {
		ch <- d
	}
}

func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	if c.stat == nil {
		return
	}
	s := c.stat()
	gauge := func(d *prometheus.Desc, v float64) { ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v) }
	counter := func(d *prometheus.Desc, v float64) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.CounterValue, v)
	}
	gauge(c.acquired, float64(s.AcquiredConns()))
	gauge(c.idle, float64(s.IdleConns()))
	gauge(c.total, float64(s.TotalConns()))
	gauge(c.max, float64(s.MaxConns()))
	counter(c.acquires, float64(s.AcquireCount()))
	counter(c.acquireWait, float64(s.EmptyAcquireCount()))
	counter(c.wait, s.AcquireDuration().Seconds())
}

// ServerStateCollector is omp_mcp_servers: enabled MCP servers by the
// state of their latest health check (healthy, failing, unknown), read at
// scrape time (at most every 30 seconds).
type ServerStateCollector struct {
	pool *pgxpool.Pool
	desc *prometheus.Desc

	mu      sync.Mutex
	last    map[string]float64
	expires time.Time
}

// NewServerStateCollector returns the collector (nil pool: describe only).
func NewServerStateCollector(pool *pgxpool.Pool) *ServerStateCollector {
	return &ServerStateCollector{pool: pool,
		desc: prometheus.NewDesc("omp_mcp_servers", "Enabled MCP servers by health state of their latest check (healthy, failing, unknown).",
			[]string{"state"}, nil)}
}

func (c *ServerStateCollector) Describe(ch chan<- *prometheus.Desc) { ch <- c.desc }

func (c *ServerStateCollector) Collect(ch chan<- prometheus.Metric) {
	if c.pool == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil || time.Now().After(c.expires) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		rows, err := c.pool.Query(ctx, `SELECT CASE WHEN h.ok IS NULL THEN 'unknown' WHEN h.ok THEN 'healthy' ELSE 'failing' END, count(*)
			FROM mcp_servers s
			LEFT JOIN LATERAL (SELECT ok FROM mcp_health_checks WHERE server_id = s.id ORDER BY checked_at DESC, id DESC LIMIT 1) h ON true
			WHERE s.enabled GROUP BY 1`)
		if err != nil {
			return
		}
		last := map[string]float64{"healthy": 0, "failing": 0, "unknown": 0}
		for rows.Next() {
			var state string
			var n int64
			if rows.Scan(&state, &n) == nil {
				last[state] = float64(n)
			}
		}
		rows.Close()
		c.last, c.expires = last, time.Now().Add(30*time.Second)
	}
	for state, n := range c.last {
		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, n, state)
	}
}
