// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// expectedFailure reports whether err is one of the errors a call may end
// with when the connection closes or breaks under it.
func expectedFailure(err error) bool {
	for _, e := range []error{ocpp.ErrClosed, ocpp.ErrConnectionLost, ocpp.ErrNotConnected, ocpp.ErrTimeout,
		ocpp.ErrBusy, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// serialChecker is a dispatcher that fails the test when two CALLs of the
// same station are handled at the same time, which T6 rules out: a peer
// sends its next CALL only once the previous one is answered.
type serialChecker struct {
	t        *testing.T
	inFlight sync.Map // station ID -> *atomic.Int32
	handled  atomic.Int64
	hold     time.Duration
}

func (s *serialChecker) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	n, _ := s.inFlight.LoadOrStore(p.ID(), new(atomic.Int32))
	if v := n.(*atomic.Int32).Add(1); v != 1 {
		s.t.Errorf("%s: %d CALLs handled at once", p.ID(), v)
	}
	defer n.(*atomic.Int32).Add(-1)
	time.Sleep(s.hold)
	s.handled.Add(1)
	return echo(ctx, p, action, payload)
}

// Many goroutines call through one connection in both directions at once,
// for every version: every call gets its own answer, and neither side ever
// handles two CALLs of the station at the same time (T6).
func TestParallelCalls(t *testing.T) {
	for _, v := range []ocpp.Version{ocpp.V16, ocpp.V201, ocpp.V21} {
		t.Run(v.String(), func(t *testing.T) {
			t.Parallel()
			noLeak(t)
			const callers, calls = 16, 20
			csmsSide := &serialChecker{t: t, hold: 100 * time.Microsecond}
			stationSide := &serialChecker{t: t, hold: 100 * time.Microsecond}
			srv, base := csms(t, nil, server.WithHandler(v, csmsSide), server.WithoutAuthentication(), server.WithMaxQueuedCalls(callers*2))
			id := "P" + v.String()
			c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(v), client.WithHandler(v, stationSide),
				client.WithMaxQueuedCalls(callers*2))
			connect(t, c)
			sess := waitStation(t, srv, id)

			var wg sync.WaitGroup
			for g := range callers {
				for _, caller := range []ocpp.Caller{c, sess} {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := range calls {
							var got echoed
							want := fmt.Sprintf(`{"g":%d,"i":%d}`, g, i)
							if err := caller.Call(context.Background(), "DataTransfer", json.RawMessage(want), &got); err != nil {
								t.Errorf("call: %v", err)
								return
							}
							if got.Station != id || string(got.Echo) != want {
								t.Errorf("sent %s, answered %+v", want, got)
							}
						}
					}()
				}
			}
			wg.Wait()
			if a, b := csmsSide.handled.Load(), stationSide.handled.Load(); a != callers*calls || b != callers*calls {
				t.Fatalf("handled %d by the CSMS and %d by the station, want %d each", a, b, callers*calls)
			}
		})
	}
}

// Calls in flight in both directions when the connection is closed, from
// either side, at a random moment: every call returns, with its answer or
// with one of the errors of a closed connection, and nothing leaks.
func TestCloseUnderLoad(t *testing.T) {
	for round := range 8 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			t.Parallel()
			noLeak(t)
			slow := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
				select {
				case <-time.After(time.Duration(rand.IntN(3)) * time.Millisecond):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return echo(ctx, p, action, payload)
			})
			v := []ocpp.Version{ocpp.V16, ocpp.V201, ocpp.V21}[round%3]
			srv, base := csms(t, nil, server.WithHandler(v, slow), server.WithoutAuthentication())
			id := fmt.Sprintf("C%d", round)
			c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(v), client.WithHandler(v, slow))
			connect(t, c)
			sess := waitStation(t, srv, id)

			var wg sync.WaitGroup
			for g := range 8 {
				for _, caller := range []ocpp.Caller{c, sess} {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := 0; ; i++ {
							ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							err := caller.Call(ctx, "Heartbeat", map[string]int{"g": g, "i": i}, nil)
							cancel()
							if err != nil {
								if !expectedFailure(err) {
									t.Errorf("unexpected error: %v", err)
								}
								return
							}
						}
					}()
				}
			}
			time.Sleep(time.Duration(5+rand.IntN(30)) * time.Millisecond)
			if round%2 == 0 {
				_ = c.Close()
			} else {
				_ = sess.Close()
				_ = c.Close()
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("calls did not return after Close")
			}
		})
	}
}

// Many connections with the same identity race each other. Whatever the
// interleaving, the server ends with at most one session for it, every
// replaced session ends with server.ErrReplaced, and nothing leaks.
func TestReplacementStorm(t *testing.T) {
	noLeak(t)
	var mu sync.Mutex
	var ended []error
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithSessionHandler(nil, func(s *server.Session, err error) {
			mu.Lock()
			ended = append(ended, err)
			mu.Unlock()
		}))
	const clients = 12
	var wg sync.WaitGroup
	cs := make([]*client.Client, clients)
	for i := range clients {
		cs[i] = station(t, base+"STORM", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := cs[i].Connect(ctx); err != nil {
				t.Errorf("connect %d: %v", i, err)
				return
			}
			// A call on a connection that may already be replaced.
			if err := cs[i].Call(ctx, "Heartbeat", nil, nil); err != nil && !expectedFailure(err) {
				t.Errorf("call %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	eventually(t, "replaced sessions ended", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(ended) >= clients-1
	})
	if n := len(srv.Stations()); n > 1 {
		t.Fatalf("%d sessions for one identity", n)
	}
	mu.Lock()
	for _, err := range ended {
		if !errors.Is(err, server.ErrReplaced) {
			t.Errorf("a replaced session ended with %v", err)
		}
	}
	mu.Unlock()
	// The surviving session works.
	// The surviving session works: the clients have no handler, so it is
	// answered with NotImplemented.
	if s, ok := srv.Station("STORM"); ok {
		var ce *ocpp.CallError
		if err := s.Call(context.Background(), "Reset", nil, nil); !errors.As(err, &ce) && !expectedFailure(err) {
			t.Fatalf("call on the surviving session: %v", err)
		}
	}
}

// Server shutdown while many stations call in both directions: Shutdown
// returns, every call returns, and nothing leaks.
func TestShutdownUnderLoad(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	var wg sync.WaitGroup
	for i := range 20 {
		id := fmt.Sprintf("S%02d", i)
		c := station(t, base+id, client.WithInsecureTransport(), client.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)))
		connect(t, c)
		sess := waitStation(t, srv, id)
		for _, caller := range []ocpp.Caller{c, sess} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					if err := caller.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
						if !expectedFailure(err) {
							t.Errorf("unexpected error: %v", err)
						}
						return
					}
				}
			}()
		}
	}
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("calls did not return after Shutdown")
	}
}

// SENDs from many goroutines while CALLs are under way in both directions
// (OCPP 2.1): every SEND arrives once, and no CALL is affected.
func TestParallelSends(t *testing.T) {
	noLeak(t)
	var received sync.Map
	var count atomic.Int64
	d := &sendRecorder{echo: ocpp.DispatcherFunc(echo), onSend: func(payload json.RawMessage) {
		if _, dup := received.LoadOrStore(string(payload), true); dup {
			t.Errorf("SEND %s arrived twice", payload)
		}
		count.Add(1)
	}}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V21, d), server.WithoutAuthentication())
	c := station(t, base+"SENDS", client.WithInsecureTransport(), client.WithVersions(ocpp.V21), client.WithHandler(ocpp.V21, ocpp.DispatcherFunc(echo)))
	connect(t, c)
	sess := waitStation(t, srv, "SENDS")
	const senders, sends = 8, 50
	var wg sync.WaitGroup
	for g := range senders {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range sends {
				if err := c.Send(context.Background(), "NotifyPeriodicEventStream", map[string]int{"g": g, "i": i}); err != nil {
					t.Errorf("send: %v", err)
					return
				}
			}
		}()
		go func() {
			defer wg.Done()
			for range 10 {
				if err := sess.Call(context.Background(), "GetVariables", nil, nil); err != nil {
					t.Errorf("call: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	eventually(t, "every SEND received", func() bool { return count.Load() == senders*sends })
}

// sendRecorder answers CALLs with echo and records SENDs.
type sendRecorder struct {
	echo   ocpp.Dispatcher
	onSend func(json.RawMessage)
}

func (s *sendRecorder) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	return s.echo.Dispatch(ctx, p, action, payload)
}

func (s *sendRecorder) DispatchSend(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) error {
	s.onSend(payload)
	return nil
}

// Under RejectNew, connections of one identity that race in are all
// refused with 409 but one, even before the first is registered; none
// replaces another, and none is refused for the station limit.
func TestRejectNewRace(t *testing.T) {
	noLeak(t)
	var replaced atomic.Int32
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithReplacePolicy(server.RejectNew), server.WithMaxStations(5),
		server.WithSessionHandler(nil, func(s *server.Session, err error) {
			if errors.Is(err, server.ErrReplaced) {
				replaced.Add(1)
			}
		}))
	const n = 12
	var wg sync.WaitGroup
	var ok, conflict atomic.Int32
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := station(t, base+"RACE", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
			var he *client.HandshakeError
			switch err := c.Connect(context.Background()); {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &he) && he.StatusCode == http.StatusConflict:
				conflict.Add(1)
			default:
				t.Errorf("connect: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || conflict.Load() != n-1 || replaced.Load() != 0 || len(srv.Stations()) != 1 {
		t.Fatalf("%d admitted, %d conflicts, %d replaced, %d sessions", ok.Load(), conflict.Load(), replaced.Load(), len(srv.Stations()))
	}
}
