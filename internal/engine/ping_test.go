// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
)

// While the reader waits for room in the incoming queue it reads nothing, no
// pong either. Pings sent in that time are not counted as lost: the endpoint
// stays up for as long as the queue is full, everything queued is handled
// once the handler goes on, and pings are answered again when the reader
// reads. The connection answers a ping only while it is being read, as a
// WebSocket does.
func TestPingWhileReaderWaitsForRoom(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	conn.pongWhileReading = true
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	var handled atomic.Int64
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		<-release
		handled.Add(1)
		return json.RawMessage(`{}`), nil
	})
	e := newEndpoint(conn, ocpp.V201, ocpp.CSMS, d, func(c *Config) { c.PingInterval = 5 * time.Millisecond })
	t.Cleanup(func() { free(); _ = e.Close() })
	up := func(when string) {
		t.Helper()
		if err := e.Err(); err != nil {
			t.Fatalf("%s: the endpoint ended: %v", when, err)
		}
	}
	// One CALL in the handler, the queue full, and one more with the reader.
	n := cap(e.incoming) + 2
	for i := range n {
		conn.reads <- []byte(fmt.Sprintf(`[2,"%d","Heartbeat",{}]`, i))
	}
	eventually(t, "the reader to wait for room in the queue", func() bool {
		up("while the queue fills")
		return len(conn.reads) == 0 && len(e.incoming) == cap(e.incoming) && e.readerWaits.Load()%2 == 1
	})
	// Ten pings while the reader waits: none of them can be answered.
	sent := conn.pings.Load()
	eventually(t, "ten pings while the reader waits", func() bool { up("while the reader waits"); return conn.pings.Load() >= sent+10 })
	up("after ten pings without a pong")

	free()
	eventually(t, "every queued CALL to be handled", func() bool { up("while the queue empties"); return handled.Load() == int64(n) })
	// The reader reads again, and so do the pongs arrive.
	sent = conn.pings.Load()
	eventually(t, "pings after the reader went on", func() bool { up("after the queue emptied"); return conn.pings.Load() >= sent+3 })
	if e.readerWaits.Load()%2 != 0 {
		t.Error("the reader is still marked as waiting")
	}
}

// A peer that answers no ping while the reader reads is still lost, with
// frames arriving or not: only the reader's own wait excuses a ping.
func TestPingLostWhileReading(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	conn.noPong = true
	e := newEndpoint(conn, ocpp.V201, ocpp.CSMS, ocpp.DispatcherFunc(echo), func(c *Config) { c.PingInterval = 5 * time.Millisecond })
	t.Cleanup(func() { _ = e.Close() })
	conn.reads <- []byte(`[2,"1","Heartbeat",{}]`)
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a peer that answers no ping was not given up")
	}
	if e.readerWaits.Load() != 0 {
		t.Fatalf("the reader waited %d times with one CALL", e.readerWaits.Load()/2)
	}
}
