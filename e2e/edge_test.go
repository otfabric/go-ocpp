// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"errors"
	"net"
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

func TestClientURLErrors(t *testing.T) {
	if _, err := client.New("ws://host/ocpp/%zz", client.WithVersions(ocpp.V16), client.WithInsecureTransport()); !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Errorf("bad escape: %v", err)
	}
	if _, err := client.New("::not a url", client.WithVersions(ocpp.V16)); !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Errorf("unparsable: %v", err)
	}
}

func TestConnectTwice(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	c := station(t, base+"TWICE", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	connect(t, c)
	connect(t, c) // does nothing
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatal(err)
	}
}

// A Close while Connect is still in the handshake makes Connect return
// ErrClosed, and the connection that opens anyway is closed.
func TestCloseDuringConnect(t *testing.T) {
	noLeak(t)
	entered, release := make(chan struct{}), make(chan struct{})
	slowAuth := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		close(entered)
		<-release
		return nil
	})
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(slowAuth))
	c := station(t, base+"SLOW", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	done := make(chan error, 1)
	go func() { done <- c.Connect(context.Background()) }()
	<-entered
	_ = c.Close()
	close(release)
	if err := <-done; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("Connect: %v", err)
	}
	eventually(t, "the unwanted session to end", func() bool { return len(srv.Stations()) == 0 })
}

// A server that accepts the WebSocket without selecting an OCPP
// subprotocol is refused by the client.
func TestNoSubprotocolSelected(t *testing.T) {
	noLeak(t)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		_, _, _ = c.Read(context.Background())
		_ = c.CloseNow()
	}))
	defer hs.Close()
	c := station(t, "ws"+strings.TrimPrefix(hs.URL, "http")+"/ocpp/X", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	err := c.Connect(context.Background())
	if !errors.Is(err, ocpp.ErrConnectFailed) || !errors.Is(err, ocpp.ErrUnsupportedVersion) {
		t.Fatalf("err = %v", err)
	}
}

// Close during the reconnect backoff returns at once.
func TestCloseDuringBackoff(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	c := station(t, base+"BO", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{WaitMinimum: time.Hour}))
	connect(t, c)
	_ = waitStation(t, srv, "BO").Close()
	eventually(t, "the client to wait for its next attempt", func() bool { return c.State() == ocpp.Connecting })
	start := time.Now()
	_ = c.Close()
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %v", d)
	}
}

func TestServerListeners(t *testing.T) {
	noLeak(t)
	if _, err := server.New(server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithReplacePolicy(server.ReplacePolicy(9))); !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Errorf("unknown replace policy: %v", err)
	}
	newSrv := func() *server.Server {
		s, err := server.New(server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	// ListenAndServe, until Close.
	s := newSrv()
	served := make(chan error, 1)
	go func() { served <- s.ListenAndServe("127.0.0.1:0") }()
	time.Sleep(50 * time.Millisecond)
	_ = s.Close()
	if err := <-served; !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("ListenAndServe after Close: %v", err)
	}
	// A listener on a closed server is refused, and closed.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Serve(ln); !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("Serve after Close: %v", err)
	}
	if _, err := ln.Accept(); err == nil {
		t.Error("the listener was left open")
	}
	// An address that cannot be listened on.
	if err := newSrv().ListenAndServe("256.0.0.1:0"); err == nil {
		t.Error("ListenAndServe on an invalid address succeeded")
	}
	// A listener closed underneath Serve.
	s2 := newSrv()
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { served <- s2.Serve(ln2) }()
	time.Sleep(50 * time.Millisecond)
	_ = ln2.Close()
	if err := <-served; err == nil || errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("Serve after its listener closed: %v", err)
	}
	_ = s2.Close()
}

// Close while the client redials, inside the handshake, stops the dial.
func TestCloseDuringRedial(t *testing.T) {
	noLeak(t)
	var block atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	auth := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		if block.Load() {
			entered <- struct{}{}
			<-release
		}
		return nil
	})
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(auth))
	defer close(release)
	c := station(t, base+"RD", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{WaitMinimum: time.Millisecond}))
	connect(t, c)
	block.Store(true)
	_ = waitStation(t, srv, "RD").Close()
	<-entered // the redial is in the handshake
	start := time.Now()
	_ = c.Close()
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Close took %v", d)
	}
	if c.State() != ocpp.Closed {
		t.Fatalf("state %v", c.State())
	}
}
