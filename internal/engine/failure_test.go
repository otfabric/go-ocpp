// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/transport"
	"github.com/otfabric/go-ocpp/ocppj"
)

// fakeConn is a transport.Conn whose reads and writes the test controls.
type fakeConn struct {
	reads     chan []byte
	readErr   error         // returned by ReadMessage once reads is drained and closed
	writeErr  error         // returned by every WriteMessage when set
	blockW    chan struct{} // when set, writes block until it is closed or the conn closes
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	written   [][]byte
	noPong    bool // Ping waits for a pong that never comes
	pings     atomic.Int64
}

func newFakeConn() *fakeConn {
	return &fakeConn{reads: make(chan []byte, 16), closed: make(chan struct{})}
}

func (c *fakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	select {
	case m, ok := <-c.reads:
		if !ok {
			return nil, c.readErr
		}
		return m, nil
	case <-c.closed:
		return nil, transport.ErrClosed
	case <-ctx.Done():
		return nil, transport.ErrClosed
	}
}

func (c *fakeConn) WriteMessage(ctx context.Context, p []byte) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	if c.blockW != nil {
		select {
		case <-c.blockW:
		case <-c.closed:
			return transport.ErrClosed
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.mu.Lock()
	c.written = append(c.written, append([]byte(nil), p...))
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) Close(int, string) error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeConn) Subprotocol() string { return "ocpp1.6" }

func (c *fakeConn) Abort() { _ = c.Close(0, "") }

func (c *fakeConn) Ping(ctx context.Context) error {
	c.pings.Add(1)
	if !c.noPong {
		return nil
	}
	select {
	case <-c.closed:
		return transport.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func fakeEndpoint(t *testing.T, conn *fakeConn, mod func(*Config)) *Endpoint {
	t.Helper()
	e := newEndpoint(conn, ocpp.V16, ocpp.ChargingStation, nil, mod)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestReadErrorLosesConnection(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	conn.readErr = errors.New("network unreachable")
	close(conn.reads)
	e := fakeEndpoint(t, conn, nil)
	<-e.Done()
	if !errors.Is(e.Err(), ocpp.ErrConnectionLost) {
		t.Fatalf("Err = %v", e.Err())
	}
}

func TestWriteErrorLosesConnection(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	conn.writeErr = errors.New("broken pipe")
	e := fakeEndpoint(t, conn, nil)
	err := e.Call(context.Background(), "Heartbeat", nil, nil)
	if !errors.Is(err, ocpp.ErrConnectionLost) {
		t.Fatalf("call: %v", err)
	}
	<-e.Done()
	if !errors.Is(e.Err(), ocpp.ErrConnectionLost) {
		t.Fatalf("Err = %v", e.Err())
	}
}

func TestRequestErrors(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, nil, nil)
	ctx := context.Background()

	// Not marshallable, and marshallable but not a JSON object: refused
	// before anything is sent, and the slot is free again.
	if err := e.Call(ctx, "Heartbeat", make(chan int), nil); err == nil {
		t.Fatal("a channel was marshalled")
	}
	if err := e.Call(ctx, "Heartbeat", []int{1}, nil); err == nil {
		t.Fatal("an array was sent as a payload")
	}

	// An empty json.RawMessage is sent as {}.
	done := make(chan error, 1)
	var resp struct {
		N int `json:"n"`
	}
	go func() { done <- e.Call(ctx, "Heartbeat", json.RawMessage(nil), &resp) }()
	f := readFrame(t, raw, ocpp.V16)
	if string(f.Payload) != "{}" {
		t.Fatalf("payload %s", f.Payload)
	}
	// A response that does not fit resp is a protocol error.
	writeRaw(t, raw, `[3,"`+f.ID+`",{"n":"not a number"}]`)
	if err := <-done; !errors.Is(err, ocpp.ErrProtocol) {
		t.Fatalf("err = %v, want ErrProtocol", err)
	}
}

// Waiting for the slot ends with the caller's context, or with the
// endpoint.
func TestWaitingForTheSlot(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, nil, nil)
	first := make(chan error, 1)
	go func() { first <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	readFrame(t, raw, ocpp.V16) // the first CALL holds the slot, unanswered

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := e.Call(ctx, "Heartbeat", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting with an expiring context: %v", err)
	}

	waiting := make(chan error, 1)
	go func() { waiting <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for e.waiting.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	_ = e.Close()
	if err := <-waiting; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("waiting when the endpoint closed: %v", err)
	}
	if err := <-first; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("pending when the endpoint closed: %v", err)
	}
}

// A CALL that cannot be handed to the writer, because the writer is stuck
// on an earlier frame, ends with the caller's context or with the endpoint.
func TestStuckWriter(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	conn.blockW = make(chan struct{})
	e := fakeEndpoint(t, conn, func(c *Config) { c.ResponseTimeout = 20 * time.Millisecond; c.WriteTimeout = time.Minute })
	ctx := context.Background()
	// The first CALL is taken by the writer, which blocks; it times out.
	if err := e.Call(ctx, "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrTimeout) {
		t.Fatalf("first: %v", err)
	}
	// The second is queued for the writer and times out too.
	if err := e.Call(ctx, "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrTimeout) {
		t.Fatalf("second: %v", err)
	}
	// The third cannot be queued: its context ends first.
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := e.Call(short, "Heartbeat", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third: %v", err)
	}
	// The fourth cannot be queued either; the endpoint closes meanwhile.
	fourth := make(chan error, 1)
	go func() { fourth <- e.Call(ctx, "Heartbeat", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	_ = e.Close()
	if err := <-fourth; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("fourth: %v", err)
	}
}

// finish completes a call once; a second completion, such as a timeout that
// fires as the response arrives, does nothing.
func TestFinishOnce(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, nil, nil)
	done := make(chan error, 1)
	go func() { done <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	f := readFrame(t, raw, ocpp.V16)
	e.mu.Lock()
	p := e.pending
	e.mu.Unlock()
	writeRaw(t, raw, `[3,"`+f.ID+`",{}]`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	e.finish(p, callResult{err: ocpp.ErrTimeout})
	if len(p.ch) != 0 {
		t.Fatal("a second result was delivered")
	}
}

// sendDispatcher handles CALLs with echo and records SENDs.
type sendDispatcher struct {
	got chan string
	err error
}

func (d sendDispatcher) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	return echo(ctx, p, action, payload)
}

func (d sendDispatcher) DispatchSend(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) error {
	d.got <- action + " " + string(payload)
	return d.err
}

// An incoming SEND (2.1) goes to DispatchSend and is never answered, also
// when it fails or there is no SEND handler.
func TestIncomingSend(t *testing.T) {
	checkNoLeak(t)
	m := &countingMetrics{}
	d := sendDispatcher{got: make(chan string, 4), err: errors.New("not now")}
	_, raw := rawPeer(t, ocpp.V21, d, func(c *Config) { c.Metrics = m })
	writeRaw(t, raw, `[6,"s1","NotifyPeriodicEventStream",{"id":1}]`)
	if got := <-d.got; got != `NotifyPeriodicEventStream {"id":1}` {
		t.Fatalf("dispatched %q", got)
	}
	expectNone(t, raw, 50*time.Millisecond)
	// Without a SendDispatcher.
	_, raw2 := rawPeer(t, ocpp.V21, ocpp.DispatcherFunc(echo), nil)
	writeRaw(t, raw2, `[6,"s2","NotifyPeriodicEventStream",{}]`)
	writeRaw(t, raw2, `[2,"c","Heartbeat",{}]`)
	if f := readFrame(t, raw2, ocpp.V21); f.ID != "c" {
		t.Fatalf("a SEND was answered: %+v", f)
	}
}

// An incoming CALLRESULTERROR is reported, not answered.
func TestIncomingCallResultError(t *testing.T) {
	checkNoLeak(t)
	m := &countingMetrics{}
	_, raw := rawPeer(t, ocpp.V21, ocpp.DispatcherFunc(echo), func(c *Config) { c.Metrics = m })
	writeRaw(t, raw, `[5,"r1","FormatViolation","bad response",{}]`)
	writeRaw(t, raw, `[2,"c","Heartbeat",{}]`)
	if f := readFrame(t, raw, ocpp.V21); f.ID != "c" {
		t.Fatalf("got %+v", f)
	}
	if m.protocolErrors.Load() != 1 {
		t.Fatalf("protocol errors %d, want 1", m.protocolErrors.Load())
	}
}

// Send writes a SEND frame without waiting for an outstanding CALL.
func TestOutgoingSend(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V21, nil, func(c *Config) {
		c.CanSend = func(action string) bool { return action != "Reset" }
	})
	ctx := context.Background()
	pending := make(chan error, 1)
	go func() { pending <- e.Call(ctx, "Heartbeat", nil, nil) }()
	call := readFrame(t, raw, ocpp.V21)
	if err := e.Send(ctx, "NotifyPeriodicEventStream", map[string]int{"id": 7}); err != nil {
		t.Fatal(err)
	}
	if f := readFrame(t, raw, ocpp.V21); f.Type != ocppj.Send || f.Action != "NotifyPeriodicEventStream" || string(f.Payload) != `{"id":7}` {
		t.Fatalf("got %+v", f)
	}
	writeRaw(t, raw, `[3,"`+call.ID+`",{}]`)
	if err := <-pending; err != nil {
		t.Fatal(err)
	}
	if err := e.Send(ctx, "Reset", nil); !errors.Is(err, ocpp.ErrWrongDirection) {
		t.Fatalf("direction: %v", err)
	}
	if err := e.Send(ctx, "X", make(chan int)); err == nil {
		t.Fatal("an unmarshallable SEND was accepted")
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	for range 20 { // fill the queue: nobody reads any more
		_ = e.Send(ctx, "NotifyPeriodicEventStream", nil)
	}
	_ = e.Send(done, "NotifyPeriodicEventStream", nil)
	_ = e.Close()
	if err := e.Send(ctx, "NotifyPeriodicEventStream", nil); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("after close: %v", err)
	}

	old, _ := rawPeer(t, ocpp.V201, nil, nil)
	if err := old.Send(ctx, "NotifyPeriodicEventStream", nil); !errors.Is(err, ocpp.ErrUnsupportedVersion) {
		t.Fatalf("SEND on 2.0.1: %v", err)
	}
}

type validated struct {
	N int `json:"n"`
}

func (v *validated) Validate() error {
	if v.N < 0 {
		return &ocpp.ValidationError{Code: ocpp.PropertyConstraintViolation, Field: "n", Reason: "below the minimum 0"}
	}
	return nil
}

// A CALLRESULT that cannot be processed is answered with a CALLRESULTERROR
// on 2.1, and only reported to the caller on earlier versions.
func TestCallResultErrorSent(t *testing.T) {
	checkNoLeak(t)
	for _, tt := range []struct {
		v      ocpp.Version
		answer string
		code   ocpp.ErrorCode
	}{
		{ocpp.V21, `{"n":-1}`, ocpp.PropertyConstraintViolation},
		{ocpp.V21, `{"n":"x"}`, ocpp.FormatViolation},
		{ocpp.V201, `{"n":-1}`, ""},
	} {
		e, raw := rawPeer(t, tt.v, nil, nil)
		done := make(chan error, 1)
		go func() { done <- e.Call(context.Background(), "GetThing", nil, &validated{}) }()
		f := readFrame(t, raw, tt.v)
		writeRaw(t, raw, `[3,"`+f.ID+`",`+tt.answer+`]`)
		if err := <-done; !errors.Is(err, ocpp.ErrProtocol) {
			t.Fatalf("%s %s: %v", tt.v, tt.answer, err)
		}
		if tt.code == "" {
			expectNone(t, raw, 50*time.Millisecond)
			continue
		}
		r := readFrame(t, raw, tt.v)
		if r.Type != ocppj.CallResultError || r.ID != f.ID || r.ErrorCode != tt.code {
			t.Fatalf("%s %s: got %+v", tt.v, tt.answer, r)
		}
	}
}

// Error descriptions are cut to 255 characters on 2.x, not on 1.6.
func TestDescriptionLimit(t *testing.T) {
	checkNoLeak(t)
	long := strings.Repeat("é", 300)
	d := ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
		return nil, ocpp.NewCallError(ocpp.GenericError, long)
	})
	for v, want := range map[ocpp.Version]int{ocpp.V201: 255, ocpp.V16: 300} {
		_, raw := rawPeer(t, v, d, nil)
		writeRaw(t, raw, `[2,"x","Anything",{}]`)
		if f := readFrame(t, raw, v); len([]rune(f.Description)) != want {
			t.Errorf("%s: description of %d characters, want %d", v, len([]rune(f.Description)), want)
		}
	}
}

func TestLost(t *testing.T) {
	if err := lost(ocpp.ErrConnectionLost); err != ocpp.ErrConnectionLost {
		t.Errorf("lost wrapped an error that already says so: %v", err)
	}
	if err := lost(errors.New("x")); !errors.Is(err, ocpp.ErrConnectionLost) {
		t.Errorf("lost(x) = %v", err)
	}
}

func TestIsObject(t *testing.T) {
	for in, want := range map[string]bool{`{}`: true, " \t\r\n{\"a\":1}": true, `{`: false, `[]`: false, ``: false, `  `: false, `null`: false} {
		if got := isObject(json.RawMessage(in)); got != want {
			t.Errorf("isObject(%q) = %v", in, got)
		}
	}
}

// A response that cannot be encoded is logged and not sent; the connection
// stays usable. The engine only builds valid frames, so this is a guard.
func TestUnencodableResponse(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, ocpp.DispatcherFunc(echo), nil)
	e.sendResult(ocppj.Frame{Type: ocppj.CallResult, ID: strings.Repeat("x", 37)})
	writeRaw(t, raw, `[2,"ok","Heartbeat",{}]`)
	if f := readFrame(t, raw, ocpp.V16); f.ID != "ok" {
		t.Fatalf("got %+v", f)
	}
}

// A peer that floods CALLs while a handler runs fills the incoming queue;
// the reader then waits, and stops when the endpoint closes.
func TestFloodWhileHandling(t *testing.T) {
	checkNoLeak(t)
	block := make(chan struct{})
	slow := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})
	e, raw := rawPeer(t, ocpp.V16, slow, nil)
	go func() {
		for i := range 40 {
			if raw.conn.WriteMessage(context.Background(), []byte(fmt.Sprintf(`[2,"%d","Heartbeat",{}]`, i))) != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(e.incoming) < cap(e.incoming) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(e.incoming) != cap(e.incoming) {
		t.Fatalf("incoming queue at %d of %d", len(e.incoming), cap(e.incoming))
	}
	_ = e.Close()
	close(block)
	<-e.Done()
}

// A call racing Close returns at once with the close's error, whether it
// became pending before shutdown ran or after: it never waits for its
// response timeout.
func TestCallRacingClose(t *testing.T) {
	checkNoLeak(t)
	for i := range 200 {
		e, _ := rawPeer(t, ocpp.V201, nil, func(c *Config) { c.ResponseTimeout = time.Hour })
		done := make(chan error, 1)
		go func() { done <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
		if i%2 == 1 {
			runtime.Gosched()
		}
		_ = e.Close()
		select {
		case err := <-done:
			if !errors.Is(err, ocpp.ErrClosed) && !errors.Is(err, ocpp.ErrConnectionLost) {
				t.Fatalf("round %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: the call waited for its response timeout", i)
		}
	}
}

// Pings go out every PingInterval while pongs arrive; a missing pong loses
// the connection.
func TestPing(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	e := fakeEndpoint(t, conn, func(c *Config) { c.PingInterval = 5 * time.Millisecond })
	deadline := time.Now().Add(5 * time.Second)
	for conn.pings.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if conn.pings.Load() < 3 || e.Err() != nil {
		t.Fatalf("%d pings, err %v", conn.pings.Load(), e.Err())
	}

	dead := newFakeConn()
	dead.noPong = true
	e2 := fakeEndpoint(t, dead, func(c *Config) { c.PingInterval = 5 * time.Millisecond })
	select {
	case <-e2.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a missing pong did not end the connection")
	}
	if !errors.Is(e2.Err(), ocpp.ErrConnectionLost) {
		t.Fatalf("err %v", e2.Err())
	}
}

// MaxBadMessages malformed frames close the connection; fewer do not.
func TestMaxBadMessages(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	e := fakeEndpoint(t, conn, func(c *Config) { c.MaxBadMessages = 3 })
	conn.reads <- []byte(`not json`)
	conn.reads <- []byte(`[9,"x"]`)
	time.Sleep(20 * time.Millisecond)
	if e.Err() != nil {
		t.Fatalf("closed after 2 bad messages: %v", e.Err())
	}
	conn.reads <- []byte(`{}`)
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not closed after 3 bad messages")
	}
	if !errors.Is(e.Err(), ocpp.ErrConnectionLost) || !errors.Is(e.Err(), ocpp.ErrProtocol) {
		t.Fatalf("err %v", e.Err())
	}
}

// handledMetrics records the errors of handled CALLs.
type handledMetrics struct {
	ocpp.NopMetrics
	mu   sync.Mutex
	errs []error
}

func (m *handledMetrics) OnCallHandled(_ ocpp.ConnInfo, _ string, _ time.Duration, err error) {
	m.mu.Lock()
	m.errs = append(m.errs, err)
	m.mu.Unlock()
}

// A handler panic is answered with InternalError, reported as
// ErrHandlerPanic, and logged with its stack.
func TestHandlerPanicReported(t *testing.T) {
	checkNoLeak(t)
	var logs bytes.Buffer
	var logMu sync.Mutex
	m := &handledMetrics{}
	d := ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
		panic("boom")
	})
	_, raw := rawPeer(t, ocpp.V201, d, func(c *Config) {
		c.Metrics = m
		c.Logger = slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, nil))
	})
	writeRaw(t, raw, `[2,"p","Heartbeat",{}]`)
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallError || f.ErrorCode != ocpp.InternalError {
		t.Fatalf("got %+v", f)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errs) != 1 || !errors.Is(m.errs[0], ocpp.ErrHandlerPanic) {
		t.Fatalf("OnCallHandled got %v", m.errs)
	}
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(logs.String(), "handler panic") || !strings.Contains(logs.String(), "stack=") {
		t.Fatalf("panic not logged with its stack:\n%s", logs.String())
	}
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// IncomingRate delays dispatch beyond the burst; nothing is refused.
func TestIncomingRate(t *testing.T) {
	checkNoLeak(t)
	_, raw := rawPeer(t, ocpp.V201, ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}), func(c *Config) { c.IncomingRate, c.IncomingBurst = 50, 2 })
	start := time.Now()
	for i := range 7 {
		writeRaw(t, raw, fmt.Sprintf(`[2,"%d","Heartbeat",{}]`, i))
	}
	for range 7 {
		if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallResult {
			t.Fatalf("got %+v", f)
		}
	}
	// 2 at once, then 5 at 50/s: at least 100 ms.
	if d := time.Since(start); d < 90*time.Millisecond {
		t.Fatalf("7 CALLs dispatched in %s", d)
	}
}

func TestIdle(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V201, nil, nil)
	if !e.Idle() {
		t.Fatal("a new endpoint is not idle")
	}
	done := make(chan error, 1)
	go func() { done <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	f := readFrame(t, raw, ocpp.V201)
	if e.Idle() {
		t.Fatal("idle with a CALL outstanding")
	}
	writeRaw(t, raw, `[3,"`+f.ID+`",{}]`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The writer records a frame as written just after the peer can read it.
	deadline := time.Now().Add(5 * time.Second)
	for !e.Idle() {
		if time.Now().After(deadline) {
			t.Fatal("not idle after the answer")
		}
		time.Sleep(time.Millisecond)
	}
}

// Functions registered with ocpp.AfterResponse run after the result is
// queued: a CALL they send goes out after the response.
func TestAfterResponse(t *testing.T) {
	checkNoLeak(t)
	var e *Endpoint
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		if !ocpp.AfterResponse(ctx, func() { _ = e.Call(context.Background(), "NotifyReport", nil, nil) }) {
			t.Error("AfterResponse refused a handler's context")
		}
		return json.RawMessage(`{"status":"Accepted"}`), nil
	})
	var raw *raw
	e, raw = rawPeer(t, ocpp.V201, d, nil)
	writeRaw(t, raw, `[2,"r","GetBaseReport",{}]`)
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallResult || f.ID != "r" {
		t.Fatalf("first frame %+v, want the response", f)
	}
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.Call || f.Action != "NotifyReport" {
		t.Fatalf("second frame %+v, want the CALL", f)
	}
	if ocpp.AfterResponse(context.Background(), func() {}) {
		t.Fatal("AfterResponse accepted a context that is not a handler's")
	}
	_ = e.Close()
}

// An incoming CALL keeps the endpoint busy until its answer is written.
func TestIdleWhileHandling(t *testing.T) {
	checkNoLeak(t)
	release := make(chan struct{})
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{"status":"Accepted"}`), nil
	})
	e, raw := rawPeer(t, ocpp.V201, d, nil)
	writeRaw(t, raw, `[2,"h","Reset",{"type":"Immediate"}]`)
	deadline := time.Now().Add(5 * time.Second)
	for !e.HandlerRunning() {
		if time.Now().After(deadline) {
			t.Fatal("the handler did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if e.Idle() {
		t.Fatal("idle while a CALL is handled")
	}
	close(release)
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallResult || f.ID != "h" {
		t.Fatalf("answer: %+v", f)
	}
	for !e.Idle() {
		if time.Now().After(deadline) {
			t.Fatal("not idle after the answer")
		}
		time.Sleep(time.Millisecond)
	}
}
