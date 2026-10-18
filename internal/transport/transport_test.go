// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func serve(t *testing.T, sub string, limit int64, handle func(Conn)) string {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r, AcceptOptions{Subprotocol: sub, ReadLimit: limit})
		if err != nil {
			return
		}
		handle(c)
	}))
	t.Cleanup(hs.Close)
	return "ws" + strings.TrimPrefix(hs.URL, "http")
}

func TestEcho(t *testing.T) {
	url := serve(t, "ocpp1.6", 0, func(c Conn) {
		for {
			m, err := c.ReadMessage(context.Background())
			if err != nil {
				return
			}
			if err := c.WriteMessage(context.Background(), m); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := Dial(ctx, url, DialOptions{Subprotocols: []string{"ocpp2.0.1", "ocpp1.6"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(StatusNormal, "") }()
	if c.Subprotocol() != "ocpp1.6" {
		t.Fatalf("subprotocol %q", c.Subprotocol())
	}
	if err := c.WriteMessage(ctx, []byte(`[2,"1","Heartbeat",{}]`)); err != nil {
		t.Fatal(err)
	}
	m, err := c.ReadMessage(ctx)
	if err != nil || string(m) != `[2,"1","Heartbeat",{}]` {
		t.Fatalf("%s %v", m, err)
	}
}

func TestCloseStatusReported(t *testing.T) {
	url := serve(t, "ocpp1.6", 0, func(c Conn) { _ = c.Close(StatusPolicy, "replaced") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := Dial(ctx, url, DialOptions{Subprotocols: []string{"ocpp1.6"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadMessage(ctx)
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != StatusPolicy || ce.Reason != "replaced" || !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(ce.Error(), "1008: replaced") {
		t.Errorf("Error() = %q", ce.Error())
	}
}

func TestBinaryRefused(t *testing.T) {
	got := make(chan error, 1)
	url := serve(t, "ocpp1.6", 0, func(c Conn) {
		_, err := c.ReadMessage(context.Background())
		got <- err
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"ocpp1.6"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.CloseNow() }()
	// The client must be reading to answer the server's close frame.
	closed := make(chan error, 1)
	go func() { _, _, err := raw.Read(ctx); closed <- err }()
	_ = raw.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3})
	if err := <-got; err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("server read: %v", err)
	}
	if err := <-closed; websocket.CloseStatus(err) != StatusUnsupported {
		t.Fatalf("client saw %v, want close 1003", err)
	}
}

func TestReadLimit(t *testing.T) {
	got := make(chan error, 1)
	url := serve(t, "ocpp1.6", 16, func(c Conn) {
		_, err := c.ReadMessage(context.Background())
		got <- err
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := Dial(ctx, url, DialOptions{Subprotocols: []string{"ocpp1.6"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(StatusNormal, "") }()
	_ = c.WriteMessage(ctx, []byte(strings.Repeat("x", 17)))
	if err := <-got; err == nil {
		t.Fatal("oversized message accepted")
	}
	if _, err := c.ReadMessage(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("client: %v", err)
	}
}

func TestPipe(t *testing.T) {
	a, b := Pipe("ocpp2.1")
	if a.Subprotocol() != "ocpp2.1" {
		t.Fatal(a.Subprotocol())
	}
	go func() { _ = a.WriteMessage(context.Background(), []byte("x")) }()
	if m, err := b.ReadMessage(context.Background()); err != nil || string(m) != "x" {
		t.Fatal(m, err)
	}
	_ = a.Close(StatusGoingAway, "bye")
	_, err := b.ReadMessage(context.Background())
	var ce *CloseError
	if !errors.As(err, &ce) || ce.Code != StatusGoingAway {
		t.Fatal(err)
	}
	if err := b.WriteMessage(context.Background(), []byte("y")); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}
