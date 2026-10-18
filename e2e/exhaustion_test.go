// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// listening starts srv on its own listener with Serve, as an application
// that lets the server listen does (the csms helper mounts it on an
// httptest server instead, which has no handshake timeout).
func listening(t *testing.T, srv *server.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-served
	})
	return ln.Addr().String()
}

// rawStation dials a WebSocket without go-ocpp, for peers that misbehave.
func rawStation(t *testing.T, base, id, subprotocol string, compress bool) *websocket.Conn {
	t.Helper()
	opts := &websocket.DialOptions{Subprotocols: []string{subprotocol}}
	if compress {
		opts.CompressionMode = websocket.CompressionNoContextTakeover
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, base+id, opts)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c.SetReadLimit(64 << 20)
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

// healthy fails the test unless a new station can connect and call.
func healthy(t *testing.T, base, id string) {
	t.Helper()
	c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatalf("a new station cannot call: %v", err)
	}
	_ = c.Close()
}

// Connections that send part of the HTTP upgrade request and then nothing
// (slowloris) are closed after the handshake timeout, and do not keep a
// station from connecting meanwhile.
func TestSlowHandshake(t *testing.T) {
	noLeak(t)
	srv, err := server.New(server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithHandshakeTimeout(300*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	addr := listening(t, srv)
	const slow = 50
	var wg sync.WaitGroup
	for i := range slow {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "GET /ocpp/SLOW%d HTTP/1.1\r\nHost: x\r\n", i) // no end of headers
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			start := time.Now()
			_, err := io.Copy(io.Discard, conn) // returns when the server closes
			if err != nil || time.Since(start) > 3*time.Second {
				t.Errorf("slow connection not closed by the server: %v after %s", err, time.Since(start))
			}
		}()
	}
	healthy(t, "ws://"+addr+"/ocpp/", "FAST")
	wg.Wait()
}

// More stations than WithMaxStations connect at once: at most the limit
// get a session, the others are refused with ErrConnectFailed, and once
// the admitted ones leave, others are admitted again (no slot leaks).
func TestStationFlood(t *testing.T) {
	noLeak(t)
	const limit, stations = 10, 60
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithMaxStations(limit))
	var admitted, refused atomic.Int64
	var mu sync.Mutex
	var in []*client.Client
	var wg sync.WaitGroup
	for i := range stations {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := station(t, fmt.Sprintf("%sFLOOD%02d", base, i), client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			switch err := c.Connect(ctx); {
			case err == nil:
				admitted.Add(1)
				mu.Lock()
				in = append(in, c)
				mu.Unlock()
			case errors.Is(err, ocpp.ErrConnectFailed):
				refused.Add(1)
			default:
				t.Errorf("connect: %v", err)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() > limit || admitted.Load()+refused.Load() != stations || len(srv.Stations()) > limit {
		t.Fatalf("%d admitted, %d refused, %d sessions; limit %d", admitted.Load(), refused.Load(), len(srv.Stations()), limit)
	}
	for _, c := range in {
		_ = c.Close()
	}
	eventually(t, "sessions gone", func() bool { return len(srv.Stations()) == 0 })
	healthy(t, base, "AFTERFLOOD")
}

// A message over the size limit closes that connection only, in both
// directions.
func TestOversizedMessage(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithMaxMessageSize(4<<10))
	big := json.RawMessage(`{"data":"` + strings.Repeat("x", 16<<10) + `"}`)

	// Station to CSMS: the CSMS closes the connection.
	c := station(t, base+"BIG1", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	if err := c.Call(context.Background(), "DataTransfer", big, nil); !errors.Is(err, ocpp.ErrConnectionLost) {
		t.Fatalf("oversized CALL to the CSMS: %v", err)
	}

	// CSMS to station: the station closes the connection.
	c2 := station(t, base+"BIG2", client.WithInsecureTransport(), client.WithVersions(ocpp.V201), client.WithMaxMessageSize(4<<10),
		client.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)))
	connect(t, c2)
	sess := waitStation(t, srv, "BIG2")
	if err := sess.Call(context.Background(), "DataTransfer", big, nil); !errors.Is(err, ocpp.ErrConnectionLost) {
		t.Fatalf("oversized CALL to the station: %v", err)
	}
	healthy(t, base, "AFTERBIG")
}

// A compressed message that inflates beyond the size limit (a compression
// bomb: 16 MiB that deflate to a few KiB) is refused with status 1009
// without being inflated in full.
func TestCompressionBomb(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	ws := rawStation(t, base, "BOMB", "ocpp2.0.1", true)
	bomb := append(append([]byte(`[2,"1","DataTransfer",{"data":"`), bytes.Repeat([]byte("a"), 16<<20)...), []byte(`"}]`)...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = ws.Write(ctx, websocket.MessageText, bomb)
	_, _, err := ws.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
		t.Fatalf("closed with %v (%v), want 1009", got, err)
	}
	healthy(t, base, "AFTERBOMB")
}

// A peer that ignores T6 and sends a flood of CALLs without waiting gets
// every one answered, in order, while the server stays responsive to
// others; the bounded incoming queue holds the reader back instead of
// buffering without limit.
func TestIncomingFlood(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	ws := rawStation(t, base, "FLOOD", "ocpp2.0.1", false)
	const n = 2000
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() {
		for i := range n {
			if err := ws.Write(ctx, websocket.MessageText, fmt.Appendf(nil, `[2,"%d","Heartbeat",{}]`, i)); err != nil {
				return
			}
		}
	}()
	healthy(t, base, "DURINGFLOOD")
	for i := range n {
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatalf("answer %d: %v", i, err)
		}
		if want := fmt.Sprintf(`[3,"%d",`, i); !strings.HasPrefix(string(data), want) {
			t.Fatalf("answer %d: %s", i, data)
		}
	}
}

// Callers beyond MaxQueuedCalls get ErrBusy at once while the slot is
// taken; the queued ones complete once the peer answers.
func TestQueueSaturation(t *testing.T) {
	noLeak(t)
	release := make(chan struct{})
	blocking := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, blocking), server.WithoutAuthentication())
	const queued = 4
	c := station(t, base+"QUEUE", client.WithInsecureTransport(), client.WithVersions(ocpp.V201), client.WithMaxQueuedCalls(queued))
	connect(t, c)
	var busy, done atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := c.Call(context.Background(), "Heartbeat", nil, nil); {
			case err == nil:
				done.Add(1)
			case errors.Is(err, ocpp.ErrBusy):
				busy.Add(1)
			default:
				t.Errorf("call: %v", err)
			}
		}()
	}
	eventually(t, "the excess refused", func() bool { return busy.Load() == 20-1-queued })
	close(release)
	wg.Wait()
	if done.Load() != 1+queued {
		t.Fatalf("%d completed, %d busy", done.Load(), busy.Load())
	}
}

// A station that stops reading cannot hold the CSMS: writes to it time out
// (WithWriteTimeout) and its connection is closed as lost.
func TestSlowReader(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithWriteTimeout(200*time.Millisecond), server.WithResponseTimeout(20*time.Millisecond))
	_ = rawStation(t, base, "DEAF", "ocpp2.0.1", false) // never reads
	sess := waitStation(t, srv, "DEAF")
	payload := json.RawMessage(`{"data":"` + strings.Repeat("x", 1<<20) + `"}`)
	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-sess.Done():
			if !errors.Is(sess.Err(), ocpp.ErrConnectionLost) {
				t.Fatalf("session ended with %v", sess.Err())
			}
			return
		case <-deadline:
			t.Fatal("a station that does not read held its session")
		default:
		}
		_ = sess.Call(context.Background(), "DataTransfer", payload, nil)
	}
}

// Keepalive: when the link silently stops carrying anything (a half-open
// connection), the pings of both sides notice it, the station reconnects
// once the link works again, and calls succeed.
func TestKeepalive(t *testing.T) {
	noLeak(t)
	var mu sync.Mutex
	var lostErr error
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithPingInterval(100*time.Millisecond),
		server.WithSessionHandler(nil, func(s *server.Session, err error) {
			mu.Lock()
			lostErr = err
			mu.Unlock()
		}))
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newChaosProxy(t, u.Host, 1)
	proxy.healed.Store(true)
	states := &stateLog{}
	c := station(t, "ws://"+proxy.ln.Addr().String()+u.Path+"KEEP", client.WithInsecureTransport(), client.WithVersions(ocpp.V201),
		client.WithPingInterval(100*time.Millisecond), client.WithStateHandler(states.handler),
		client.WithReconnect(client.Reconnect{WaitMinimum: 50 * time.Millisecond}))
	connect(t, c)
	first := waitStation(t, srv, "KEEP")

	proxy.frozen.Store(true)
	select {
	case <-first.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the CSMS did not notice the dead link")
	}
	eventually(t, "the station notices the dead link", func() bool { return c.State() != ocpp.Connected })
	mu.Lock()
	if !errors.Is(lostErr, ocpp.ErrConnectionLost) {
		t.Errorf("session ended with %v", lostErr)
	}
	mu.Unlock()

	proxy.cutAll()
	proxy.frozen.Store(false)
	eventually(t, "the station calls again", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return c.Call(ctx, "Heartbeat", nil, nil) == nil
	})
	t.Logf("states %s", states)
}
