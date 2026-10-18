// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// Headers given with WithHeader reach the CSMS's authenticator; headers the
// client sets itself, and malformed ones, are refused.
func TestClientHeaders(t *testing.T) {
	noLeak(t)
	var tenant atomic.Value
	auth := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		tenant.Store(r.Request.Header.Get("X-Tenant"))
		return nil
	})
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(auth))
	c := station(t, base+"HDR", client.WithInsecureTransport(), client.WithVersions(ocpp.V201), client.WithHeader("X-Tenant", "acme"))
	connect(t, c)
	if got, _ := tenant.Load().(string); got != "acme" {
		t.Fatalf("X-Tenant %q", got)
	}
	for _, h := range [][2]string{{"Authorization", "x"}, {"sec-websocket-protocol", "ocpp1.6"}, {"Host", "x"}, {"Bad Name", "x"}, {"X-Ok", "a\r\nb"}, {"", "x"}} {
		if _, err := client.New(base+"HDR", client.WithInsecureTransport(), client.WithVersions(ocpp.V201), client.WithHeader(h[0], h[1])); !errors.Is(err, ocpp.ErrInvalidOption) {
			t.Errorf("header %q: %v", h[0], err)
		}
	}
}

// A browser page on another origin cannot open a session unless the
// server allows its origin.
func TestOriginCheck(t *testing.T) {
	noLeak(t)
	for _, allowed := range []bool{false, true} {
		opts := []server.Option{server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication()}
		if allowed {
			opts = append(opts, server.WithOriginPatterns("dashboard.example.com"))
		}
		_, base := csms(t, nil, opts...)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, resp, err := websocket.Dial(ctx, base+"ORIGIN", &websocket.DialOptions{
			Subprotocols: []string{"ocpp2.0.1"}, HTTPHeader: http.Header{"Origin": {"https://dashboard.example.com"}}})
		cancel()
		if allowed {
			if err != nil {
				t.Fatalf("allowed origin refused: %v", err)
			}
			_ = c.CloseNow()
		} else if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("foreign origin: %v", err)
		}
	}
}

// Shutdown lets a call in flight finish, refuses new ones, then closes.
func TestClientShutdown(t *testing.T) {
	noLeak(t)
	release := make(chan struct{})
	slow := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		<-release
		return json.RawMessage(`{}`), nil
	})
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, slow), server.WithoutAuthentication())
	c := station(t, base+"DRAIN", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	inFlight := make(chan error, 1)
	go func() { inFlight <- c.Call(context.Background(), "Heartbeat", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	shut := make(chan error, 1)
	go func() { shut <- c.Shutdown(context.Background()) }()
	eventually(t, "new calls refused", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		return errors.Is(c.Call(ctx, "Heartbeat", nil, nil), ocpp.ErrClosed)
	})
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned with a call in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-inFlight; err != nil {
		t.Fatalf("the call in flight: %v", err)
	}
	if err := <-shut; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if c.State() != ocpp.Closed {
		t.Fatalf("state %v", c.State())
	}

	// A context that ends first: Shutdown returns its error and closes.
	release2 := make(chan struct{})
	defer close(release2)
	_, base2 := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, a string, b json.RawMessage) (json.RawMessage, error) {
		select {
		case <-release2:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})), server.WithoutAuthentication())
	c2 := station(t, base2+"DRAIN2", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c2)
	go func() { _ = c2.Call(context.Background(), "Heartbeat", nil, nil) }()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := c2.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) || c2.State() != ocpp.Closed {
		t.Fatalf("Shutdown: %v, state %v", err, c2.State())
	}
}

// WithIncomingRateLimit on the server slows a station's CALLs down without
// refusing any.
func TestServerRateLimit(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithIncomingRateLimit(100, 1))
	c := station(t, base+"RATE", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	start := time.Now()
	for range 11 {
		if err := c.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d < 90*time.Millisecond {
		t.Fatalf("11 CALLs at 100/s took %s", d)
	}
	if _, err := server.New(server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithIncomingRateLimit(-1, 1)); !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Fatalf("negative rate: %v", err)
	}
}

// A station that closes on purpose is told apart from a lost one by the
// ocpp.CloseError wrapped in the session's error.
func TestPeerCloseError(t *testing.T) {
	noLeak(t)
	ended := make(chan error, 1)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithSessionHandler(nil, func(s *server.Session, err error) { ended <- err }))
	c := station(t, base+"BYE", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	sess := waitStation(t, srv, "BYE")
	_ = c.Close()
	err := <-ended
	var ce *ocpp.CloseError
	if !errors.Is(err, ocpp.ErrConnectionLost) || !errors.As(err, &ce) || ce.Code != 1000 || !ce.Normal() {
		t.Fatalf("session ended with %v", err)
	}
	if !errors.As(sess.Err(), &ce) {
		t.Fatalf("Session.Err %v", sess.Err())
	}
	if (&ocpp.CloseError{Code: 1008, Reason: "x"}).Normal() || (&ocpp.CloseError{Code: 1008, Reason: "x"}).Error() == "" {
		t.Fatal("1008 is not a normal close")
	}
}

// The HTTP status of a refused handshake is typed; an authenticator can
// choose it with server.StatusError (4xx and 5xx only).
func TestHandshakeStatus(t *testing.T) {
	noLeak(t)
	auth := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		switch r.StationID {
		case "DOWNGRADE":
			return &server.StatusError{Code: http.StatusForbidden, Err: errors.New("profile 3 required")}
		case "BACKEND":
			return &server.StatusError{Code: http.StatusServiceUnavailable}
		case "ODD":
			return &server.StatusError{Code: 302}
		}
		return errors.New("no")
	})
	_, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(auth))
	for id, want := range map[string]int{"DOWNGRADE": 403, "BACKEND": 503, "ODD": 401, "OTHER": 401} {
		c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
		err := c.Connect(context.Background())
		var he *client.HandshakeError
		if !errors.Is(err, ocpp.ErrConnectFailed) || !errors.As(err, &he) || he.StatusCode != want {
			t.Errorf("%s: %v, want HTTP %d", id, err, want)
		}
	}
	if (&server.StatusError{Code: 403}).Error() != "HTTP 403" {
		t.Fatal("StatusError text")
	}
}

// Send on a client that is not connected, or closed; the options that only
// pass values through; Session.Send to a 2.1 station; WithMaxBadMessages on
// the server.
func TestOptionsAndSendPaths(t *testing.T) {
	noLeak(t)
	idle := station(t, "ws://127.0.0.1:1/ocpp/IDLE", client.WithInsecureTransport(), client.WithVersions(ocpp.V21),
		client.WithWriteTimeout(time.Second), client.WithIncomingRateLimit(100, 10))
	if err := idle.Send(context.Background(), "NotifyPeriodicEventStream", nil); !errors.Is(err, ocpp.ErrNotConnected) {
		t.Fatalf("Send before Connect: %v", err)
	}
	_ = idle.Close()
	if err := idle.Send(context.Background(), "NotifyPeriodicEventStream", nil); !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("Send after Close: %v", err)
	}

	received := make(chan json.RawMessage, 1)
	stationSide := &sendRecorder{echo: ocpp.DispatcherFunc(echo), onSend: func(p json.RawMessage) { received <- p }}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V21, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithMaxBadMessages(2))
	c := station(t, base+"S21", client.WithInsecureTransport(), client.WithVersions(ocpp.V21), client.WithHandler(ocpp.V21, stationSide))
	connect(t, c)
	if err := waitStation(t, srv, "S21").Send(context.Background(), "VendorStream", json.RawMessage(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := <-received; string(got) != `{"v":1}` {
		t.Fatalf("station received %s", got)
	}

	ws := rawStation(t, base, "BAD", "ocpp2.1", false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for range 2 {
		_ = ws.Write(ctx, websocket.MessageText, []byte(`not a frame`))
	}
	for {
		if _, _, err := ws.Read(ctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("closed with %v, want 1008", err)
			}
			break
		}
	}
}

// Compression is really negotiated: the server accepts permessage-deflate
// for every version when it is offered, and the client offers it only with
// WithCompression.
func TestCompressionNegotiated(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)),
		server.WithHandler(ocpp.V21, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	for _, v := range []ocpp.Version{ocpp.V16, ocpp.V201, ocpp.V21} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, resp, err := websocket.Dial(ctx, base+"Z"+strings.ReplaceAll(v.String(), ".", ""), &websocket.DialOptions{
			Subprotocols: []string{v.Subprotocol()}, CompressionMode: websocket.CompressionNoContextTakeover})
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if ext := resp.Header.Get("Sec-WebSocket-Extensions"); !strings.Contains(ext, "permessage-deflate") {
			t.Errorf("%s: the server did not accept compression: %q", v, ext)
		}
		_ = c.CloseNow()
	}

	offered := make(chan string, 2)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offered <- r.Header.Get("Sec-WebSocket-Extensions")
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer hs.Close()
	url := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ocpp/OFFER"
	for _, compress := range []bool{true, false} {
		opts := []client.Option{client.WithInsecureTransport(), client.WithVersions(ocpp.V201)}
		if compress {
			opts = append(opts, client.WithCompression())
		}
		_ = station(t, url, opts...).Connect(context.Background())
		if got := <-offered; strings.Contains(got, "permessage-deflate") != compress {
			t.Errorf("WithCompression %v: offered %q", compress, got)
		}
	}
}
