package auth

import (
	"sync"
	"time"
)

// limiter is a fixed-window request counter kept in this process's memory.
// Grounded kept these counters in Valkey; this gateway has no Valkey yet
// (docs/decisions.md #11), so with several replicas each enforces the limit
// on its own share of the traffic. Counters are ephemeral by design.
type limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	now     func() time.Time
}

type window struct {
	start time.Time
	count int
}

// maxTracked bounds the map; past it, expired windows are swept.
const maxTracked = 10_000

func newLimiter() *limiter {
	return &limiter{windows: map[string]*window{}, now: time.Now}
}

// allow counts one event for key and reports whether it is within limit
// events per period, and if not, how long until the window resets.
func (l *limiter) allow(key string, limit int, period time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= period {
		if !ok && len(l.windows) >= maxTracked {
			l.sweep(now, period)
		}
		w = &window{start: now}
		l.windows[key] = w
	}
	w.count++
	if w.count > limit {
		return false, period - now.Sub(w.start)
	}
	return true, 0
}

func (l *limiter) sweep(now time.Time, period time.Duration) {
	for k, w := range l.windows {
		if now.Sub(w.start) >= period {
			delete(l.windows, k)
		}
	}
}
