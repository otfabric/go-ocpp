// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
)

// doneMetrics records what OnCallDone is given.
type doneMetrics struct {
	ocpp.NopMetrics
	mu    sync.Mutex
	calls []doneCall
}

type doneCall struct {
	action string
	d      time.Duration
	err    error
}

func (m *doneMetrics) OnCallDone(_ ocpp.ConnInfo, action string, d time.Duration, err error) {
	m.mu.Lock()
	m.calls = append(m.calls, doneCall{action, d, err})
	m.mu.Unlock()
}

func (m *doneMetrics) all() []doneCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]doneCall(nil), m.calls...)
}

// OnCallDone reports what Call returns: a response that does not decode or
// fails validation is reported with that error, not as a call that
// succeeded because a response arrived.
func TestOnCallDoneAfterDecoding(t *testing.T) {
	checkNoLeak(t)
	for _, v := range []ocpp.Version{ocpp.V16, ocpp.V201, ocpp.V21} {
		m := &doneMetrics{}
		e, raw := rawPeer(t, v, nil, func(c *Config) { c.Metrics = m })
		answers := []struct {
			payload string
			check   func(error) bool
		}{
			{`{"n":-1}`, func(err error) bool {
				return errors.Is(err, ocpp.ErrProtocol) && errors.Is(err, ocpp.ErrInvalidMessage)
			}},
			{`{"n":"x"}`, func(err error) bool {
				return errors.Is(err, ocpp.ErrProtocol) && !errors.Is(err, ocpp.ErrInvalidMessage)
			}},
			{`{"n":7}`, func(err error) bool { return err == nil }},
		}
		for i, a := range answers {
			done := make(chan error, 1)
			go func() { done <- e.Call(context.Background(), "GetThing", nil, &validated{}) }()
			f := readFrame(t, raw, v)
			writeRaw(t, raw, `[3,"`+f.ID+`",`+a.payload+`]`)
			err := <-done
			if !a.check(err) {
				t.Fatalf("%s %s: Call returned %v", v, a.payload, err)
			}
			calls := m.all()
			if len(calls) != i+1 {
				t.Fatalf("%s %s: OnCallDone was given %d calls, want %d", v, a.payload, len(calls), i+1)
			}
			if got := calls[i]; got.action != "GetThing" || got.err != err || got.d <= 0 {
				t.Fatalf("%s %s: OnCallDone got %q %v after %v, want the error of Call: %v", v, a.payload, got.action, got.err, got.d, err)
			}
			if v == ocpp.V21 && err != nil {
				readFrame(t, raw, v) // the CALLRESULTERROR
			}
		}
	}
}

// A Call that ends before it has the outgoing slot is reported too: a full
// queue (ErrBusy), a context that ends while it waits, an endpoint that has
// ended. Every Call is reported once.
func TestOnCallDoneWithoutTheSlot(t *testing.T) {
	checkNoLeak(t)
	m := &doneMetrics{}
	e, raw := rawPeer(t, ocpp.V16, nil, func(c *Config) { c.Metrics = m; c.MaxQueuedCalls = 1 })
	bg := context.Background()
	wantReports := func(n int, is error) {
		t.Helper()
		calls := m.all()
		if len(calls) != n {
			t.Fatalf("OnCallDone was given %d calls, want %d: %v", len(calls), n, calls)
		}
		if last := calls[n-1]; !errors.Is(last.err, is) || last.action != "Heartbeat" || last.d < 0 {
			t.Fatalf("report %d: %q %v after %v, want %v", n, last.action, last.err, last.d, is)
		}
	}

	first := make(chan error, 1)
	go func() { first <- e.Call(bg, "Heartbeat", nil, nil) }()
	readFrame(t, raw, ocpp.V16) // the first CALL has the slot, unanswered
	ctx, cancel := context.WithCancel(bg)
	second := make(chan error, 1)
	go func() { second <- e.Call(ctx, "Heartbeat", nil, nil) }()
	eventually(t, "the second Call to wait for the slot", func() bool { return e.waiting.Load() == 1 })

	// The queue of one is full.
	if err := e.Call(bg, "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrBusy) {
		t.Fatalf("third call: %v, want ErrBusy", err)
	}
	wantReports(1, ocpp.ErrBusy)

	// The context of the one that waits ends.
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("second call: %v, want context.Canceled", err)
	}
	wantReports(2, context.Canceled)

	// The endpoint ends with the first still pending; then a Call on it.
	_ = e.Close()
	if err := <-first; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("first call: %v, want ErrClosed", err)
	}
	wantReports(3, ocpp.ErrClosed)
	if err := e.Call(bg, "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("call after Close: %v, want ErrClosed", err)
	}
	wantReports(4, ocpp.ErrClosed)
}

// A Call whose context ends after its CALL was written is reported once,
// when it returns and with the context's error; the response that arrives
// later frees the slot and is not reported as a call of its own. A response
// timeout is reported as ErrTimeout.
func TestOnCallDoneOncePerCall(t *testing.T) {
	checkNoLeak(t)
	m := &doneMetrics{}
	e, raw := rawPeer(t, ocpp.V201, nil, func(c *Config) { c.Metrics = m })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Call(ctx, "Heartbeat", nil, nil) }()
	f := readFrame(t, raw, ocpp.V201)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls := m.all(); len(calls) != 1 || !errors.Is(calls[0].err, context.Canceled) {
		t.Fatalf("OnCallDone after the Call returned: %v, want one report with context.Canceled", calls)
	}
	// The late response frees the slot for the next CALL.
	writeRaw(t, raw, `[3,"`+f.ID+`",{}]`)
	go func() { done <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	g := readFrame(t, raw, ocpp.V201)
	writeRaw(t, raw, `[3,"`+g.ID+`",{}]`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if calls := m.all(); len(calls) != 2 || calls[1].err != nil {
		t.Fatalf("OnCallDone after two calls: %v, want two reports, the second nil", calls)
	}

	slow, rawSlow := rawPeer(t, ocpp.V201, nil, func(c *Config) { c.Metrics = m; c.ResponseTimeout = 20 * time.Millisecond })
	err := slow.Call(context.Background(), "Heartbeat", nil, nil)
	readFrame(t, rawSlow, ocpp.V201)
	calls := m.all()
	if len(calls) != 3 || calls[2].err != err || !errors.Is(err, ocpp.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a response timeout: Call returned %v, OnCallDone got %v", err, calls)
	}
}
