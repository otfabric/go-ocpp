// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/transport"
)

// reusingConn is a connection that reads every message into the same
// buffer, as a transport that reuses its read buffer would: what the engine
// keeps of a message after the next read must be a copy.
type reusingConn struct {
	*fakeConn
	buf []byte
}

func (c *reusingConn) ReadMessage(ctx context.Context) ([]byte, error) {
	m, err := c.fakeConn.ReadMessage(ctx)
	if err != nil {
		return nil, err
	}
	for i := range c.buf {
		c.buf[i] = '#' // the earlier message is gone
	}
	c.buf = append(c.buf[:0], m...)
	return c.buf, nil
}

// A dispatcher owns the payload it is given: it may keep it. Frames read
// later, into the same transport buffer, do not change it, and neither does
// what the dispatcher does with the payload change frames already queued.
func TestDispatcherOwnsItsPayload(t *testing.T) {
	checkNoLeak(t)
	var mu sync.Mutex
	var kept []json.RawMessage
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		kept = append(kept, payload)
		mu.Unlock()
		return payload, nil // the response is the payload itself
	})
	conn := &reusingConn{fakeConn: newFakeConn(), buf: make([]byte, 0, 256)}
	e := newEndpoint(conn, ocpp.V201, ocpp.CSMS, d, nil)
	t.Cleanup(func() { _ = e.Close() })
	const n = 5
	for i := range n {
		conn.reads <- []byte(fmt.Sprintf(`[2,"%d","DataTransfer",{"vendorId":"V","data":"payload-%d"}]`, i, i))
		eventually(t, "the response", func() bool { return answered(conn.fakeConn, fmt.Sprint(i)) })
	}
	mu.Lock()
	defer mu.Unlock()
	for i, p := range kept {
		if want := fmt.Sprintf(`{"vendorId":"V","data":"payload-%d"}`, i); string(p) != want {
			t.Errorf("the payload kept from CALL %d is now %s, want %s", i, p, want)
		}
		if overlap(p, conn.buf) {
			t.Errorf("the payload of CALL %d shares memory with the transport's buffer", i)
		}
	}
	// The dispatcher changes what it kept: the responses already written
	// are not that memory.
	for _, p := range kept {
		for i := range p {
			p[i] = '!'
		}
	}
	conn.mu.Lock()
	defer conn.mu.Unlock()
	for i, w := range conn.written {
		if want := fmt.Sprintf(`[3,"%d",{"vendorId":"V","data":"payload-%d"}]`, i, i); string(w) != want {
			t.Errorf("response %d is %s, want %s", i, w, want)
		}
	}
}

// overlap reports whether a and b share memory.
func overlap(a, b []byte) bool {
	if cap(a) == 0 || cap(b) == 0 {
		return false
	}
	a, b = a[:cap(a)], b[:cap(b)]
	for i := range a {
		if &a[i] == &b[0] {
			return true
		}
	}
	for i := range b {
		if &b[i] == &a[0] {
			return true
		}
	}
	return false
}

// A request is read while Call runs and not kept: a caller that reuses the
// memory of its json.RawMessage once Call has returned does not change the
// frame, also when the frame is still being written at that time. The same
// holds for Send.
func TestCallerMayReuseItsRequest(t *testing.T) {
	checkNoLeak(t)
	for _, send := range []bool{false, true} {
		conn := newFakeConn()
		conn.blockW = make(chan struct{}) // a write that does not complete yet
		e := newEndpoint(conn, ocpp.V21, ocpp.ChargingStation, nil, nil)
		t.Cleanup(func() { _ = e.Close() })
		const original = `{"vendorId":"V","data":"original"}`
		req := json.RawMessage(original)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		if send {
			go func() { done <- e.Send(ctx, "NotifyPeriodicEventStream", req) }()
		} else {
			go func() { done <- e.Call(ctx, "DataTransfer", req, nil) }()
		}
		// The writer has taken the frame and is writing it.
		eventually(t, "the frame to be with the writer", func() bool { return conn.writes.Load() == 1 })
		cancel() // the Call returns; its CALL stays with the writer
		if err := <-done; send && err != nil || !send && !errors.Is(err, context.Canceled) {
			t.Fatalf("send %v: returned %v", send, err)
		}
		copy(req, bytes.Repeat([]byte("?"), len(req))) // the caller reuses its memory
		close(conn.blockW)                             // and now the write completes
		eventually(t, "the frame to be written", func() bool {
			conn.mu.Lock()
			defer conn.mu.Unlock()
			return len(conn.written) == 1
		})
		conn.mu.Lock()
		w := string(conn.written[0])
		conn.mu.Unlock()
		if !bytes.Contains([]byte(w), []byte(original)) {
			t.Errorf("send %v: the frame written is %s, want the payload %s", send, w, original)
		}
	}
}

// A *json.RawMessage response is the caller's: it receives a copy of the
// payload, which later frames, read into the same transport buffer, do not
// change, and the caller's own slice is reused when it is large enough.
func TestResponseIsACopy(t *testing.T) {
	checkNoLeak(t)
	conn := &reusingConn{fakeConn: newFakeConn(), buf: make([]byte, 0, 256)}
	e := newEndpoint(conn, ocpp.V16, ocpp.ChargingStation, nil, func(c *Config) { c.NewMessageID = sequence() })
	t.Cleanup(func() { _ = e.Close() })
	var responses [3]json.RawMessage
	own := make(json.RawMessage, 0, 64)
	responses[2] = own // a slice the caller brought
	for i := range responses {
		done := make(chan error, 1)
		go func() { done <- e.Call(context.Background(), "GetConfiguration", nil, &responses[i]) }()
		eventually(t, "the CALL to be written", func() bool { return answered(conn.fakeConn, fmt.Sprintf("m%d", i+1)) })
		conn.reads <- []byte(fmt.Sprintf(`[3,"m%d",{"n":%d}]`, i+1, i))
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range responses {
		if want := fmt.Sprintf(`{"n":%d}`, i); string(r) != want {
			t.Errorf("response %d is now %s, want %s", i, r, want)
		}
		if overlap(r, conn.buf) {
			t.Errorf("response %d shares memory with the transport's buffer", i)
		}
	}
	if !overlap(responses[2], own) {
		t.Error("a response slice with room was not reused")
	}
}

// sequence returns message IDs m1, m2, ….
func sequence() func() string {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("m%d", n)
	}
}

var _ transport.Conn = (*reusingConn)(nil)
