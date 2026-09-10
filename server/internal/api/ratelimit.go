package api

import (
	"sync"
	"time"
)

// rateLimiter is a token bucket: burst permits available at once, refilling one
// every interval.
//
// It is deliberately global to the route rather than keyed by client. The
// routes it guards are unauthenticated and reachable by anything that can
// reach the listener, so a per-client key is a value the caller chooses and
// would only tell an attacker to vary it. One operator drives this server, and
// the budget is sized for a person rather than for a share of one.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	burst    int
	tokens   int
	last     time.Time
}

func newRateLimiter(burst int, interval time.Duration) *rateLimiter {
	return &rateLimiter{interval: interval, burst: burst, tokens: burst}
}

// allow spends a permit if one is available, refilling first for the time since
// the last call. now is passed in so tests do not have to sleep.
func (l *rateLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.last.IsZero() {
		l.last = now
	}
	if elapsed := now.Sub(l.last); elapsed >= l.interval {
		gained := int(elapsed / l.interval)
		l.tokens = min(l.tokens+gained, l.burst)
		// Only whole intervals are credited; the remainder carries forward so a
		// stream of sub-interval calls still earns a permit eventually.
		l.last = l.last.Add(time.Duration(gained) * l.interval)
	}

	if l.tokens == 0 {
		return false
	}
	l.tokens--
	return true
}
