package auth

import (
	"sync"
	"time"
)

// limiter is a per-key token bucket.
//
// A bucket per key (source IP for pairing, device id for requests) is what
// makes the limit meaningful: one misbehaving phone cannot exhaust the budget of
// another, and an attacker cannot escape the limit by rotating device ids
// because the pairing bucket is keyed by address.
type limiter struct {
	rate  float64 // tokens per second
	burst float64 // bucket capacity
	now   func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	lastGC  time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst int, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	if rate <= 0 {
		rate = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &limiter{
		rate:    rate,
		burst:   float64(burst),
		now:     now,
		buckets: make(map[string]*bucket),
		lastGC:  now(),
	}
}

// Allow consumes one token for key, returning ErrRateLimited when the bucket is
// empty.
func (l *limiter) Allow(key string) error {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	// Refill proportionally to elapsed time, capped at the burst size.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * l.rate
		if b.tokens > l.burst {
			b.tokens = l.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return ErrRateLimited
	}
	b.tokens--
	l.gcLocked(now)
	return nil
}

// gcLocked drops buckets that have been idle long enough to be full again.
// Without this, a long-running host would accumulate one map entry per source
// address it has ever seen.
func (l *limiter) gcLocked(now time.Time) {
	const interval = 5 * time.Minute
	if now.Sub(l.lastGC) < interval {
		return
	}
	l.lastGC = now
	full := l.burst / l.rate
	cutoff := now.Add(-time.Duration(full*2) * time.Second)
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// Reset clears the bucket for a key. It is used by tests and by the CLI after
// an operator resolves a lockout.
func (l *limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

// ResetAll clears every bucket.
func (l *limiter) ResetAll() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buckets = make(map[string]*bucket)
}

// ResetRateLimit exposes bucket clearing to the CLI.
func (m *Manager) ResetRateLimit(key string) { m.limit.Reset(key) }
