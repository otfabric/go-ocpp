// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// While draining, a malformed CALL is not answered, any more than a valid
// one: a peer that sends them and does not read the CALLERRORs would
// otherwise keep a frame unwritten, and the endpoint from ever being Idle
// (Server.Shutdown and Session.Shutdown wait for Idle).
func TestDrainDoesNotAnswerMalformedCalls(t *testing.T) {
	checkNoLeak(t)
	for _, v := range []ocpp.Version{ocpp.V16, ocpp.V201} {
		conn := newFakeConn()
		conn.blockW = make(chan struct{}) // a peer that does not read
		m := &countingMetrics{}
		e := fakeEndpoint(t, conn, func(c *Config) { c.Info.Version = v; c.Metrics = m; c.WriteTimeout = time.Minute })
		if !e.Idle() {
			t.Fatal("not idle at the start")
		}
		e.Drain()
		conn.reads <- []byte(`[2,"m","Heartbeat"]`) // a CALL without payload: answered with a CALLERROR when not draining
		conn.reads <- []byte(`[2,"this message ID is far longer than thirty-six characters","Heartbeat",{}]`)
		conn.reads <- []byte(`[3,"none",{}]`) // a response to nothing: once it is counted, the frames before are handled
		deadline := time.Now().Add(5 * time.Second)
		for m.protocolErrors.Load() < 3 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: frames not read", v)
			}
			time.Sleep(time.Millisecond)
		}
		if !e.Idle() {
			t.Fatalf("%s: a malformed CALL received while draining was answered: the endpoint never becomes idle", v)
		}
		conn.mu.Lock()
		n := len(conn.written)
		conn.mu.Unlock()
		if n != 0 {
			t.Fatalf("%s: %d frames written", v, n)
		}
		_ = e.Close()
	}
}

// sendGate blocks every SEND until release is closed, and records the
// context of each.
type sendGate struct {
	sendCounter
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	ctxs    []context.Context
}

func (g *sendGate) DispatchSend(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) error {
	g.mu.Lock()
	g.ctxs = append(g.ctxs, ctx)
	g.mu.Unlock()
	g.entered <- struct{}{}
	<-g.release
	return g.sendCounter.DispatchSend(ctx, p, action, payload)
}

// A SEND that started before Drain is in flight; SENDs dispatched while
// draining are delivered but do not keep the endpoint from being idle, so that
// a peer that keeps sending cannot keep a drain from ending. Close does not
// cancel them, and the SENDs received before it are delivered after it.
func TestDrainNotHeldBySends(t *testing.T) {
	checkNoLeak(t)
	g := &sendGate{entered: make(chan struct{}), release: make(chan struct{})}
	e, r := rawPeer(t, ocpp.V21, g, nil)

	writeRaw(t, r, sendFrame("before"))
	<-g.entered
	e.Drain()
	if e.Idle() {
		t.Fatal("idle while a SEND that started before Drain is handled")
	}
	g.release <- struct{}{}

	writeRaw(t, r, sendFrame("during1"))
	<-g.entered
	if !e.Idle() {
		t.Fatal("a SEND dispatched while draining keeps the endpoint busy")
	}
	writeRaw(t, r, sendFrame("during2"))
	writeRaw(t, r, sendFrame("during3"))
	deadline := time.Now().Add(5 * time.Second)
	for e.handling.Load() != 3 { // read and queued behind the running handler
		if time.Now().After(deadline) {
			t.Fatal("SENDs not read")
		}
		time.Sleep(time.Millisecond)
	}
	if !e.Idle() {
		t.Fatal("SENDs queued while draining keep the endpoint busy")
	}
	_ = e.Close()
	g.mu.Lock()
	running := g.ctxs[len(g.ctxs)-1]
	g.mu.Unlock()
	if running.Err() != nil {
		t.Fatal("Close cancelled the context of a SEND dispatched while draining")
	}
	close(g.release)
	go func() {
		for range g.entered {
		}
	}()
	<-e.Done()
	close(g.entered)
	if n := g.n.Load(); n != 4 {
		t.Fatalf("%d of 4 SENDs delivered", n)
	}
}

// In OCPP 2.x, a frame whose message type is not an integer and whose message
// ID is longer than 36 characters is answered with ID "-1": the ID cannot be
// echoed.
func TestLongIDWithBadTypeAnswered(t *testing.T) {
	checkNoLeak(t)
	_, r := rawPeer(t, ocpp.V201, ocpp.DispatcherFunc(echo), nil)
	writeRaw(t, r, `["2","`+strings.Repeat("x", 37)+`","Heartbeat",{}]`)
	if f := readFrame(t, r, ocpp.V201); f.Type != ocppj.CallError || f.ID != "-1" || f.ErrorCode != ocpp.RpcFrameworkError {
		t.Fatalf("got %+v", f)
	}
}

// A CALL whose call ended before the writer took it off its queue (no
// response within the timeout, or its context ended) is not written; the slot
// is free for the next CALL.
func TestUnsentCallNotWritten(t *testing.T) {
	for _, viaCtx := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%v", viaCtx), func(t *testing.T) {
			checkNoLeak(t)
			conn := newFakeConn()
			conn.blockW = make(chan struct{}) // the writer is stuck on the first frame
			e := fakeEndpoint(t, conn, func(c *Config) {
				c.WriteTimeout = time.Minute
				c.ResponseTimeout = time.Minute
				if !viaCtx {
					c.ResponseTimeout = 20 * time.Millisecond
				}
			})
			conn.reads <- []byte(`[2,"in","Heartbeat",{}]`) // answered with a CALLERROR: no dispatcher
			deadline := time.Now().Add(5 * time.Second)
			for e.unwritten.Load() != 1 || e.handling.Load() != 0 {
				if time.Now().After(deadline) {
					t.Fatal("the answer was not queued")
				}
				time.Sleep(time.Millisecond)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- e.Call(ctx, "Heartbeat", nil, nil) }()
			for e.unwritten.Load() != 2 {
				if time.Now().After(deadline) {
					t.Fatal("the CALL was not queued")
				}
				time.Sleep(time.Millisecond)
			}
			if viaCtx {
				cancel()
			}
			err := <-done
			if viaCtx && !errors.Is(err, context.Canceled) || !viaCtx && !errors.Is(err, ocpp.ErrTimeout) {
				t.Fatalf("Call: %v", err)
			}
			close(conn.blockW)
			for !e.Idle() {
				if time.Now().After(deadline) {
					t.Fatal("not idle")
				}
				time.Sleep(time.Millisecond)
			}
			// The slot is free: the next CALL goes out and is answered.
			next := make(chan error, 1)
			go func() { next <- e.Call(context.Background(), "StatusNotification", nil, nil) }()
			var written [][]byte
			for {
				conn.mu.Lock()
				written = append([][]byte(nil), conn.written...)
				conn.mu.Unlock()
				if len(written) == 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("written %q", written)
				}
				time.Sleep(time.Millisecond)
			}
			f, err := ocppj.Decode(written[1], ocpp.V16, ocppj.Limits{})
			if err != nil || f.Type != ocppj.Call || f.Action != "StatusNotification" {
				t.Fatalf("written %q", written)
			}
			conn.reads <- []byte(`[3,"` + f.ID + `",{}]`)
			if err := <-next; err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(written[0]), `"in"`) {
				t.Fatalf("written %q", written)
			}
		})
	}
}

// unparsable fails to decode with a syntax error, as a response type with its
// own UnmarshalJSON can.
type unparsable struct{}

func (*unparsable) UnmarshalJSON([]byte) error {
	var v any
	return json.Unmarshal([]byte(`{`), &v)
}

// A response that fails to decode with a syntax error is refused with
// FormatViolation, and the decoding error's text is not sent.
func TestCallResultErrorSyntax(t *testing.T) {
	checkNoLeak(t)
	e, r := rawPeer(t, ocpp.V21, nil, nil)
	done := make(chan error, 1)
	go func() { done <- e.Call(context.Background(), "GetThing", nil, &unparsable{}) }()
	f := readFrame(t, r, ocpp.V21)
	writeRaw(t, r, `[3,"`+f.ID+`",{}]`)
	if err := <-done; !errors.Is(err, ocpp.ErrProtocol) {
		t.Fatal(err)
	}
	g := readFrame(t, r, ocpp.V21)
	if g.Type != ocppj.CallResultError || g.ErrorCode != ocpp.FormatViolation || strings.Contains(g.Description, "unexpected end") {
		t.Fatalf("got %+v", g)
	}
}
