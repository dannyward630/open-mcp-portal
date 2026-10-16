// Package ratelimit counts events per key in fixed one-period windows, in
// this process's memory (like internal/auth's sign-in limiter; decision 11:
// no Valkey yet, so with several replicas each enforces a limit on its own
// share of the traffic). Counters are ephemeral by design.
package ratelimit

import (
	"sync"
	"time"
)

// maxTracked bounds the map; past it, expired windows are swept.
const maxTracked = 10_000

// Limiter is a fixed-window counter. The zero value is not usable: use New.
type Limiter struct {
	mu      sync.Mutex
	windows map[string]*window
	now     func() time.Time
}

type window struct {
	start time.Time
	count int
}

// New returns an empty limiter.
func New() *Limiter { return &Limiter{windows: map[string]*window{}, now: time.Now} }

// Allow counts one event for key and reports whether it is within limit
// events per period, and if not, how long until the window resets.
func (l *Limiter) Allow(key string, limit int, period time.Duration) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.windows[key]
	if !ok || now.Sub(w.start) >= period {
		if !ok && len(l.windows) >= maxTracked {
			for k, old := range l.windows {
				if now.Sub(old.start) >= period {
					delete(l.windows, k)
				}
			}
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
