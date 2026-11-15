// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"math"
	"sync"
	"time"
)

// limiter is a token bucket: rate tokens a second, up to burst. wait takes
// one token, waiting until one is available or ctx ends.
type limiter struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newLimiter(rate float64, burst int) *limiter {
	if burst < 1 {
		burst = 1
	}
	return &limiter{rate: rate, burst: float64(burst), tokens: float64(burst), last: time.Now()}
}

// maxWait bounds one wait for a token. When the next token is further away,
// wait computes the remaining time again after maxWait, so the bound does
// not change when the token is given.
const maxWait = time.Hour

// delay returns how long to wait until the next token is due: at least a
// nanosecond and at most maxWait, whatever the rate. The time is computed
// as a float64, which for a very low rate is beyond what a time.Duration
// holds (and for a rate that is not finite is no number at all); it is
// bounded before it is converted. l.mu is held.
func (l *limiter) delay() time.Duration {
	ns := (1 - l.tokens) / l.rate * float64(time.Second)
	switch {
	case math.IsNaN(ns) || ns >= float64(maxWait):
		return maxWait
	case ns < 1:
		return time.Nanosecond
	}
	return time.Duration(ns)
}

// wait reports false when ctx ended first.
func (l *limiter) wait(ctx context.Context) bool {
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens = min(l.burst, l.tokens+now.Sub(l.last).Seconds()*l.rate)
		l.last = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return true
		}
		delay := l.delay()
		l.mu.Unlock()
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return false
		}
	}
}
