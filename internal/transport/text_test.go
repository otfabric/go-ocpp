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

	"github.com/otfabric/go-ocpp/internal/leakcheck"
)

// A text message that is not valid UTF-8 ends the connection with status
// 1007 (RFC 6455 §8.1), with and without compression, where the check comes
// after decompression. Valid UTF-8 before it is read as it is.
func TestTextNotUTF8(t *testing.T) {
	leakcheck.Check(t)
	// Longer than the size from which coder/websocket compresses a message.
	pad := strings.Repeat("a", 1024)
	for name, bad := range map[string]string{
		"one byte":            "[2,\"1\",\"Heartbeat\",{\"k\":\"" + pad + "\xff\"}]",
		"a truncated rune":    "[2,\"1\",\"Heartbeat\",{\"k\":\"" + pad + "\xe2\x82\"}]",
		"an encoded low half": "[2,\"1\",\"Heartbeat\",{\"k\":\"" + pad + "\xed\xb0\x80\"}]",
		"only that byte":      "\xff",
	} {
		for _, compress := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "", true: "/compressed"}[compress], func(t *testing.T) {
				type read struct {
					msg string
					err error
				}
				got := make(chan read, 2)
				hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, err := Accept(w, r, AcceptOptions{Subprotocol: "ocpp2.0.1", Compression: compress})
					if err != nil {
						return
					}
					for range 2 {
						m, err := c.ReadMessage(context.Background())
						got <- read{string(m), err}
					}
				}))
				defer hs.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				opts := &websocket.DialOptions{Subprotocols: []string{"ocpp2.0.1"}}
				if compress {
					opts.CompressionMode = websocket.CompressionNoContextTakeover
				}
				raw, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http"), opts)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = raw.CloseNow() }()
				if ext := resp.Header.Get("Sec-WebSocket-Extensions"); strings.Contains(ext, "permessage-deflate") != compress {
					t.Fatalf("extensions %q, compression wanted: %v", ext, compress)
				}
				// The client must be reading to answer the server's close frame.
				closed := make(chan error, 1)
				go func() { _, _, err := raw.Read(ctx); closed <- err }()

				good := "[2,\"0\",\"Heartbeat\",{\"k\":\"" + pad + "é\U0001F600�\"}]"
				if err := raw.Write(ctx, websocket.MessageText, []byte(good)); err != nil {
					t.Fatal(err)
				}
				if r := <-got; r.err != nil || r.msg != good {
					t.Fatalf("valid UTF-8: read %d bytes, %v", len(r.msg), r.err)
				}
				if err := raw.Write(ctx, websocket.MessageText, []byte(bad)); err != nil {
					t.Fatal(err)
				}
				r := <-got
				if r.err == nil || r.msg != "" || !strings.Contains(r.err.Error(), "not valid UTF-8") {
					t.Fatalf("read a message of %d bytes, %v; want an error and no message", len(r.msg), r.err)
				}
				if _, isClose := errors.AsType[*CloseError](r.err); isClose {
					t.Fatalf("the error says the peer closed: %v", r.err)
				}
				if err := <-closed; websocket.CloseStatus(err) != StatusInvalidData {
					t.Fatalf("the peer saw %v, want close %d", err, StatusInvalidData)
				}
			})
		}
	}
}
