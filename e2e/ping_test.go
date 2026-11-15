// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// gatedSends holds every SEND until release is closed, then counts it.
type gatedSends struct {
	counter
	release chan struct{}
}

func (g *gatedSends) DispatchSend(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) error {
	<-g.release
	return g.counter.DispatchSend(ctx, p, action, payload)
}

// A CSMS that pings (WithPingInterval) and whose SEND handler is slower than
// a station's burst: the incoming queue fills, reading waits, and with it
// the pongs. The session is not given up for that. It stays up for ten ping
// intervals with the queue full, every SEND is delivered once the handler
// goes on, and the connection then carries CALLs in both directions. Over a
// real WebSocket, where a pong is read only while the connection is read.
func TestPingWhileIncomingQueueIsFull(t *testing.T) {
	noLeak(t)
	const sends = 40
	var logs syncBuffer
	recv := &gatedSends{release: make(chan struct{})}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V21, recv), server.WithoutAuthentication(),
		server.WithPingInterval(50*time.Millisecond),
		server.WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	var once sync.Once
	release := func() { once.Do(func() { close(recv.release) }) }
	t.Cleanup(release) // before the server's shutdown, which waits for the handler
	c := station(t, base+"PING", client.WithInsecureTransport(), client.WithHandler(ocpp.V21, ocpp.DispatcherFunc(echo)))
	connect(t, c)
	sess := waitStation(t, srv, "PING")
	up := func(when string) {
		t.Helper()
		if err := sess.Err(); err != nil {
			t.Fatalf("%s: the session ended: %v", when, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := range sends {
		if err := c.Send(ctx, "NotifyPeriodicEventStream", json.RawMessage(sendPayload)); err != nil {
			t.Fatalf("SEND %d: %v", i, err)
		}
	}
	// One SEND in the handler, 16 queued, one with the reader, the rest in
	// the socket: ten pings go out, and none can be answered.
	eventually(t, "ten pings while reading waits for the queue", func() bool {
		up("with the incoming queue full")
		return strings.Count(logs.String(), "pong not awaited") >= 10
	})
	if n := recv.sends.Load(); n != 0 {
		t.Fatalf("%d SENDs were handled before the handler was released", n)
	}

	release()
	eventually(t, "every SEND to be delivered", func() bool {
		up("while the queue empties")
		return recv.sends.Load() == sends
	})
	if err := c.Call(ctx, "Heartbeat", nil, nil); err != nil {
		t.Fatalf("a CALL from the station afterwards: %v", err)
	}
	if err := sess.Call(ctx, "Reset", nil, nil); err != nil {
		t.Fatalf("a CALL to the station afterwards: %v", err)
	}
	up("at the end")
	if strings.Contains(logs.String(), "no pong") {
		t.Errorf("a ping was counted as lost:\n%s", logs.String())
	}
}
