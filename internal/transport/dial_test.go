// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// brokenUpgrade starts a server that answers every handshake with a 101
// whose upgrade headers are valid except for a subprotocol nobody offered,
// and then keeps the connection open without sending anything. It returns
// the URL and, with TLS, a client configuration that trusts the server.
func brokenUpgrade(t *testing.T, tlsOn bool) (string, *tls.Config) {
	t.Helper()
	done := make(chan struct{})
	hs := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\nSec-WebSocket-Protocol: ocpp9\r\n\r\n")
		_ = brw.Flush()
		<-done
	}))
	if tlsOn {
		hs.StartTLS()
	} else {
		hs.Start()
	}
	t.Cleanup(func() { close(done); hs.Close() })
	if tlsOn {
		return "wss" + strings.TrimPrefix(hs.URL, "https"), hs.Client().Transport.(*http.Transport).TLSClientConfig
	}
	return "ws" + strings.TrimPrefix(hs.URL, "http"), nil
}

// A server that answers with a broken 101 and then holds the connection
// open cannot hold Dial past its context: coder/websocket reads the start of
// such a response under a 3 s timer of its own, which Dial's context must
// cut short.
func TestDialBrokenUpgradeBoundByContext(t *testing.T) {
	plain, _ := brokenUpgrade(t, false)
	secure, trust := brokenUpgrade(t, true)
	var nd net.Dialer
	for name, c := range map[string]struct {
		url string
		hc  *http.Client
	}{
		"default client":   {plain, nil},
		"own transport":    {plain, &http.Client{Transport: &http.Transport{DialContext: nd.DialContext}}},
		"own TLS dialer":   {secure, &http.Client{Transport: &http.Transport{DialTLSContext: (&tls.Dialer{Config: trust}).DialContext}}},
		"TLS of transport": {secure, &http.Client{Transport: &http.Transport{TLSClientConfig: trust}}},
	} {
		t.Run(name, func(t *testing.T) {
			// Long enough for a TLS handshake on a slow (emulated, -race)
			// host, well short of coder/websocket's 3 s timer.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			_, _, err := Dial(ctx, c.url, DialOptions{HTTPClient: c.hc, Subprotocols: []string{"ocpp1.6"}})
			if err == nil || !strings.Contains(err.Error(), "Sec-WebSocket-Protocol") {
				t.Fatalf("a broken upgrade: %v", err)
			}
			// The context ends after 1 s; coder/websocket's own timer after 3 s.
			if d := time.Since(start); d > 2500*time.Millisecond {
				t.Fatalf("Dial returned after %s: not bound by its context (%v)", d, err)
			}
		})
	}
}

// A transport Dial cannot copy is used as it is: a RoundTripper of the
// application's, or an *http.Transport with the deprecated dial functions.
func TestDialUnboundTransports(t *testing.T) {
	url := serve(t, "ocpp1.6", 0, func(c Conn) { _, _ = c.ReadMessage(context.Background()) })
	var nd net.Dialer
	dial := func(n, a string) (net.Conn, error) { return nd.Dial(n, a) }
	deprecatedDial, deprecatedDialTLS := &http.Transport{}, &http.Transport{}
	//lint:ignore SA1019 what is tested
	deprecatedDial.Dial = dial //nolint:staticcheck // SA1019: what is tested
	//lint:ignore SA1019 what is tested
	deprecatedDialTLS.DialTLS = dial //nolint:staticcheck // SA1019: what is tested
	for name, hc := range map[string]*http.Client{
		"round tripper":      {Transport: roundTripper{http.DefaultTransport}},
		"deprecated Dial":    {Transport: deprecatedDial},
		"deprecated DialTLS": {Transport: deprecatedDialTLS},
	} {
		got, release := bound(context.Background(), hc)
		if got != hc || !release() {
			t.Errorf("%s: copied", name)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, _, err := Dial(ctx, url, DialOptions{HTTPClient: hc, Subprotocols: []string{"ocpp1.6"}})
		cancel()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = c.Close(StatusNormal, "")
	}
}

type roundTripper struct{ http.RoundTripper }

// The connection of a successful Dial is not closed when its context ends
// afterwards; when the context ended as the handshake completed, the
// connection is closed and Dial fails.
func TestDialContextEnd(t *testing.T) {
	url := serve(t, "ocpp1.6", 0, func(c Conn) {
		for {
			m, err := c.ReadMessage(context.Background())
			if err != nil || c.WriteMessage(context.Background(), m) != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	c, _, err := Dial(ctx, url, DialOptions{Subprotocols: []string{"ocpp1.6"}})
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := c.WriteMessage(wctx, []byte("x")); err != nil {
		t.Fatalf("write after the dial context ended: %v", err)
	}
	if m, err := c.ReadMessage(wctx); err != nil || string(m) != "x" {
		t.Fatalf("read after the dial context ended: %q %v", m, err)
	}
	_ = c.Close(StatusNormal, "")

	// released, as Dial calls it when bound closed a connection.
	ws, _, err := websocket.Dial(wctx, url, &websocket.DialOptions{Subprotocols: []string{"ocpp1.6"}})
	if err != nil {
		t.Fatal(err)
	}
	ended, end := context.WithCancel(context.Background())
	end()
	if err := released(ended, ws, nil, false); !errors.Is(err, ErrClosed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("released: %v", err)
	}
	if _, _, err := ws.Read(wctx); err == nil {
		t.Fatal("the connection was not closed")
	}
	if err := released(ended, nil, errors.New("x"), false); err == nil || errors.Is(err, ErrClosed) || !errors.Is(err, context.Canceled) {
		t.Fatalf("a failed dial whose context ended: %v", err)
	}
	if err := released(ended, nil, context.Canceled, false); err != context.Canceled {
		t.Fatalf("a dial that failed with its context's error: %v", err)
	}
	if err := released(ended, nil, errors.New("x"), true); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("a dial whose connection was not closed: %v", err)
	}
}
