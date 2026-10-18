// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
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
	time.Sleep(20 * time.Millisecond)
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
	time.Sleep(20 * time.Millisecond)
	_ = e.Close()
	if n := handled.Load(); n != 1 {
		t.Fatalf("%d CALLs handled", n)
	}
}
