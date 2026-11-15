// SPDX-License-Identifier: MIT

package e2e

// The rows of the lifecycle contract matrix (TESTING.md) that are about
// Close itself, for the three long-lived types of the API: L1 (Close
// twice), L2 (Close from several goroutines) and L10 (Close from a
// handler). Every wait is for an event.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

const closers = 8

// closeAtOnce calls closeFn from closers goroutines that start together, and
// fails the test unless every call returns nil.
func closeAtOnce(t *testing.T, what string, closeFn func() error) {
	t.Helper()
	start := make(chan struct{})
	results := make(chan error, closers)
	for range closers {
		go func() {
			<-start // a barrier: every goroutine calls Close at once
			results <- closeFn()
		}()
	}
	close(start)
	for range closers {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("%s: Close: %v, want nil", what, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: a Close did not return", what)
		}
	}
}

// held is a dispatcher that holds every CALL until release is closed, and
// says when one has arrived.
type held struct {
	arrived chan string
	release chan struct{}
	once    sync.Once
}

func newHeld(t *testing.T) *held {
	h := &held{arrived: make(chan string, 16), release: make(chan struct{})}
	t.Cleanup(h.free)
	return h
}

func (h *held) free() { h.once.Do(func() { close(h.release) }) }

func (h *held) Dispatch(ctx context.Context, p ocpp.Peer, action string, _ json.RawMessage) (json.RawMessage, error) {
	h.arrived <- action
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return json.RawMessage(`{}`), nil
}

// recvErr waits for an error on ch.
func recvErr(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// L1 and L2 for client.Client: Close from several goroutines at once, and
// again afterwards, returns nil every time; the client is closed once (one
// Closed state, one OnDisconnect), a CALL in flight fails with ErrClosed,
// and so does everything after.
func TestClientCloseTwiceAndConcurrently(t *testing.T) {
	noLeak(t)
	for round := range 5 {
		h := newHeld(t)
		srv, base := csms(t, nil, server.WithHandler(ocpp.V201, h), server.WithoutAuthentication())
		m, states := &recorder{}, &stateLog{}
		id := fmt.Sprintf("L2C%d", round)
		c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V201),
			client.WithMetrics(m), client.WithStateHandler(states.handler),
			client.WithReconnect(client.Reconnect{WaitMinimum: time.Millisecond}))
		connect(t, c)
		waitStation(t, srv, id)
		pending := make(chan error, 1)
		go func() { pending <- c.Call(context.Background(), "Heartbeat", nil, nil) }()
		<-h.arrived // the CALL is with the CSMS's handler: it is in flight

		closeAtOnce(t, "client", c.Close)
		if err := recvErr(t, pending, "the CALL in flight"); !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: the CALL in flight: %v, want ErrClosed", round, err)
		}
		// One of the calls did the closing; it has returned, so it is done.
		if s := c.State(); s != ocpp.Closed {
			t.Fatalf("round %d: state %s after every Close returned", round, s)
		}
		for i := range 2 {
			if err := c.Close(); err != nil {
				t.Fatalf("round %d: Close %d after the others: %v", round, i, err)
			}
		}
		if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: Connect after Close: %v, want ErrClosed", round, err)
		}
		if err := c.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: Call after Close: %v, want ErrClosed", round, err)
		}
		if err := c.Send(context.Background(), "NotifyPeriodicEventStream", nil); !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: Send after Close: %v, want ErrClosed", round, err)
		}
		if got := states.String(); got != "[Connecting Connected Closed]" {
			t.Fatalf("round %d: states %s, want one Closed after Connected", round, got)
		}
		if n := m.disconnects.Load(); n != 1 {
			t.Fatalf("round %d: OnDisconnect called %d times, want once", round, n)
		}
		h.free()
	}
}

// L1 for a client that never connected: Close, twice, returns nil, and
// Connect then fails with ErrClosed.
func TestClientCloseBeforeConnect(t *testing.T) {
	noLeak(t)
	states := &stateLog{}
	c := station(t, "ws://127.0.0.1:1/ocpp/NEVER", client.WithInsecureTransport(), client.WithVersions(ocpp.V16), client.WithStateHandler(states.handler))
	closeAtOnce(t, "a client that never connected", c.Close)
	if err := c.Close(); err != nil {
		t.Fatalf("Close again: %v", err)
	}
	if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("Connect after Close: %v, want ErrClosed", err)
	}
	if got := states.String(); got != "[Closed]" || c.State() != ocpp.Closed {
		t.Fatalf("states %s, state %s", got, c.State())
	}
}

// L1 and L2 for server.Session: Close from several goroutines at once, and
// again afterwards, returns nil every time, each call after the session's
// reader and writer have returned; the session ends once (Done, Err, one
// call of the session handler, with nil for a local close), and a CALL in
// flight fails with ErrClosed.
func TestSessionCloseTwiceAndConcurrently(t *testing.T) {
	noLeak(t)
	for round := range 5 {
		var ended atomic.Int64
		endedWith := make(chan error, 4)
		srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
			server.WithSessionHandler(nil, func(_ *server.Session, err error) { ended.Add(1); endedWith <- err }))
		h := newHeld(t)
		id := fmt.Sprintf("L2S%d", round)
		c := station(t, base+id, client.WithInsecureTransport(), client.WithHandler(ocpp.V201, h))
		connect(t, c)
		sess := waitStation(t, srv, id)
		pending := make(chan error, 1)
		go func() { pending <- sess.Call(context.Background(), "Reset", nil, nil) }()
		<-h.arrived // the CALL is with the station's handler

		closeAtOnce(t, "session", func() error {
			if err := sess.Close(); err != nil {
				return err
			}
			if sess.Err() == nil {
				return errors.New("Close returned while Err was nil")
			}
			return nil
		})
		select {
		case <-sess.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: Done not closed", round)
		}
		if err := sess.Err(); err != ocpp.ErrClosed {
			t.Fatalf("round %d: Err = %v, want ErrClosed", round, err)
		}
		if err := recvErr(t, pending, "the CALL in flight"); !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: the CALL in flight: %v, want ErrClosed", round, err)
		}
		if err := recvErr(t, endedWith, "the session handler"); err != nil {
			t.Fatalf("round %d: the session handler got %v, want nil for a local Close", round, err)
		}
		for i := range 2 {
			if err := sess.Close(); err != nil {
				t.Fatalf("round %d: Close %d after the others: %v", round, i, err)
			}
		}
		if err := sess.Call(context.Background(), "Reset", nil, nil); !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: Call after Close: %v, want ErrClosed", round, err)
		}
		eventually(t, "the session to be unregistered", func() bool { _, ok := srv.Station(id); return !ok })
		if n := ended.Load(); n != 1 {
			t.Fatalf("round %d: the session handler was called %d times, want once", round, n)
		}
		h.free()
	}
}

// L1 and L2 for server.Server: Close from several goroutines at once, and
// again afterwards, returns nil every time. Close does not wait for the
// sessions: each ends with ErrClosed, is reported once, and Shutdown, which
// does wait, then returns nil. Serve returns ErrClosed, and a station that
// connects afterwards is refused.
func TestServerCloseTwiceAndConcurrently(t *testing.T) {
	noLeak(t)
	const stations = 3
	var ended atomic.Int64
	srv, err := server.New(server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithSessionHandler(nil, func(*server.Session, error) { ended.Add(1) }))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	base := "ws://" + ln.Addr().String() + "/ocpp/"
	var sessions []*server.Session
	for i := range stations {
		id := fmt.Sprintf("L2SRV%d", i)
		c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
		connect(t, c)
		sessions = append(sessions, waitStation(t, srv, id))
	}

	closeAtOnce(t, "server", srv.Close)
	if err := recvErr(t, served, "Serve to return"); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("Serve returned %v, want ErrClosed", err)
	}
	for i, sess := range sessions {
		select {
		case <-sess.Done():
		case <-time.After(10 * time.Second):
			t.Fatalf("session %d did not end", i)
		}
		if err := sess.Err(); err != ocpp.ErrClosed {
			t.Fatalf("session %d: Err = %v, want ErrClosed", i, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown after Close: %v", err)
	}
	if n := ended.Load(); n != stations {
		t.Fatalf("the session handler was called %d times, want %d", n, stations)
	}
	for i := range 2 {
		if err := srv.Close(); err != nil {
			t.Fatalf("Close %d after the others: %v", i, err)
		}
	}
	if err := srv.Serve(ln); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("Serve after Close: %v, want ErrClosed", err)
	}
	late := station(t, base+"LATE", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	if err := late.Connect(ctx); !errors.Is(err, ocpp.ErrConnectFailed) {
		t.Fatalf("Connect after the server closed: %v, want ErrConnectFailed", err)
	}
	if n := len(srv.Stations()); n != 0 {
		t.Fatalf("%d sessions after Close", n)
	}
}

// L10 for client.Client: Close called from a handler returns, without
// waiting for that handler; the client is then closed, and the CSMS, whose
// CALL gets no answer, sees a normal closure.
func TestClientCloseFromHandler(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	var c *client.Client
	closed := make(chan error, 1)
	c = station(t, base+"L10C", client.WithInsecureTransport(),
		client.WithHandler(ocpp.V16, ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
			closed <- c.Close()
			return json.RawMessage(`{}`), nil
		})))
	connect(t, c)
	sess := waitStation(t, srv, "L10C")
	callErr := make(chan error, 1)
	go func() { callErr <- sess.Call(context.Background(), "Reset", json.RawMessage(`{"type":"Soft"}`), nil) }()
	if err := recvErr(t, closed, "Close in the handler"); err != nil {
		t.Fatalf("Close from the handler: %v", err)
	}
	err := recvErr(t, callErr, "the CSMS's CALL")
	var ce *ocpp.CloseError
	if !errors.Is(err, ocpp.ErrConnectionLost) || !errors.As(err, &ce) || !ce.Normal() {
		t.Fatalf("the CSMS's CALL: %v, want a lost connection that the station closed normally", err)
	}
	eventually(t, "the client to be closed", func() bool { return c.State() == ocpp.Closed })
	if err := c.Close(); err != nil {
		t.Fatalf("Close again: %v", err)
	}
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("Call after Close: %v", err)
	}
}

// L10 for server.Session: Close called from a handler of that session
// returns, without waiting for the handler; the session ends with
// ErrClosed, and the station, whose CALL gets no answer, sees a normal
// closure.
func TestSessionCloseFromHandler(t *testing.T) {
	noLeak(t)
	closed := make(chan error, 1)
	sessions := make(chan *server.Session, 1)
	_, base := csms(t, nil, server.WithoutAuthentication(),
		server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(func(_ context.Context, p ocpp.Peer, _ string, _ json.RawMessage) (json.RawMessage, error) {
			sess := p.(*server.Session)
			closed <- sess.Close()
			sessions <- sess
			return json.RawMessage(`{}`), nil
		})))
	c := station(t, base+"L10S", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	callErr := make(chan error, 1)
	go func() { callErr <- c.Call(context.Background(), "Heartbeat", nil, nil) }()
	if err := recvErr(t, closed, "Close in the handler"); err != nil {
		t.Fatalf("Close from the handler: %v", err)
	}
	err := recvErr(t, callErr, "the station's CALL")
	var ce *ocpp.CloseError
	if !errors.Is(err, ocpp.ErrConnectionLost) || !errors.As(err, &ce) || !ce.Normal() {
		t.Fatalf("the station's CALL: %v, want a lost connection that the CSMS closed normally", err)
	}
	sess := <-sessions
	select {
	case <-sess.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the session did not end after its handler returned")
	}
	if sess.Err() != ocpp.ErrClosed {
		t.Fatalf("Err = %v, want ErrClosed", sess.Err())
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close again: %v", err)
	}
}
