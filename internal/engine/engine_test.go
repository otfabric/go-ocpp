// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// peer is a minimal ocpp.Peer for handlers.
type peer struct {
	id string
	v  ocpp.Version
	e  *Endpoint
}

func (p *peer) ID() string            { return p.id }
func (p *peer) Version() ocpp.Version { return p.v }
func (p *peer) Call(ctx context.Context, action string, req, resp any) error {
	return p.e.Call(ctx, action, req, resp)
}

func newEndpoint(conn transport.Conn, v ocpp.Version, role ocpp.Role, d ocpp.Dispatcher, mod func(*Config)) *Endpoint {
	p := &peer{id: "CS1", v: v}
	cfg := Config{Info: ocpp.ConnInfo{StationID: "CS1", Version: v, Role: role, SessionID: role.String()}, Peer: p, Dispatcher: d}
	if mod != nil {
		mod(&cfg)
	}
	p.e = New(conn, cfg)
	p.e.Start()
	return p.e
}

// pair returns a station endpoint and a CSMS endpoint connected in memory.
func pair(t *testing.T, v ocpp.Version, station, csms ocpp.Dispatcher) (*Endpoint, *Endpoint) {
	t.Helper()
	a, b := transport.Pipe(v.Subprotocol())
	s := newEndpoint(a, v, ocpp.ChargingStation, station, nil)
	c := newEndpoint(b, v, ocpp.CSMS, csms, nil)
	t.Cleanup(func() { _ = s.Close(); _ = c.Close() })
	return s, c
}

// raw is the far end of an endpoint's connection, driven frame by frame
// by the test. A goroutine reads it, because a read whose context ends
// closes the connection, as coder/websocket does.
type raw struct {
	conn   transport.Conn
	frames chan []byte
}

// rawPeer returns an endpoint and the other end of its connection.
func rawPeer(t *testing.T, v ocpp.Version, d ocpp.Dispatcher, mod func(*Config)) (*Endpoint, *raw) {
	t.Helper()
	a, b := transport.Pipe(v.Subprotocol())
	e := newEndpoint(a, v, ocpp.ChargingStation, d, mod)
	r := &raw{conn: b, frames: make(chan []byte, 64)}
	go func() {
		defer close(r.frames)
		for {
			data, err := b.ReadMessage(context.Background())
			if err != nil {
				return
			}
			r.frames <- data
		}
	}()
	t.Cleanup(func() { _ = e.Close(); _ = b.Close(1000, "") })
	return e, r
}

func readFrame(t *testing.T, r *raw, v ocpp.Version) ocppj.Frame {
	t.Helper()
	select {
	case data, ok := <-r.frames:
		if !ok {
			t.Fatal("connection closed")
		}
		f, err := ocppj.Decode(data, v, ocppj.Limits{})
		if err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		return f
	case <-time.After(5 * time.Second):
		t.Fatal("no frame within 5s")
	}
	panic("unreachable")
}

// expectNone fails if a frame arrives within d.
func expectNone(t *testing.T, r *raw, d time.Duration) {
	t.Helper()
	select {
	case data := <-r.frames:
		t.Fatalf("unexpected frame %s", data)
	case <-time.After(d):
	}
}

func writeRaw(t *testing.T, r *raw, s string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.conn.WriteMessage(ctx, []byte(s)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func echo(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(fmt.Sprintf(`{"action":%q,"echo":%s}`, action, payload)), nil
}

func checkNoLeak(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(5 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			buf := make([]byte, 1<<16)
			t.Errorf("%d goroutines leaked:\n%s", n-before, buf[:runtime.Stack(buf, true)])
		}
	})
}

func TestBidirectional(t *testing.T) {
	checkNoLeak(t)
	for _, v := range ocpp.Versions() {
		t.Run(v.String(), func(t *testing.T) {
			s, c := pair(t, v, ocpp.DispatcherFunc(echo), ocpp.DispatcherFunc(echo))
			ctx := context.Background()
			var got struct {
				Action string          `json:"action"`
				Echo   json.RawMessage `json:"echo"`
			}
			if err := s.Call(ctx, "Heartbeat", nil, &got); err != nil || got.Action != "Heartbeat" || string(got.Echo) != "{}" {
				t.Fatalf("station call: %v %+v", err, got)
			}
			if err := c.Call(ctx, "Reset", map[string]string{"type": "Soft"}, &got); err != nil || got.Action != "Reset" || string(got.Echo) != `{"type":"Soft"}` {
				t.Fatalf("csms call: %v %+v", err, got)
			}
			var raw json.RawMessage
			if err := c.Call(ctx, "GetConfiguration", json.RawMessage(`{"key":["A"]}`), &raw); err != nil || !strings.Contains(string(raw), `"key":["A"]`) {
				t.Fatalf("raw call: %v %s", err, raw)
			}
		})
	}
}

// A handler that calls the peer while handling a CALL must not deadlock:
// the reader keeps delivering responses while the handler runs.
func TestCallFromHandler(t *testing.T) {
	checkNoLeak(t)
	csms := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		var conf json.RawMessage
		if err := p.Call(ctx, "GetConfiguration", nil, &conf); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"status":"Accepted","configuration":` + string(conf) + `}`), nil
	})
	station := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"configurationKey":[]}`), nil
	})
	s, _ := pair(t, ocpp.V16, station, csms)
	var resp json.RawMessage
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Call(ctx, "BootNotification", nil, &resp); err != nil {
		t.Fatal(err)
	}
	if want := `{"status":"Accepted","configuration":{"configurationKey":[]}}`; string(resp) != want {
		t.Fatalf("got %s, want %s", resp, want)
	}
}

// T6: at most one CALL outstanding per direction.
func TestOneOutstandingCall(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, nil, nil)
	const n = 5
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- e.Call(context.Background(), "Heartbeat", map[string]int{"n": i}, nil)
		}()
	}
	for range n {
		f := readFrame(t, raw, ocpp.V16)
		if f.Type != ocppj.Call {
			t.Fatalf("got %s", f.Type)
		}
		// No second CALL may arrive before this one is answered.
		expectNone(t, raw, 50*time.Millisecond)
		writeRaw(t, raw, `[3,"`+f.ID+`",{}]`)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}

func TestTimeoutAndLateResponse(t *testing.T) {
	checkNoLeak(t)
	m := &countingMetrics{}
	e, raw := rawPeer(t, ocpp.V201, nil, func(c *Config) { c.ResponseTimeout = 50 * time.Millisecond; c.Metrics = m })
	err := e.Call(context.Background(), "Heartbeat", nil, nil)
	if !errors.Is(err, ocpp.ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	f := readFrame(t, raw, ocpp.V201)
	// The slot is free again: the next CALL is sent.
	done := make(chan error, 1)
	go func() { done <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	g := readFrame(t, raw, ocpp.V201)
	if g.ID == f.ID {
		t.Fatal("message ID reused")
	}
	// The late response to the first CALL is dropped, not delivered to the second.
	writeRaw(t, raw, `[3,"`+f.ID+`",{"late":true}]`)
	writeRaw(t, raw, `[3,"`+g.ID+`",{}]`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if m.protocolErrors.Load() != 1 {
		t.Errorf("protocol errors = %d, want 1 (the late response)", m.protocolErrors.Load())
	}
}

// When the caller's context ends, Call returns, but the CALL keeps the slot
// until its response arrives (T6).
func TestContextCancelKeepsSlot(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Call(ctx, "Heartbeat", nil, nil) }()
	f := readFrame(t, raw, ocpp.V16)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	second := make(chan error, 1)
	go func() { second <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	expectNone(t, raw, 50*time.Millisecond)
	writeRaw(t, raw, `[3,"`+f.ID+`",{}]`)
	g := readFrame(t, raw, ocpp.V16)
	writeRaw(t, raw, `[3,"`+g.ID+`",{}]`)
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestBusy(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V16, nil, func(c *Config) { c.MaxQueuedCalls = 1 })
	first := make(chan error, 1)
	go func() { first <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	f := readFrame(t, raw, ocpp.V16)
	second := make(chan error, 1)
	go func() { second <- e.Call(context.Background(), "Heartbeat", nil, nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for e.waiting.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := e.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrBusy) {
		t.Fatalf("third call: %v, want ErrBusy", err)
	}
	writeRaw(t, raw, `[3,"`+f.ID+`",{}]`)
	g := readFrame(t, raw, ocpp.V16)
	writeRaw(t, raw, `[3,"`+g.ID+`",{}]`)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
}

func TestCallErrorReceived(t *testing.T) {
	checkNoLeak(t)
	e, raw := rawPeer(t, ocpp.V201, nil, nil)
	done := make(chan error, 1)
	go func() { done <- e.Call(context.Background(), "SetDisplayMessage", nil, nil) }()
	f := readFrame(t, raw, ocpp.V201)
	writeRaw(t, raw, `[4,"`+f.ID+`","NotSupported","not here",{"x":1}]`)
	err := <-done
	var ce *ocpp.CallError
	if !errors.As(err, &ce) || ce.Code != ocpp.NotSupported || ce.Description != "not here" || string(ce.Details) != `{"x":1}` {
		t.Fatalf("err = %#v", err)
	}
}

func TestHandlerErrors(t *testing.T) {
	checkNoLeak(t)
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		switch action {
		case "Plain":
			return nil, errors.New("database password is hunter2")
		case "Supported":
			return nil, ocpp.NewCallError(ocpp.NotSupported, "no displays")
		case "WrongVersionCode":
			return nil, ocpp.NewCallError(ocpp.FormatViolation, "2.x code on 1.6")
		case "Panic":
			panic("boom")
		case "Nil":
			return nil, nil
		case "NotObject":
			return json.RawMessage(`[1]`), nil
		}
		return json.RawMessage(`{}`), nil
	})
	tests := []struct {
		action, code, desc string
	}{
		{"Plain", "InternalError", ""},
		{"Supported", "NotSupported", "no displays"},
		{"WrongVersionCode", "InternalError", ""},
		{"Panic", "InternalError", ""},
		{"Nil", "InternalError", ""},
		{"NotObject", "InternalError", ""},
	}
	_, raw := rawPeer(t, ocpp.V16, d, nil)
	for i, tt := range tests {
		id := fmt.Sprint(i)
		writeRaw(t, raw, `[2,"`+id+`","`+tt.action+`",{}]`)
		f := readFrame(t, raw, ocpp.V16)
		if f.Type != ocppj.CallError || f.ID != id || string(f.ErrorCode) != tt.code || f.Description != tt.desc {
			t.Errorf("%s: got %+v, want %s %q", tt.action, f, tt.code, tt.desc)
		}
	}
}

func TestNoDispatcher(t *testing.T) {
	checkNoLeak(t)
	_, raw := rawPeer(t, ocpp.V201, nil, nil)
	writeRaw(t, raw, `[2,"x","Reset",{"type":"Immediate"}]`)
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallError || f.ErrorCode != ocpp.NotImplemented {
		t.Fatalf("got %+v", f)
	}
}

func TestMalformedFrames(t *testing.T) {
	checkNoLeak(t)
	for _, tt := range []struct {
		v    ocpp.Version
		code ocpp.ErrorCode
	}{{ocpp.V16, ocpp.FormationViolation}, {ocpp.V201, ocpp.RpcFrameworkError}} {
		m := &countingMetrics{}
		_, raw := rawPeer(t, tt.v, ocpp.DispatcherFunc(echo), func(c *Config) { c.Metrics = m })
		// Frames whose message ID cannot be read: answered with ID "-1" in
		// OCPP 2.x (Part 4 §4.2.3), dropped in 1.6, which defines no ID for
		// them.
		writeRaw(t, raw, `nonsense`)
		writeRaw(t, raw, `[2,7,"Heartbeat",{}]`)
		if tt.v != ocpp.V16 {
			for range 2 {
				if f := readFrame(t, raw, tt.v); f.Type != ocppj.CallError || f.ID != "-1" || f.ErrorCode != tt.code {
					t.Fatalf("%s: got %+v, want CALLERROR %s with ID -1", tt.v, f, tt.code)
				}
			}
		}
		// Dropped: an unknown message type (errata 1161, 1163), a type the
		// version lacks, a response to nothing.
		writeRaw(t, raw, `[9,"a","Heartbeat",{}]`)
		writeRaw(t, raw, `[6,"b","Heartbeat",{}]`)
		writeRaw(t, raw, `[3,"c",{}]`)
		// Answered: a CALL whose ID can be read.
		writeRaw(t, raw, `[2,"d","Heartbeat"]`)
		f := readFrame(t, raw, tt.v)
		if f.Type != ocppj.CallError || f.ID != "d" || f.ErrorCode != tt.code {
			t.Fatalf("%s: got %+v, want CALLERROR %s for d", tt.v, f, tt.code)
		}
		// The connection is still usable.
		writeRaw(t, raw, `[2,"e","Heartbeat",{}]`)
		if f := readFrame(t, raw, tt.v); f.Type != ocppj.CallResult || f.ID != "e" {
			t.Fatalf("%s: got %+v", tt.v, f)
		}
		if got := m.protocolErrors.Load(); got != 6 {
			t.Errorf("%s: protocol errors = %d, want 6", tt.v, got)
		}
	}
}

func TestCloseFailsPendingCall(t *testing.T) {
	checkNoLeak(t)
	a, b := transport.Pipe("ocpp1.6")
	s := newEndpoint(a, ocpp.V16, ocpp.ChargingStation, nil, nil)
	block := make(chan struct{})
	c := newEndpoint(b, ocpp.V16, ocpp.CSMS, ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		<-block
		return json.RawMessage(`{}`), nil
	}), nil)
	done := make(chan error, 1)
	go func() { done <- s.Call(context.Background(), "Heartbeat", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	_ = s.Close()
	if err := <-done; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("pending call: %v, want ErrClosed", err)
	}
	if err := s.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("call after close: %v", err)
	}
	close(block)
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("peer endpoint did not end")
	}
	if !errors.Is(c.Err(), ocpp.ErrConnectionLost) {
		t.Fatalf("peer Err = %v, want ErrConnectionLost", c.Err())
	}
	if s.Err() != ocpp.ErrClosed {
		t.Fatalf("Err = %v", s.Err())
	}
}

func TestCloseFromHandler(t *testing.T) {
	checkNoLeak(t)
	a, b := transport.Pipe("ocpp1.6")
	var self *Endpoint
	closed := make(chan struct{})
	self = newEndpoint(a, ocpp.V16, ocpp.CSMS, ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		_ = self.Close()
		close(closed)
		return json.RawMessage(`{}`), nil
	}), nil)
	other := newEndpoint(b, ocpp.V16, ocpp.ChargingStation, nil, nil)
	defer func() { _ = other.Close() }()
	go func() { _ = other.Call(context.Background(), "Heartbeat", nil, nil) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close from a handler blocked")
	}
	<-self.Done()
}

func TestWrongDirection(t *testing.T) {
	checkNoLeak(t)
	e, _ := rawPeer(t, ocpp.V16, nil, func(c *Config) {
		c.CanSend = func(action string) bool { return action != "RemoteStartTransaction" }
	})
	if err := e.Call(context.Background(), "RemoteStartTransaction", nil, nil); !errors.Is(err, ocpp.ErrWrongDirection) {
		t.Fatalf("err = %v", err)
	}
}

func TestInterceptorsAndObserver(t *testing.T) {
	checkNoLeak(t)
	var mu sync.Mutex
	var seen []string
	record := func(s string) { mu.Lock(); seen = append(seen, s); mu.Unlock() }
	icpt := func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
		record(fmt.Sprintf("%s %s %s %v", info.Role, info.Direction, info.Action, info.MessageID != ""))
		return next(ctx)
	}
	var frames atomic.Int64
	obs := func(info ocpp.ConnInfo, dir ocpp.Direction, frame []byte) { frames.Add(1) }
	a, b := transport.Pipe("ocpp1.6")
	s := newEndpoint(a, ocpp.V16, ocpp.ChargingStation, nil, func(c *Config) { c.CallInterceptor = icpt; c.FrameObserver = obs })
	c := newEndpoint(b, ocpp.V16, ocpp.CSMS, ocpp.DispatcherFunc(echo), func(c *Config) { c.HandlerInterceptor = icpt; c.FrameObserver = obs })
	defer func() { _ = s.Close() }()
	defer func() { _ = c.Close() }()
	if err := s.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"ChargingStation outgoing Heartbeat true", "CSMS incoming Heartbeat true"}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("interceptors saw %q, want %q", seen, want)
	}
	// The observer sees an outgoing frame after it is written, so the CSMS's
	// CALLRESULT may be observed after the station's Call has returned.
	deadline := time.Now().Add(5 * time.Second)
	for frames.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := frames.Load(); n != 4 {
		t.Errorf("observer saw %d frames, want 4", n)
	}
}

func TestNewUUID(t *testing.T) {
	seen := map[string]bool{}
	for range 1000 {
		id := NewUUID()
		if len(id) != 36 || id[14] != '4' || seen[id] {
			t.Fatalf("bad or repeated UUID %q", id)
		}
		seen[id] = true
	}
}

type countingMetrics struct {
	ocpp.NopMetrics
	protocolErrors atomic.Int64
}

func (m *countingMetrics) OnProtocolError(ocpp.ConnInfo, error) { m.protocolErrors.Add(1) }
