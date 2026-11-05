// SPDX-License-Identifier: MIT

package engine

import (
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
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
