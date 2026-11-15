// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// The paths of Send that the main tests do not take: an action that cannot
// be encoded, the call interceptor, and an endpoint that closes while a
// SEND waits for room in the queue.
func TestSendPaths(t *testing.T) {
	checkNoLeak(t)
	var intercepted atomic.Int32
	conn := newFakeConn()
	conn.blockW = make(chan struct{}) // the writer never finishes a write
	e := newEndpoint(conn, ocpp.V21, ocpp.ChargingStation, nil, func(c *Config) {
		c.CallInterceptor = func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
			intercepted.Add(1)
			return next(ctx)
		}
	})
	defer func() { _ = e.Close() }()
	ctx := context.Background()
	if err := e.Send(ctx, "", nil); err == nil {
		t.Fatal("a SEND without an action was accepted")
	}
	// One SEND is taken by the blocked writer, 16 fill the queue.
	for range 17 {
		if err := e.Send(ctx, "NotifyPeriodicEventStream", nil); err != nil {
			t.Fatal(err)
		}
	}
	waiting := make(chan error, 1)
	go func() { waiting <- e.Send(ctx, "NotifyPeriodicEventStream", nil) }()
	// Unwritten: the 17 above and the one that waits for room.
	eventually(t, "the last SEND to wait for room in the queue", func() bool { return e.unwritten.Load() == 18 })
	_ = conn.Close(0, "")
	go func() { _ = e.Close() }()
	select {
	case err := <-waiting:
		if err == nil {
			t.Fatal("a SEND was queued on a closed endpoint")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a waiting SEND did not return when the endpoint closed")
	}
	if intercepted.Load() < 18 {
		t.Fatalf("interceptor saw %d SENDs", intercepted.Load())
	}
}

// panicSender panics on every SEND.
type panicSender struct{ ocpp.DispatcherFunc }

func (panicSender) DispatchSend(context.Context, ocpp.Peer, string, json.RawMessage) error {
	panic("send handler")
}

// A SEND handler that panics is recovered, through the handler interceptor,
// and reported as ErrHandlerPanic.
func TestSendHandlerPanic(t *testing.T) {
	checkNoLeak(t)
	m := &handledMetrics{}
	var intercepted atomic.Int32
	d := panicSender{ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})}
	_, raw := rawPeer(t, ocpp.V21, d, func(c *Config) {
		c.Metrics = m
		c.HandlerInterceptor = func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
			intercepted.Add(1)
			return next(ctx)
		}
	})
	writeRaw(t, raw, `[6,"s","NotifyPeriodicEventStream",{}]`)
	writeRaw(t, raw, `[2,"c","Heartbeat",{}]`)
	if f := readFrame(t, raw, ocpp.V21); f.Type != ocppj.CallResult || f.ID != "c" {
		t.Fatalf("got %+v", f)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errs) < 1 || !errors.Is(m.errs[0], ocpp.ErrHandlerPanic) || intercepted.Load() != 2 {
		t.Fatalf("errors %v, intercepted %d", m.errs, intercepted.Load())
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(1000, 0) // a burst below 1 is 1
	if !l.wait(context.Background()) {
		t.Fatal("the first token was refused")
	}
	slow := newLimiter(0.001, 1)
	slow.wait(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if slow.wait(ctx) {
		t.Fatal("a token was given before its time")
	}
}

// The wait for a token is at least a nanosecond and at most maxWait for
// every rate a float64 holds: also where the time until the token, computed
// as a float64, is beyond what a time.Duration holds, infinite, or not a
// number. Where it fits it is the time until the token.
func TestLimiterDelay(t *testing.T) {
	for name, tt := range map[string]struct {
		rate   float64
		lo, hi time.Duration
	}{
		"1 a second":           {1, time.Second, time.Second},
		"1000 a second":        {1000, time.Millisecond, time.Millisecond},
		"1 in two seconds":     {0.5, 2 * time.Second, 2 * time.Second},
		"1 in half an hour":    {1.0 / 1800, 30*time.Minute - time.Microsecond, 30*time.Minute + time.Microsecond},
		"1 an hour":            {1.0 / 3600, maxWait - time.Microsecond, maxWait},
		"1 in two hours":       {1.0 / 7200, maxWait, maxWait},
		"1e-9 a second":        {1e-9, maxWait, maxWait},
		"2^63 ns":              {1e9 / math.Exp2(63), maxWait, maxWait},
		"1e-300 a second":      {1e-300, maxWait, maxWait},
		"the smallest rate":    {math.SmallestNonzeroFloat64, maxWait, maxWait},
		"1e9 a second":         {1e9, time.Nanosecond, time.Nanosecond},
		"2e9 a second":         {2e9, time.Nanosecond, time.Nanosecond},
		"the largest rate":     {math.MaxFloat64, time.Nanosecond, time.Nanosecond},
		"an infinite rate":     {math.Inf(1), time.Nanosecond, time.Nanosecond},
		"a rate not a number":  {math.NaN(), maxWait, maxWait},
		"a rate of zero":       {0, maxWait, maxWait},
		"a negative rate":      {-1, time.Nanosecond, time.Nanosecond},
		"minus infinity":       {math.Inf(-1), time.Nanosecond, time.Nanosecond},
		"the smallest, signed": {-math.SmallestNonzeroFloat64, time.Nanosecond, time.Nanosecond},
	} {
		l := newLimiter(tt.rate, 1)
		l.tokens = 0
		if got := l.delay(); got < tt.lo || got > tt.hi {
			t.Errorf("%s: a wait of %v (%d ns), want %v to %v", name, got, int64(got), tt.lo, tt.hi)
		}
	}
	// Tokens that are no number (an infinite rate times no time) wait too.
	l := newLimiter(1, 1)
	l.tokens = math.NaN()
	if got := l.delay(); got != maxWait {
		t.Errorf("tokens not a number: a wait of %v, want %v", got, maxWait)
	}
}

// A token that is further away than maxWait is given at its time: the wait
// is taken in steps. With a rate too low for any token, wait ends with its
// context and gives none.
func TestLimiterLowRates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		l := newLimiter(1.0/(3*3600), 1) // one token in three hours
		if !l.wait(context.Background()) {
			t.Fatal("the first token, of the burst, was refused")
		}
		if d := time.Since(start); d != 0 {
			t.Fatalf("the first token took %v", d)
		}
		if !l.wait(context.Background()) {
			t.Fatal("the second token was refused")
		}
		if d := time.Since(start); d < 3*time.Hour || d > 3*time.Hour+time.Second {
			t.Fatalf("the second token after %v, want 3h", d)
		}

		for _, rate := range []float64{1e-300, math.SmallestNonzeroFloat64} {
			start = time.Now()
			l = newLimiter(rate, 1)
			l.wait(context.Background())
			ctx, cancel := context.WithTimeout(context.Background(), 1000*time.Hour)
			if l.wait(ctx) {
				t.Fatalf("rate %v: a second token after %v", rate, time.Since(start))
			}
			cancel()
			if d := time.Since(start); d != 1000*time.Hour {
				t.Fatalf("rate %v: the wait ended after %v, want with its context after 1000h", rate, d)
			}
		}
	})
}

// An endpoint that closes while its dispatcher waits for the rate limiter
// stops without dispatching.
func TestCloseWhileRateLimited(t *testing.T) {
	checkNoLeak(t)
	var handled atomic.Int32
	e, raw := rawPeer(t, ocpp.V201, ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
		handled.Add(1)
		return json.RawMessage(`{}`), nil
	}), func(c *Config) { c.IncomingRate, c.IncomingBurst = 0.001, 1 })
	writeRaw(t, raw, `[2,"1","Heartbeat",{}]`)
	writeRaw(t, raw, `[2,"2","Heartbeat",{}]`)
	readFrame(t, raw, ocpp.V201)
	// The first CALL is answered; the second is off the queue and waits
	// for the limiter, about 1000 s.
	eventually(t, "the second CALL to wait for the limiter", func() bool {
		return e.handling.Load() == 1 && e.active.Load() == 0 && len(e.incoming) == 0
	})
	_ = e.Close()
	if n := handled.Load(); n != 1 {
		t.Fatalf("%d CALLs handled", n)
	}
}
