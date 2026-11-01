// Package ratelimit is a small per-key token bucket, used to keep button
// spam and request floods from overloading the bot or the site.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows, per key, burst actions at once then one every interval.
type Limiter struct {
	interval time.Duration
	burst    float64

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// New returns a limiter refilling one token every interval, up to burst.
func New(interval time.Duration, burst int) *Limiter {
	return &Limiter{interval: interval, burst: float64(max(burst, 1)), buckets: map[string]*bucket{}, now: time.Now}
}

// Allow takes one token for key; false when key went over its budget.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweepLocked(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+float64(now.Sub(b.last))/float64(l.interval))
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked forgets idle keys once a minute so memory stays bounded.
func (l *Limiter) sweepLocked(now time.Time) {
	if now.Sub(l.swept) < time.Minute {
		return
	}
	l.swept = now
	full := time.Duration(l.burst * float64(l.interval))
	for k, b := range l.buckets {
		if now.Sub(b.last) > full {
			delete(l.buckets, k)
		}
	}
}

// Len is the number of keys tracked, for tests.
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
