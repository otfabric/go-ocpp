// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocppj"
	"github.com/otfabric/go-ocpp/server"
)

// A text message that is not valid UTF-8 closes the session with WebSocket
// status 1007: no handler runs, no CALLERROR is sent, and the session ends
// as a lost connection. Over a real WebSocket, in every version.
func TestTextNotUTF8ClosesSession(t *testing.T) {
	noLeak(t)
	for _, vc := range versionCases {
		t.Run(vc.v.String(), func(t *testing.T) {
			var handled atomic.Int64
			ended := make(chan error, 1)
			d := ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
				handled.Add(1)
				return json.RawMessage(`{}`), nil
			})
			_, base := csms(t, nil, server.WithHandler(vc.v, d), server.WithoutAuthentication(),
				server.WithSessionHandler(nil, func(_ *server.Session, err error) { ended <- err }))
			r := rawStationPeer(t, base, "UTF8", vc.v)
			r.write(`[2,"ok","Heartbeat",{"k":"é"}]`)
			if f := r.read(); f.Type != ocppj.CallResult || f.ID != "ok" {
				t.Fatalf("valid UTF-8: got %s %q", f.Type, f.ID)
			}
			r.write("[2,\"bad\",\"Heartbeat\",{\"k\":\"A\xffB\"}]")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, data, err := r.ws.Read(ctx)
			if websocket.CloseStatus(err) != websocket.StatusInvalidFramePayloadData {
				t.Fatalf("read %q, %v; want close status 1007", data, err)
			}
			select {
			case err := <-ended:
				if !errors.Is(err, ocpp.ErrConnectionLost) {
					t.Fatalf("the session ended with %v, want ErrConnectionLost", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the session did not end")
			}
			if n := handled.Load(); n != 1 {
				t.Fatalf("the handler ran %d times, want once (for the valid CALL)", n)
			}
		})
	}
}

// The same from the CSMS to a station: the client loses the connection and
// closes it with 1007.
func TestTextNotUTF8ClosesClient(t *testing.T) {
	noLeak(t)
	url, peers := rawCSMS(t, ocpp.V201)
	var handled atomic.Int64
	states := make(chan error, 8)
	c := station(t, url+"UTF8", client.WithInsecureTransport(),
		client.WithHandler(ocpp.V201, ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
			handled.Add(1)
			return json.RawMessage(`{}`), nil
		})),
		client.WithStateHandler(func(s ocpp.State, err error) {
			if s == ocpp.Disconnected {
				states <- err
			}
		}))
	connect(t, c)
	r := <-peers
	r.write("[2,\"bad\",\"Reset\",{\"type\":\"\xe2\x82\"}]")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := r.ws.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInvalidFramePayloadData {
		t.Fatalf("read %q, %v; want close status 1007", data, err)
	}
	select {
	case err := <-states:
		if !errors.Is(err, ocpp.ErrConnectionLost) {
			t.Fatalf("the client disconnected with %v, want ErrConnectionLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client did not report the lost connection")
	}
	if n := handled.Load(); n != 0 {
		t.Fatalf("the handler ran %d times", n)
	}
}

// The escape of a surrogate without its pair is valid UTF-8 on the wire, so
// the WebSocket carries it; the frame is then malformed: a CALLERROR under
// its message ID, no handler, and the session stays.
func TestSurrogateEscapeWithoutPair(t *testing.T) {
	noLeak(t)
	for _, vc := range versionCases {
		t.Run(vc.v.String(), func(t *testing.T) {
			_, base := csms(t, nil, server.WithHandler(vc.v, vc.csms), server.WithoutAuthentication())
			r := rawStationPeer(t, base, "SUR", vc.v)
			// A valid BootNotification whose vendor ends with text.
			boot := func(text string) string { return strings.Replace(vc.boot("VENDOR"), "VENDOR", "V"+text, 1) }
			r.write(`[2,"h","BootNotification",` + boot(`\ud83d`) + `]`)
			r.expectError("h", vc.rpc)
			r.write(`[2,"l","BootNotification",` + boot(`\ude00`) + `]`)
			r.expectError("l", vc.rpc)
			r.write(`[2,"p","BootNotification",` + boot(`\ud83d\ude00`) + `]`)
			r.expectError("p", ocpp.NotSupported) // well-formed and valid: it reaches the dispatcher, which has no handler
		})
	}
}
