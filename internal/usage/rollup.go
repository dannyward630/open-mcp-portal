// Package usage keeps hourly rollups of call records and reports usage
// (docs/phase6-operations.md "Usage", decision 34).
//
// A leader-elected loop (advisory lock omg.usage-rollup, every 5 minutes)
// recomputes the current and previous UTC hour from mcp_calls (delete and
// insert per hour, one transaction each), so late commits are included and
// reports lag at most about five minutes. After an outage it catches up from
// where it stopped (at most MaxHoursPerRun hours a run). Latency goes into
// fixed buckets; p50/p95 are interpolated from merged buckets.
package usage

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ncecere/open-mcp-gateway/internal/store"
	"github.com/ncecere/open-mcp-gateway/internal/store/dbgen"
)

// LockKey is the rollup loop's advisory lock.
const LockKey = "omg.usage-rollup"

// Interval is how often the rollup runs.
const Interval = 5 * time.Minute

// MaxHoursPerRun bounds a catch-up run.
const MaxHoursPerRun = 168

// LatencyBucketsMs are the upper bounds of the latency buckets (then +Inf),
// as in the rollup query.
var LatencyBucketsMs = []float64{5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000, 60000, 120000}

// Service rolls up and reports usage.
type Service struct {
	pool *pgxpool.Pool
	q    *dbgen.Queries
	log  *slog.Logger
	now  func() time.Time
	// Observe is told after each run whether this replica led it and when
	// it succeeded (metrics).
	Observe func(leader bool, success time.Time)
}

// New returns a Service.
func New(pool *pgxpool.Pool, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{pool: pool, log: log, now: time.Now}
	if pool != nil {
		s.q = dbgen.New(pool)
	}
	return s
}

// RollupResult says what a run did.
type RollupResult struct {
	Skipped bool // another replica holds the lock
	Hours   int
	// RolledUpTo is the new watermark.
	RolledUpTo time.Time
}

// Rollup recomputes the hours from the one before the watermark's hour to
// the current hour (on the first run, from the oldest call), at most
// MaxHoursPerRun of them.
func (s *Service) Rollup(ctx context.Context) (RollupResult, error) {
	release, ok, err := store.TryAdvisoryLockText(ctx, s.pool, LockKey)
	if err != nil {
		return RollupResult{}, err
	}
	if !ok {
		if s.Observe != nil {
			s.Observe(false, time.Time{})
		}
		return RollupResult{Skipped: true}, nil
	}
	defer release()
	started := s.now().UTC()
	st, err := s.q.GetUsageRollupState(ctx)
	if err != nil {
		return RollupResult{}, err
	}
	var from time.Time
	if st.RolledUpTo != nil {
		from = st.RolledUpTo.UTC().Truncate(time.Hour).Add(-time.Hour)
	} else {
		oldest, err := s.q.OldestMCPCall(ctx)
		if err != nil {
			return RollupResult{}, err
		}
		from = oldest.UTC().Truncate(time.Hour)
	}
	current := started.Truncate(time.Hour)
	watermark := started
	if hours := int(current.Sub(from)/time.Hour) + 1; hours > MaxHoursPerRun {
		current = from.Add((MaxHoursPerRun - 1) * time.Hour)
		watermark = current.Add(time.Hour)
	}
	res := RollupResult{}
	for h := from; !h.After(current); h = h.Add(time.Hour) {
		if err := s.rollupHour(ctx, h); err != nil {
			return res, err
		}
		res.Hours++
	}
	if err := s.q.SetUsageRolledUpTo(ctx, &watermark); err != nil {
		return res, err
	}
	res.RolledUpTo = watermark
	if s.Observe != nil {
		s.Observe(true, s.now())
	}
	return res, nil
}

func (s *Service) rollupHour(ctx context.Context, hour time.Time) error {
	return store.InTx(ctx, s.pool, func(q *dbgen.Queries, _ pgx.Tx) error {
		if _, err := q.DeleteUsageHour(ctx, hour); err != nil {
			return err
		}
		_, err := q.InsertUsageHour(ctx, hour)
		return err
	})
}

// Loop runs Rollup at start and every Interval until ctx is cancelled.
func (s *Service) Loop(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		if _, err := s.Rollup(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("usage rollup failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Percentile interpolates the p-th percentile (0-1) of latencies counted in
// buckets (LatencyBucketsMs, then +Inf), linearly within the bucket. nil
// without calls.
func Percentile(buckets []int64, p float64) *int32 {
	var total int64
	for _, n := range buckets {
		total += n
	}
	if total == 0 {
		return nil
	}
	rank := p * float64(total)
	var cum int64
	for i, n := range buckets {
		if n == 0 {
			continue
		}
		if float64(cum+n) >= rank {
			lower := 0.0
			if i > 0 {
				lower = LatencyBucketsMs[i-1]
			}
			if i >= len(LatencyBucketsMs) {
				v := int32(lower)
				return &v
			}
			upper := LatencyBucketsMs[i]
			frac := (rank - float64(cum)) / float64(n)
			v := int32(math.Round(lower + frac*(upper-lower)))
			return &v
		}
		cum += n
	}
	v := int32(LatencyBucketsMs[len(LatencyBucketsMs)-1])
	return &v
}
