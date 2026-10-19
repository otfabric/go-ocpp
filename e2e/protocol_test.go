// SPDX-License-Identifier: MIT

package e2e

// The protocol behaviours that differ between OCPP versions, end to end over
// real WebSocket connections, in every version: CALLERROR propagation in both
// directions, the error codes a version defines, handler errors and panics,
// the validation codes of each version, malformed and unknown frames, invalid
// and late responses, response timeouts and identity rules.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/ocppj"
	"github.com/otfabric/go-ocpp/server"
)

// versionCase is what the protocol tests need to know of a version.
type versionCase struct {
	v ocpp.Version
	// typed CSMS and station dispatchers with no handler implemented
	csms, station ocpp.Dispatcher
	// heartbeat sends a typed Heartbeat from a station
	heartbeat func(ctx context.Context, c *client.Client) error
	// boot is a valid BootNotification payload with vendor as given
	boot func(vendor any) string
	// reset is a valid Reset request (an action only the CSMS sends)
	reset string
	// the codes the version uses for a violation of the structure, a
	// missing required field, and a malformed CALL
	format, occurrence, rpc ocpp.ErrorCode
	// undefined is an error code this version does not define
	undefined ocpp.ErrorCode
	// vendorMax is the maxLength of the boot's vendor
	vendorMax int
}

var versionCases = []versionCase{
	{
		v:    ocpp.V16,
		csms: ocpp16.CentralSystem(ocpp16.UnimplementedCentralSystemHandler{}), station: ocpp16.ChargePoint(ocpp16.UnimplementedChargePointHandler{}),
		heartbeat: func(ctx context.Context, c *client.Client) error {
			_, err := ocpp16.Heartbeat(ctx, c, &ocpp16.HeartbeatRequest{})
			return err
		},
		boot: func(vendor any) string {
			return fmt.Sprintf(`{"chargePointVendor":%s,"chargePointModel":"M"}`, mustJSON(vendor))
		},
		reset:  `{"type":"Soft"}`,
		format: ocpp.FormationViolation, occurrence: ocpp.OccurenceConstraintViolation, rpc: ocpp.FormationViolation,
		undefined: ocpp.RpcFrameworkError, vendorMax: 20,
	},
	{
		v:    ocpp.V201,
		csms: ocpp201.CSMS(ocpp201.UnimplementedCSMSHandler{}), station: ocpp201.ChargingStation(ocpp201.UnimplementedChargingStationHandler{}),
		heartbeat: func(ctx context.Context, c *client.Client) error {
			_, err := ocpp201.Heartbeat(ctx, c, &ocpp201.HeartbeatRequest{})
			return err
		},
		boot: func(vendor any) string {
			return fmt.Sprintf(`{"reason":"PowerUp","chargingStation":{"vendorName":%s,"model":"M"}}`, mustJSON(vendor))
		},
		reset:  `{"type":"Immediate"}`,
		format: ocpp.FormatViolation, occurrence: ocpp.OccurrenceConstraintViolation, rpc: ocpp.RpcFrameworkError,
		undefined: ocpp.FormationViolation, vendorMax: 50,
	},
	{
		v:    ocpp.V21,
		csms: ocpp21.CSMS(ocpp21.UnimplementedCSMSHandler{}), station: ocpp21.ChargingStation(ocpp21.UnimplementedChargingStationHandler{}),
		heartbeat: func(ctx context.Context, c *client.Client) error {
			_, err := ocpp21.Heartbeat(ctx, c, &ocpp21.HeartbeatRequest{})
			return err
		},
		boot: func(vendor any) string {
			return fmt.Sprintf(`{"reason":"PowerUp","chargingStation":{"vendorName":%s,"model":"M"}}`, mustJSON(vendor))
		},
		reset:  `{"type":"Immediate"}`,
		format: ocpp.FormatViolation, occurrence: ocpp.OccurrenceConstraintViolation, rpc: ocpp.RpcFrameworkError,
		undefined: ocpp.FormationViolation, vendorMax: 50,
	},
}

// rawPeer is a WebSocket peer driven frame by frame.
type rawPeer struct {
	t  *testing.T
	ws *websocket.Conn
	v  ocpp.Version
}

func (r *rawPeer) write(frame string) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
		r.t.Fatalf("write %s: %v", frame, err)
	}
}

func (r *rawPeer) read() ocppj.Frame {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := r.ws.Read(ctx)
	if err != nil {
		r.t.Fatalf("read: %v", err)
	}
	f, err := ocppj.Decode(data, r.v, ocppj.Limits{})
	if err != nil {
		r.t.Fatalf("decode %s: %v", data, err)
	}
	return f
}

// expectError reads a CALLERROR for id with code.
func (r *rawPeer) expectError(id string, code ocpp.ErrorCode) {
	r.t.Helper()
	f := r.read()
	if f.Type != ocppj.CallError || f.ID != id || f.ErrorCode != code {
		r.t.Fatalf("for %s: got %s %q %s %q, want CALLERROR %s", id, f.Type, f.ID, f.ErrorCode, f.Description, code)
	}
}

// expectNothingBefore checks that the next frame is the answer to a CALL sent
// now: whatever was written before it got no answer.
func (r *rawPeer) expectNothingBefore(id string) {
	r.t.Helper()
	r.write(`[2,"` + id + `","Heartbeat",{}]`)
	if f := r.read(); f.ID != id {
		r.t.Fatalf("an answer to %q came first: %s %q %s", id, f.Type, f.ID, f.ErrorCode)
	}
}

// rawStationPeer connects a raw WebSocket station to base+id.
func rawStationPeer(t *testing.T, base, id string, v ocpp.Version) *rawPeer {
	t.Helper()
	return &rawPeer{t: t, ws: rawStation(t, base, id, v.Subprotocol(), false), v: v}
}

// rawCSMS starts a server that accepts one raw WebSocket connection per
// station and hands it over.
func rawCSMS(t *testing.T, v ocpp.Version) (string, <-chan *rawPeer) {
	t.Helper()
	peers := make(chan *rawPeer, 4)
	done := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{v.Subprotocol()}})
		if err != nil {
			return
		}
		peers <- &rawPeer{t: t, ws: ws, v: v}
		<-done // a hijacked connection does not end the request's context
		_ = ws.CloseNow()
	}))
	t.Cleanup(func() { close(done); hs.Close() })
	return "ws" + strings.TrimPrefix(hs.URL, "http") + "/ocpp/", peers
}

// failing answers each action with a different failure, and the others with {}.
func failing(v ocpp.Version, undefined ocpp.ErrorCode) ocpp.DispatcherFunc {
	return func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		switch action {
		case "Heartbeat", "Reset":
			return nil, &ocpp.CallError{Code: ocpp.GenericError, Description: "refused on " + v.String(), Details: json.RawMessage(`{"why":"test"}`)}
		case "Authorize", "TriggerMessage":
			return nil, ocpp.NewCallError(undefined, "a code this version does not define")
		case "DataTransfer":
			return nil, errors.New("a handler failure with internal details")
		case "StatusNotification", "ClearCache":
			panic("handler panic")
		}
		return json.RawMessage(`{}`), nil
	}
}

// checkFailures sends the failing actions from one side and checks each
// CALLERROR, and that the connection survived a panic.
func checkFailures(t *testing.T, v ocpp.Version, call func(action string) error, refused, undefined, failed, panicked, ok string) {
	t.Helper()
	var ce *ocpp.CallError
	err := call(refused)
	if !errors.As(err, &ce) || ce.Code != ocpp.GenericError || ce.Description != "refused on "+v.String() || string(ce.Details) != `{"why":"test"}` {
		t.Fatalf("%s: CALLERROR not propagated: %v (details %s)", refused, err, ce.Details)
	}
	for _, action := range []string{undefined, failed, panicked} {
		err = call(action)
		if !errors.As(err, &ce) || ce.Code != ocpp.InternalError || ce.Description != "" {
			t.Fatalf("%s: want InternalError without description, got %v", action, err)
		}
	}
	if err := call(ok); err != nil {
		t.Fatalf("%s after a handler panic: %v", ok, err)
	}
}

// TestProtocolPerVersion runs the version-dependent protocol behaviours in
// every version, with go-ocpp on both ends and against raw WebSocket peers.
func TestProtocolPerVersion(t *testing.T) {
	noLeak(t)
	for _, vc := range versionCases {
		t.Run(vc.v.String(), func(t *testing.T) {
			ctx := context.Background()

			t.Run("callerrors-both-directions", func(t *testing.T) {
				srv, base := csms(t, nil, server.WithHandler(vc.v, failing(vc.v, vc.undefined)), server.WithoutAuthentication())
				c := station(t, base+"ERR", client.WithInsecureTransport(), client.WithVersions(vc.v), client.WithHandler(vc.v, failing(vc.v, vc.undefined)))
				connect(t, c)
				if c.Role() != ocpp.ChargingStation {
					t.Errorf("client role %v", c.Role())
				}
				checkFailures(t, vc.v, func(a string) error { return c.Call(ctx, a, json.RawMessage(`{}`), nil) },
					"Heartbeat", "Authorize", "DataTransfer", "StatusNotification", "MeterValues")
				sess := waitStation(t, srv, "ERR")
				if sess.Role() != ocpp.CSMS {
					t.Errorf("session role %v", sess.Role())
				}
				checkFailures(t, vc.v, func(a string) error { return sess.Call(ctx, a, json.RawMessage(`{}`), nil) },
					"Reset", "TriggerMessage", "DataTransfer", "ClearCache", "UnlockConnector")
			})

			t.Run("validation-and-dispatch", func(t *testing.T) {
				_, base := csms(t, nil, server.WithHandler(vc.v, vc.csms), server.WithoutAuthentication())
				r := rawStationPeer(t, base, "VAL", vc.v)
				r.write(`[2,"ok","BootNotification",` + vc.boot("V") + `]`)
				r.expectError("ok", ocpp.NotSupported) // valid, but no handler
				r.write(`[2,"u","NoSuchAction",{}]`)
				r.expectError("u", ocpp.NotImplemented)
				r.write(`[2,"w","Reset",` + vc.reset + `]`)
				r.expectError("w", ocpp.NotSupported) // the CSMS sends it, never receives it
				r.write(`[2,"o","BootNotification",{}]`)
				r.expectError("o", vc.occurrence)
				extra := strings.TrimSuffix(vc.boot("V"), "}") + `,"bogus":1}`
				r.write(`[2,"f","BootNotification",` + extra + `]`)
				r.expectError("f", vc.format)
				r.write(`[2,"t","BootNotification",` + vc.boot(1) + `]`)
				r.expectError("t", ocpp.TypeConstraintViolation)
				r.write(`[2,"p","BootNotification",` + vc.boot(strings.Repeat("v", vc.vendorMax+1)) + `]`)
				r.expectError("p", ocpp.PropertyConstraintViolation)
			})

			t.Run("malformed-and-unknown-frames", func(t *testing.T) {
				_, base := csms(t, nil, server.WithHandler(vc.v, vc.csms), server.WithoutAuthentication())
				r := rawStationPeer(t, base, "MAL", vc.v)
				r.write(`[2,"m1","Heartbeat"]`) // a CALL without payload: its ID is readable
				r.expectError("m1", vc.rpc)
				r.write(`not a frame`)
				if vc.v == ocpp.V16 {
					r.expectNothingBefore("after-1") // 1.6 defines no ID to answer it with
				} else {
					r.expectError("-1", ocpp.RpcFrameworkError)
				}
				r.write(`[7,"z1",{}]`) // an unknown message type is ignored
				r.expectNothingBefore("after-2")
				r.write(`[3,"never-sent",{}]`) // a response to no CALL is dropped
				r.expectNothingBefore("after-3")
				r.write(`[6,"s1","NotifyPeriodicEventStream",{"id":1,"pending":0,"basetime":"2026-01-01T00:00:00Z","data":[]}]`)
				r.expectNothingBefore("after-4") // a SEND (2.1), or an unknown type before 2.1: never answered
			})

			t.Run("invalid-response", func(t *testing.T) {
				url, peers := rawCSMS(t, vc.v)
				c := station(t, url+"INV", client.WithInsecureTransport(), client.WithVersions(vc.v))
				connect(t, c)
				r := <-peers
				t.Cleanup(func() { _ = r.ws.CloseNow() }) // before the station's close, which would wait for its handshake
				done := make(chan error, 1)
				go func() { done <- vc.heartbeat(ctx, c) }()
				call := r.read()
				r.write(`[3,"` + call.ID + `",{}]`) // currentTime missing
				err := <-done
				var ve *ocpp.ValidationError
				if !errors.As(err, &ve) || !errors.Is(err, ocpp.ErrProtocol) {
					t.Fatalf("an invalid response: %v", err)
				}
				go func() { done <- vc.heartbeat(ctx, c) }()
				f := r.read()
				if vc.v == ocpp.V21 {
					// 2.1 answers the invalid CALLRESULT with a CALLRESULTERROR
					if f.Type != ocppj.CallResultError || f.ID != call.ID || f.ErrorCode != ocpp.OccurrenceConstraintViolation {
						t.Fatalf("want CALLRESULTERROR for %s, got %s %q %s", call.ID, f.Type, f.ID, f.ErrorCode)
					}
					f = r.read()
				}
				if f.Type != ocppj.Call {
					t.Fatalf("before 2.1 nothing answers an invalid CALLRESULT; got %s", f.Type)
				}
				r.write(`[3,"` + f.ID + `",{"currentTime":"2026-01-01T00:00:00Z"}]`)
				if err := <-done; err != nil {
					t.Fatalf("a valid response after an invalid one: %v", err)
				}
			})

			t.Run("timeout-and-late-response", func(t *testing.T) {
				url, peers := rawCSMS(t, vc.v)
				c := station(t, url+"LATE", client.WithInsecureTransport(), client.WithVersions(vc.v), client.WithResponseTimeout(200*time.Millisecond))
				connect(t, c)
				r := <-peers
				t.Cleanup(func() { _ = r.ws.CloseNow() }) // before the station's close, which would wait for its handshake
				done := make(chan error, 1)
				go func() { done <- vc.heartbeat(ctx, c) }()
				first := r.read()
				if err := <-done; !errors.Is(err, ocpp.ErrTimeout) {
					t.Fatalf("no answer: %v", err)
				}
				r.write(`[3,"` + first.ID + `",{"currentTime":"2026-01-01T00:00:00Z"}]`) // too late: dropped
				go func() { done <- vc.heartbeat(ctx, c) }()
				next := r.read()
				if next.ID == first.ID {
					t.Fatal("a message ID was reused")
				}
				r.write(`[3,"` + next.ID + `",{"currentTime":"2026-01-01T00:00:00Z"}]`)
				if err := <-done; err != nil {
					t.Fatalf("the call after a late response: %v", err)
				}
			})

			t.Run("identity", func(t *testing.T) {
				_, base := csms(t, nil, server.WithHandler(vc.v, vc.csms), server.WithoutAuthentication())
				c, err := client.New(base+"BAD:ID", client.WithInsecureTransport(), client.WithVersions(vc.v))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = c.Close() }()
				cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				err = c.Connect(cctx)
				var he *client.HandshakeError
				if vc.v == ocpp.V16 {
					if err != nil {
						t.Fatalf("1.6 sets no identity rule: %v", err)
					}
				} else if !errors.As(err, &he) || he.StatusCode != http.StatusBadRequest {
					t.Fatalf("2.x refuses an identity with ':' with 400: %v", err)
				}
			})
		})
	}
}

// SetReconnect changes the back-off of a running client, and nil stops it
// from reconnecting after the next loss.
func TestSetReconnect(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	c := station(t, base+"SETREC", client.WithInsecureTransport(), client.WithVersions(ocpp.V201),
		client.WithReconnect(client.Reconnect{WaitMinimum: time.Hour}))
	connect(t, c)
	if err := c.SetReconnect(&client.Reconnect{WaitMinimum: 10 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	_ = waitStation(t, srv, "SETREC").Close()
	// with the hour of WithReconnect it would not be back within the test
	eventually(t, "the reconnection with the new back-off", func() bool {
		s, ok := srv.Station("SETREC")
		return ok && s.Err() == nil && c.State() == ocpp.Connected && len(srv.Stations()) == 1
	})
	if err := c.SetReconnect(&client.Reconnect{}); !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Fatalf("a back-off without WaitMinimum: %v", err)
	}
	if err := c.SetReconnect(nil); err != nil {
		t.Fatal(err)
	}
	s, _ := srv.Station("SETREC")
	_ = s.Close()
	eventually(t, "the client to stay disconnected", func() bool { return c.State() == ocpp.Disconnected })
	time.Sleep(100 * time.Millisecond)
	if c.State() != ocpp.Disconnected {
		t.Fatalf("reconnected after SetReconnect(nil): %v", c.State())
	}
	if c.SetReconnect(&client.Reconnect{WaitMinimum: time.Millisecond}) == nil && c.State() == ocpp.Connected {
		t.Log("SetReconnect after the loss does not reconnect by itself")
	}
}

// A custom Authenticator sees the request, accepts, or refuses with the HTTP
// status of a StatusError (401 for any other error), in every version.
func TestCustomAuthenticator(t *testing.T) {
	noLeak(t)
	seen := make(chan server.ConnectRequest, 8)
	auth := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		seen <- r
		switch r.StationID {
		case "DENY":
			return &server.StatusError{Code: http.StatusForbidden, Err: errors.New("profile too low")}
		case "DOWN":
			return &server.StatusError{Code: http.StatusServiceUnavailable, Err: errors.New("backend down")}
		case "PLAIN":
			return errors.New("refused")
		case "ODD":
			return &server.StatusError{Code: 200, Err: errors.New("not an error status")}
		}
		if !r.HasBasicAuth || r.Username != r.StationID || string(r.Password) != "pw" {
			return errors.New("bad credentials")
		}
		return nil
	})
	for _, vc := range versionCases {
		t.Run(vc.v.String(), func(t *testing.T) {
			_, base := csms(t, nil, server.WithHandler(vc.v, vc.csms), server.WithAuthenticator(auth))
			for id, want := range map[string]int{"OK": 0, "DENY": 403, "DOWN": 503, "PLAIN": 401, "ODD": 401} {
				c, err := client.New(base+id, client.WithInsecureTransport(), client.WithVersions(vc.v), client.WithBasicAuth(id, "pw"))
				if err != nil {
					t.Fatal(err)
				}
				cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				err = c.Connect(cctx)
				cancel()
				_ = c.Close()
				r := <-seen
				if r.StationID != id || r.Request == nil || r.RemoteAddr == "" || r.TLS != nil {
					t.Errorf("%s: ConnectRequest %+v", id, r)
				}
				if strings.Contains(fmt.Sprintf("%v %+v", r.Password, r), "pw") {
					t.Errorf("%s: the password prints", id)
				}
				var he *client.HandshakeError
				switch {
				case want == 0 && err != nil:
					t.Errorf("%s: refused: %v", id, err)
				case want != 0 && (!errors.As(err, &he) || he.StatusCode != want):
					t.Errorf("%s: want HTTP %d, got %v", id, want, err)
				}
			}
		})
	}
}
