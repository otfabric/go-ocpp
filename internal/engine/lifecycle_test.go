// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/transport"
)

// closeCounter counts the Close and Abort calls a connection gets.
type closeCounter struct {
	transport.Conn
	mu     sync.Mutex
	closes int
}

func (c *closeCounter) Close(code int, reason string) error {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	return c.Conn.Close(code, reason)
}

func (c *closeCounter) Abort() {
	c.mu.Lock()
	c.closes++
	c.mu.Unlock()
	c.Conn.Abort()
}

func (c *closeCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closes
}

// L1: Close twice. The second Close returns nil like the first and changes
// nothing: the connection is closed once, Done stays closed and Err keeps
// its cause. Also for an endpoint that was never started, and for one whose
// connection was lost before the first Close.
func TestCloseTwice(t *testing.T) {
	checkNoLeak(t)
	for name, prepare := range map[string]func(*closeCounter, *Endpoint, transport.Conn) error{
		"started": func(_ *closeCounter, e *Endpoint, _ transport.Conn) error {
			e.Start()
			return ocpp.ErrClosed
		},
		"never started": func(*closeCounter, *Endpoint, transport.Conn) error { return ocpp.ErrClosed },
		"connection lost first": func(_ *closeCounter, e *Endpoint, far transport.Conn) error {
			e.Start()
			_ = far.Close(transport.StatusGoingAway, "")
			<-e.Done()
			return ocpp.ErrConnectionLost
		},
	} {
		t.Run(name, func(t *testing.T) {
			near, far := transport.Pipe("ocpp2.0.1")
			conn := &closeCounter{Conn: near}
			e := New(conn, Config{Info: ocpp.ConnInfo{StationID: "CS1", Version: ocpp.V201, Role: ocpp.CSMS}})
			want := prepare(conn, e, far)
			for i := range 3 {
				if err := e.Close(); err != nil {
					t.Fatalf("Close %d: %v, want nil", i+1, err)
				}
				select {
				case <-e.Done():
				default:
					t.Fatalf("Done is not closed after Close %d", i+1)
				}
				if err := e.Err(); !errors.Is(err, want) {
					t.Fatalf("Err after Close %d: %v, want %v", i+1, err, want)
				}
				if n := conn.count(); n != 1 {
					t.Fatalf("the connection was closed %d times after Close %d, want once", n, i+1)
				}
			}
			if err := e.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, want) {
				t.Fatalf("a Call after Close: %v, want %v", err, want)
			}
		})
	}
}

// L2: goroutines that call Close at the same time all return nil, each
// after the endpoint's goroutines have returned; the connection is closed
// once, and a CALL that was pending fails with ErrClosed.
func TestCloseFromSeveralGoroutines(t *testing.T) {
	checkNoLeak(t)
	for round := range 20 {
		near, far := transport.Pipe("ocpp2.0.1")
		conn := &closeCounter{Conn: near}
		e := New(conn, Config{Info: ocpp.ConnInfo{StationID: "CS1", Version: ocpp.V201, Role: ocpp.CSMS}})
		e.Start()
		pending := make(chan error, 1)
		go func() { pending <- e.Call(context.Background(), "Reset", nil, nil) }()
		if _, err := far.ReadMessage(context.Background()); err != nil { // the CALL is written: it is pending
			t.Fatal(err)
		}

		const closers = 8
		start := make(chan struct{})
		results := make(chan error, closers)
		for range closers {
			go func() {
				<-start // a barrier: every goroutine calls Close at once
				err := e.Close()
				select {
				case <-e.Done():
				default:
					err = errors.New("Close returned before Done was closed")
				}
				results <- err
			}()
		}
		close(start)
		for range closers {
			select {
			case err := <-results:
				if err != nil {
					t.Fatalf("round %d: Close: %v", round, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("round %d: a Close did not return", round)
			}
		}
		if err := <-pending; !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("round %d: the pending CALL: %v, want ErrClosed", round, err)
		}
		if n := conn.count(); n != 1 {
			t.Fatalf("round %d: the connection was closed %d times, want once", round, n)
		}
		if e.Err() != ocpp.ErrClosed {
			t.Fatalf("round %d: Err = %v", round, e.Err())
		}
	}
}

// L8: a response belongs to the connection it arrived on. Every connection
// has an Endpoint of its own, with its own pending CALL: a response that
// arrives on an earlier connection completes nothing on a later one, also
// when both use the same message ID. It is dropped where it arrived, as a
// response to no CALL, and counted there.
func TestResponseBelongsToItsConnection(t *testing.T) {
	checkNoLeak(t)
	same := func() string { return "the-same-id" }
	earlier := &countingMetrics{}
	_, earlierRaw := rawPeer(t, ocpp.V201, nil, func(c *Config) { c.NewMessageID = same; c.Metrics = earlier })
	later, laterRaw := rawPeer(t, ocpp.V201, nil, func(c *Config) { c.NewMessageID = same })

	var resp json.RawMessage
	done := make(chan error, 1)
	go func() { done <- later.Call(context.Background(), "Heartbeat", nil, &resp) }()
	if f := readFrame(t, laterRaw, ocpp.V201); f.ID != "the-same-id" {
		t.Fatalf("message ID %q", f.ID)
	}
	// A response with that ID arrives on the earlier connection.
	writeRaw(t, earlierRaw, `[3,"the-same-id",{"from":"earlier"}]`)
	eventually(t, "the earlier connection to drop the response", func() bool { return earlier.protocolErrors.Load() == 1 })
	select {
	case err := <-done:
		t.Fatalf("the CALL on the later connection ended with %v and %s", err, resp)
	default:
	}
	later.mu.Lock()
	pending := later.pending != nil
	later.mu.Unlock()
	if !pending {
		t.Fatal("the CALL on the later connection is no longer pending")
	}
	// Its own response completes it.
	writeRaw(t, laterRaw, `[3,"the-same-id",{"from":"later"}]`)
	if err := <-done; err != nil || string(resp) != `{"from":"later"}` {
		t.Fatalf("the CALL on the later connection: %v, %s", err, resp)
	}
}
