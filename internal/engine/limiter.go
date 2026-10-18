// SPDX-License-Identifier: MIT

package engine

import (
	"context"
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
		delay := time.Duration((1 - l.tokens) / l.rate * float64(time.Second))
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
