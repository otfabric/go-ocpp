// SPDX-License-Identifier: MIT

package e2e

// A hostile CSMS against the client: raw WebSocket and raw HTTP servers that
// abuse the handshake, the frames and the connection's lifetime.

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
)

// hostile starts a plain HTTP server with h and returns its ws:// base URL.
func hostile(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	return "ws://" + strings.TrimPrefix(hs.URL, "http://") + "/ocpp/"
}

// acceptOCPP accepts a WebSocket with subprotocol sub, as a CSMS would.
func acceptOCPP(w http.ResponseWriter, r *http.Request, sub string) *websocket.Conn {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{sub}})
	if err != nil {
		return nil
	}
	return c
}

// stall is a TCP listener that accepts connections and never answers, a
// CSMS stuck in the handshake. Its connections close when the test ends.
func stall(t *testing.T) (url string, accepted <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	acc := make(chan struct{}, 64)
	var mu sync.Mutex
	var conns []net.Conn
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			select {
			case acc <- struct{}{}:
			default:
			}
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	return "ws://" + ln.Addr().String() + "/ocpp/", acc
}

// raw101 hijacks the request, answers it with a 101 carrying headers and
// closes the connection.
func raw101(w http.ResponseWriter, r *http.Request, headers string) {
	conn, brw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	h := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h[:])
	headers = strings.ReplaceAll(headers, "{accept}", accept)
	_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\n%s\r\n", headers)
	_ = brw.Flush()
}

// The CSMS answers the handshake with 101 but breaks it: a subprotocol the
// client did not offer, several, or no Upgrade headers. Connect fails with
// ErrConnectFailed, and no *HandshakeError is reported: the CSMS did not
// refuse with an HTTP status.
func TestHostileBrokenUpgrade(t *testing.T) {
	const ok = "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: {accept}\r\n"
	for _, tc := range []struct{ name, headers string }{
		{"not offered", ok + "Sec-WebSocket-Protocol: ocpp2.0.1\r\n"},
		{"several", ok + "Sec-WebSocket-Protocol: ocpp1.6, ocpp2.0.1\r\n"},
		{"unknown", ok + "Sec-WebSocket-Protocol: ocpp9\r\n"},
		{"no upgrade", "Connection: Upgrade\r\nSec-WebSocket-Accept: {accept}\r\nSec-WebSocket-Protocol: ocpp1.6\r\n"},
		{"no connection", "Upgrade: websocket\r\nSec-WebSocket-Accept: {accept}\r\nSec-WebSocket-Protocol: ocpp1.6\r\n"},
		{"wrong accept", "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: AAAA\r\nSec-WebSocket-Protocol: ocpp1.6\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			noLeak(t)
			base := hostile(t, func(w http.ResponseWriter, r *http.Request) { raw101(w, r, tc.headers) })
			c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
			err := c.Connect(context.Background())
			if !errors.Is(err, ocpp.ErrConnectFailed) {
				t.Fatalf("Connect = %v", err)
			}
			if he, ok := errors.AsType[*client.HandshakeError](err); ok {
				t.Errorf("HandshakeError %d for a handshake the CSMS accepted: %v", he.StatusCode, err)
			}
			if c.State() != ocpp.Disconnected {
				t.Errorf("state %v", c.State())
			}
		})
	}
}

// A wss:// CSMS that redirects to ws:// must not get the station onto a
// clear-text connection, nor its Basic credentials sent in clear text.
func TestHostileRedirectDowngrade(t *testing.T) {
	noLeak(t)
	var plainHits atomic.Int64
	var leaked atomic.Value
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		if a := r.Header.Get("Authorization"); a != "" {
			leaked.Store(a)
		}
		if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
			_, _, _ = c.Read(context.Background())
			_ = c.CloseNow()
		}
	}))
	t.Cleanup(plain.Close)
	for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		for _, scheme := range []string{"ws", "http"} {
			t.Run(fmt.Sprintf("%d %s", code, scheme), func(t *testing.T) {
				secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, scheme+"://"+strings.TrimPrefix(plain.URL, "http://")+r.URL.Path, code)
				}))
				t.Cleanup(secure.Close)
				url := "wss://" + strings.TrimPrefix(secure.URL, "https://") + "/ocpp/CS1"
				for _, opt := range []client.Option{
					client.WithTLS(secure.Client().Transport.(*http.Transport).TLSClientConfig),
					client.WithHTTPClient(secure.Client()),
				} {
					c := station(t, url, client.WithVersions(ocpp.V16), client.WithBasicAuth("CS1", "secret"), opt)
					err := c.Connect(context.Background())
					if !errors.Is(err, ocpp.ErrConnectFailed) {
						t.Errorf("Connect followed the downgrade: %v (state %v)", err, c.State())
					}
					if he, ok := errors.AsType[*client.HandshakeError](err); !ok || he.StatusCode != code {
						t.Errorf("err = %v, want HandshakeError %d", err, code)
					}
					_ = c.Close()
				}
			})
		}
	}
	if n := plainHits.Load(); n != 0 {
		t.Errorf("the clear-text server got %d requests (Authorization %v)", n, leaked.Load())
	}
}

// Redirects that keep TLS are followed, with the redirect policy of a
// WithHTTPClient client still applied.
func TestHostileRedirectKeepsTLS(t *testing.T) {
	noLeak(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
			_, _, _ = c.Read(context.Background())
			_ = c.CloseNow()
		}
	}))
	t.Cleanup(target.Close)
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "wss://"+strings.TrimPrefix(target.URL, "https://")+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	front.TLS = target.TLS.Clone()
	front.StartTLS()
	t.Cleanup(front.Close)
	url := "wss://" + strings.TrimPrefix(front.URL, "https://") + "/ocpp/CS1"

	hc := target.Client()
	var asked atomic.Int64
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { asked.Add(1); return nil }
	c := station(t, url, client.WithVersions(ocpp.V16), client.WithHTTPClient(hc))
	connect(t, c)
	if asked.Load() != 1 {
		t.Errorf("the HTTP client's CheckRedirect was asked %d times", asked.Load())
	}
	_ = c.Close()

	refuse := target.Client()
	refuse.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c = station(t, url, client.WithVersions(ocpp.V16), client.WithHTTPClient(refuse))
	if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) {
		t.Errorf("Connect = %v", err)
	}
	if hc.CheckRedirect == nil || refuse.CheckRedirect == nil {
		t.Error("the application's HTTP client was changed")
	}
}

// A CSMS that never answers the handshake: WithHandshakeTimeout ends
// Connect, and so does the context.
func TestHostileHandshakeStall(t *testing.T) {
	noLeak(t)
	base, _ := stall(t)
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithHandshakeTimeout(100*time.Millisecond))
	start := time.Now()
	if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Connect = %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("Connect took %v", d)
	}
	if c.State() != ocpp.Disconnected {
		t.Errorf("state %v", c.State())
	}

	c = station(t, base+"CS2", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	eventually(t, "connecting", func() bool { return c.State() == ocpp.Connecting })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ocpp.ErrConnectFailed) || !errors.Is(err, context.Canceled) {
			t.Errorf("Connect = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect ignored its context")
	}
}

// Close while Connect is stuck in the handshake ends Connect at once with
// ErrClosed, rather than after the handshake timeout.
func TestHostileCloseDuringStalledConnect(t *testing.T) {
	noLeak(t)
	base, accepted := stall(t)
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithHandshakeTimeout(time.Minute))
	done := make(chan error, 1)
	go func() { done <- c.Connect(context.Background()) }()
	<-accepted
	_ = c.Close()
	select {
	case err := <-done:
		if !errors.Is(err, ocpp.ErrClosed) {
			t.Errorf("Connect = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect still in the handshake after Close")
	}
	if c.State() != ocpp.Closed {
		t.Errorf("state %v", c.State())
	}
}

// A second Connect while the first is still in the handshake returns once
// the connection is open, not before.
func TestHostileConcurrentConnect(t *testing.T) {
	noLeak(t)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	entered := make(chan struct{}, 1)
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-gate
		if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
			_, _, _ = c.Read(context.Background())
			_ = c.CloseNow()
		}
	})
	t.Cleanup(release) // before the server's cleanup, which waits for the handler
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	first := make(chan error, 1)
	go func() { first <- c.Connect(context.Background()) }()
	<-entered
	second := make(chan error, 1)
	go func() { second <- c.Connect(context.Background()) }()
	select {
	case err := <-second:
		t.Fatalf("second Connect returned %v in state %v, before the connection opened", err, c.State())
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil || c.State() != ocpp.Connected {
		t.Fatalf("second Connect = %v, state %v", err, c.State())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Connect(ctx); err != nil {
		t.Errorf("Connect on a connected client = %v", err)
	}
}

// A second Connect while the first fails gets the first one's error, and
// a Connect that waits honours its own context.
func TestHostileConcurrentConnectFails(t *testing.T) {
	noLeak(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 4)
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		http.Error(w, "no", http.StatusServiceUnavailable)
	})
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	first := make(chan error, 1)
	go func() { first <- c.Connect(context.Background()) }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting Connect with a cancelled context = %v", err)
	}
	second := make(chan error, 1)
	go func() { second <- c.Connect(context.Background()) }()
	close(release)
	err1, err2 := <-first, <-second
	if he, ok := errors.AsType[*client.HandshakeError](err1); !ok || he.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("first = %v", err1)
	}
	if !errors.Is(err2, ocpp.ErrConnectFailed) {
		t.Fatalf("second = %v", err2)
	}
}

// The state handler may read the client's state and version.
func TestHostileStateHandlerReadsClient(t *testing.T) {
	_, base := hostileEcho(t)
	var c *client.Client
	var mu sync.Mutex
	var seen []string
	c, err := client.New(base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithStateHandler(func(s ocpp.State, err error) {
			got := c.State()
			mu.Lock()
			seen = append(seen, fmt.Sprintf("%v=%v/%v", s, got, c.Version()))
			mu.Unlock()
		}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Connect(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Connect deadlocked in the state handler") // c is left open: Close would hang too
	}
	_ = c.Close()
	mu.Lock()
	defer mu.Unlock()
	want := "[Connecting=Connecting/Version(0) Connected=Connected/1.6 Closed=Closed/1.6]"
	if got := fmt.Sprint(seen); got != want {
		t.Errorf("seen %s, want %s", got, want)
	}
}

// hostileEcho is a raw CSMS that accepts ocpp1.6 and answers every CALL
// with an empty CALLRESULT. It returns the number of connections accepted.
func hostileEcho(t *testing.T) (*atomic.Int64, string) {
	t.Helper()
	var n atomic.Int64
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		c := acceptOCPP(w, r, "ocpp1.6")
		if c == nil {
			return
		}
		n.Add(1)
		defer func() { _ = c.CloseNow() }()
		for {
			_, m, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var f []json.RawMessage
			if json.Unmarshal(m, &f) == nil && len(f) == 4 && string(f[0]) == "2" {
				_ = c.Write(r.Context(), websocket.MessageText, []byte(`[3,`+string(f[1])+`,{}]`))
			}
		}
	})
	return &n, base
}

// A CSMS that accepts the connection and drops it at once: the client
// redials with its back-off, never sooner than WaitMinimum, and Close
// stops it in every phase without leaking.
func TestHostileAcceptThenDrop(t *testing.T) {
	noLeak(t)
	const wait = 20 * time.Millisecond
	var mu sync.Mutex
	var at []time.Time
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		c := acceptOCPP(w, r, "ocpp1.6")
		if c == nil {
			return
		}
		mu.Lock()
		at = append(at, time.Now())
		mu.Unlock()
		_ = c.CloseNow()
	})
	var log stateLog
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{WaitMinimum: wait}), client.WithStateHandler(log.handler))
	connect(t, c)
	eventually(t, "5 connections", func() bool { mu.Lock(); defer mu.Unlock(); return len(at) >= 5 })
	_ = c.Close()
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(at); i++ {
		if d := at[i].Sub(at[i-1]); d < wait {
			t.Errorf("redial %d after %v, before WaitMinimum %v", i, d, wait)
		}
	}
	// Every transition is a change, in the documented order.
	log.mu.Lock()
	defer log.mu.Unlock()
	for i := 1; i < len(log.states); i++ {
		if log.states[i] == log.states[i-1] {
			t.Fatalf("repeated state in %v", log.states)
		}
	}
	if last := log.states[len(log.states)-1]; last != ocpp.Closed {
		t.Errorf("last state %v", last)
	}
}

// SetReconnect races with a redial loop and Close.
func TestHostileSetReconnectRace(t *testing.T) {
	noLeak(t)
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
			_ = c.CloseNow()
		}
	})
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{WaitMinimum: time.Millisecond}))
	connect(t, c)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := range 4 {
		wg.Go(func() {
			for j := 0; ; j++ {
				select {
				case <-stop:
					return
				default:
				}
				var r *client.Reconnect
				if (i+j)%5 != 0 {
					r = &client.Reconnect{WaitMinimum: time.Millisecond, RandomRange: time.Millisecond, RepeatTimes: j % 3}
				}
				_ = c.SetReconnect(r)
				_ = c.State()
				_ = c.Call(context.Background(), "Heartbeat", nil, nil)
			}
		})
	}
	time.Sleep(50 * time.Millisecond) // let the races run; nothing waits on it
	_ = c.Close()
	close(stop)
	wg.Wait()
	if c.State() != ocpp.Closed {
		t.Errorf("state %v", c.State())
	}
}

// Frames a CSMS should never send: results for calls never made,
// duplicate results, a result of the wrong shape. The client ignores the
// unsolicited ones, fails only the call whose result it cannot decode, and
// the connection stays usable.
func TestHostileResults(t *testing.T) {
	noLeak(t)
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		c := acceptOCPP(w, r, "ocpp1.6")
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		ctx := r.Context()
		write := func(s string) { _ = c.Write(ctx, websocket.MessageText, []byte(s)) }
		write(`[3,"never-sent",{}]`)
		write(`[4,"never-sent","GenericError","",{}]`)
		for {
			_, m, err := c.Read(ctx)
			if err != nil {
				return
			}
			var f []json.RawMessage
			if json.Unmarshal(m, &f) != nil || len(f) != 4 {
				continue
			}
			id := string(f[1])
			switch string(f[2]) {
			case `"Twice"`:
				write(`[3,` + id + `,{"n":1}]`)
				write(`[3,` + id + `,{"n":2}]`)
			case `"Shape"`:
				write(`[3,` + id + `,{"n":"one"}]`)
			case `"Array"`:
				write(`[3,` + id + `,[1,2]]`)
			default:
				write(`[3,` + id + `,{"n":3}]`)
			}
		}
	})
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithResponseTimeout(5*time.Second))
	connect(t, c)
	var resp struct{ N int }
	if err := c.Call(context.Background(), "Twice", nil, &resp); err != nil || resp.N != 1 {
		t.Fatalf("Twice: %v %+v", err, resp)
	}
	if err := c.Call(context.Background(), "Shape", nil, &resp); !errors.Is(err, ocpp.ErrProtocol) {
		t.Errorf("Shape: %v", err)
	}
	if err := c.Call(context.Background(), "Next", nil, &resp); err != nil || resp.N != 3 {
		t.Fatalf("Next: %v %+v", err, resp)
	}
	// A malformed CALLRESULT is dropped (ERRORS.md): the call waits on,
	// and holds the outgoing slot until the response timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, "Array", nil, &resp); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Array: %v", err)
	}
	if c.State() != ocpp.Connected {
		t.Errorf("state %v", c.State())
	}
}

// A CSMS that never answers: the call's context and the response timeout
// both end the call, each with its own error, and the slot is free again.
func TestHostileCallTimeouts(t *testing.T) {
	noLeak(t)
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		c := acceptOCPP(w, r, "ocpp1.6")
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	})
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithResponseTimeout(100*time.Millisecond))
	connect(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Call(ctx, "Heartbeat", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctx: %v", err)
	}
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrTimeout) {
		t.Errorf("response timeout: %v", err)
	}
	if c.State() != ocpp.Connected {
		t.Errorf("state %v", c.State())
	}
}

// A CSMS that floods CALLs and never reads the answers, while the client
// closes: Close returns, nothing leaks, and nothing races.
func TestHostileCallFloodDuringClose(t *testing.T) {
	noLeak(t)
	flooding := make(chan struct{}, 1)
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		c := acceptOCPP(w, r, "ocpp1.6")
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for i := 0; ; i++ {
			if c.Write(r.Context(), websocket.MessageText, fmt.Appendf(nil, `[2,"%d","Reset",{"type":"Soft"}]`, i)) != nil {
				return
			}
			if i == 100 {
				flooding <- struct{}{}
			}
		}
	})
	var handled atomic.Int64
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithWriteTimeout(time.Second),
		client.WithHandler(ocpp.V16, ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
			handled.Add(1)
			return json.RawMessage(`{"status":"Accepted"}`), nil
		})))
	connect(t, c)
	<-flooding
	// Close while CALLs are being handled, not before the first one: under
	// load the dispatcher may not have started yet when the flood has.
	eventually(t, "a CALL handled", func() bool { return handled.Load() > 0 })
	start := time.Now()
	_ = c.Close()
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("Close took %v", d)
	}
}

// Close leaves no goroutine behind in any state.
func TestHostileCloseInEveryState(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		noLeak(t)
		c := station(t, "ws://127.0.0.1:1/ocpp/CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
		_ = c.Close()
		if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrClosed) {
			t.Errorf("Connect after Close = %v", err)
		}
	})
	t.Run("failed", func(t *testing.T) {
		noLeak(t)
		base := hostile(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
		c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
		if err := c.Connect(context.Background()); err == nil {
			t.Fatal("connected")
		}
		_ = c.Close()
	})
	t.Run("lost without reconnect", func(t *testing.T) {
		noLeak(t)
		base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
			if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
				_ = c.CloseNow()
			}
		})
		c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
		connect(t, c)
		eventually(t, "disconnected", func() bool { return c.State() == ocpp.Disconnected })
		_ = c.Close()
	})
	t.Run("redial stalled", func(t *testing.T) {
		noLeak(t)
		var n atomic.Int64
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
			if n.Add(1) > 1 {
				select { // the redial hangs in the handshake
				case <-release:
				case <-r.Context().Done():
				}
				return
			}
			if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
				_ = c.CloseNow()
			}
		})
		c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
			client.WithReconnect(client.Reconnect{WaitMinimum: time.Millisecond}), client.WithHandshakeTimeout(time.Minute))
		connect(t, c)
		eventually(t, "redial", func() bool { return n.Load() > 1 })
		start := time.Now()
		_ = c.Close()
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("Close took %v", d)
		}
	})
}

// Connect while WithReconnect redials waits for the redial; Close ends a
// waiting Connect with ErrClosed.
func TestHostileConnectDuringRedial(t *testing.T) {
	noLeak(t)
	var n atomic.Int64
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		k := n.Add(1)
		if k == 2 {
			<-gate // the first redial waits
		}
		c := acceptOCPP(w, r, "ocpp1.6")
		if c == nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		if k == 1 {
			return // the first connection is dropped at once
		}
		_, _, _ = c.Read(context.Background())
	})
	t.Cleanup(release)
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{WaitMinimum: time.Millisecond}))
	connect(t, c)
	eventually(t, "redial", func() bool { return n.Load() == 2 })
	done := make(chan error, 1)
	go func() { done <- c.Connect(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Connect returned %v while redialling (state %v)", err, c.State())
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil || c.State() != ocpp.Connected {
		t.Fatalf("Connect = %v, state %v", err, c.State())
	}

	// Waiting Connects end with ErrClosed on Close.
	stalled, accepted := stall(t)
	c2 := station(t, stalled+"CS2", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { first <- c2.Connect(context.Background()) }()
	<-accepted
	go func() { second <- c2.Connect(context.Background()) }()
	_ = c2.Close()
	if err := <-first; !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("first = %v", err)
	}
	if err := <-second; !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("second = %v", err)
	}
}

// The default redirect policy still applies: wss to wss is followed, and
// a loop stops after 10 redirects.
func TestHostileRedirectDefaultPolicy(t *testing.T) {
	noLeak(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/loop/CS1" {
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
			return
		}
		if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
			_, _, _ = c.Read(context.Background())
			_ = c.CloseNow()
		}
	}))
	t.Cleanup(target.Close)
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "wss://"+strings.TrimPrefix(target.URL, "https://")+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	front.TLS = target.TLS.Clone()
	front.StartTLS()
	t.Cleanup(front.Close)
	tlsOpt := client.WithTLS(target.Client().Transport.(*http.Transport).TLSClientConfig)
	c := station(t, "wss://"+strings.TrimPrefix(front.URL, "https://")+"/ocpp/CS1", client.WithVersions(ocpp.V16), tlsOpt)
	connect(t, c)
	_ = c.Close()
	c = station(t, "wss://"+strings.TrimPrefix(target.URL, "https://")+"/loop/CS1", client.WithVersions(ocpp.V16), tlsOpt)
	if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) || !strings.Contains(err.Error(), "stopped after 10 redirects") {
		t.Errorf("redirect loop: %v", err)
	}
}

// SetReconnect(nil) while the client redials a CSMS that refuses it: the
// redial goes on with the settings it had until it connects, and the next
// loss is not redialled.
func TestHostileSetReconnectNilWhileRedialling(t *testing.T) {
	noLeak(t)
	var n, accepted atomic.Int64
	var allow atomic.Bool
	base := hostile(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) > 1 && !allow.Load() {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		if c := acceptOCPP(w, r, "ocpp1.6"); c != nil {
			accepted.Add(1)
			_ = c.CloseNow()
		}
	})
	var log stateLog
	c := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{WaitMinimum: time.Millisecond}), client.WithStateHandler(log.handler))
	connect(t, c) // accepted, then dropped: the redial loop starts
	eventually(t, "refused redials", func() bool { return n.Load() >= 4 })
	if err := c.SetReconnect(nil); err != nil {
		t.Fatal(err)
	}
	seen := n.Load()
	eventually(t, "a redial after SetReconnect(nil)", func() bool { return n.Load() > seen+1 })
	allow.Store(true)
	eventually(t, "the redial connects and is lost", func() bool {
		log.mu.Lock()
		defer log.mu.Unlock()
		k := len(log.states)
		return accepted.Load() == 2 && k > 0 && log.states[k-1] == ocpp.Disconnected
	})
	_ = c.Close()
	// Not redialled: Connecting would come between Disconnected and Closed.
	if got, want := log.String(), "[Connecting Connected Disconnected Connecting Connected Disconnected Closed]"; got != want {
		t.Errorf("states %s, want %s", got, want)
	}
}
