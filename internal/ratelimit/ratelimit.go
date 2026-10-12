// Package ratelimit implements a token bucket that never blocks: callers get
// told how long to wait and reschedule the work instead of parking a goroutine.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a token bucket with an optional pause. The zero value is not usable; use New.
type Limiter struct {
	mu          sync.Mutex
	interval    time.Duration // time to earn one token; 0 means unlimited
	burst       float64
	tokens      float64
	last        time.Time
	pausedUntil time.Time
}

// New allows n events per period, in bursts of up to n.
// n <= 0 or per <= 0 means unlimited, though Pause still applies.
func New(n int, per time.Duration) *Limiter {
	l := &Limiter{}
	if n > 0 && per > 0 {
		l.interval = per / time.Duration(n)
		l.burst = float64(n)
		l.tokens = float64(n)
	}
	return l
}

// Take consumes a token and returns 0 if one is available at now,
// otherwise it returns how long until one will be.
func (l *Limiter) Take(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Before(l.pausedUntil) {
		return l.pausedUntil.Sub(now)
	}
	if l.interval == 0 {
		return 0
	}
	if !l.last.IsZero() && now.After(l.last) {
		l.tokens = min(l.burst, l.tokens+float64(now.Sub(l.last))/float64(l.interval))
	}
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return 0
	}
	return max(time.Duration((1-l.tokens)*float64(l.interval)), time.Nanosecond)
}

// Pause makes Take refuse all tokens until the given time.
func (l *Limiter) Pause(until time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until.After(l.pausedUntil) {
		l.pausedUntil = until
	}
}
