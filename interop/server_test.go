//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/server"
)

// newCSMS starts a go-ocpp server on all interfaces, where a container can
// reach it through the Docker host gateway, and returns it with its port.
func newCSMS(t *testing.T, opts ...server.Option) (*server.Server, int) {
	t.Helper()
	srv, err := server.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if err := <-served; !errors.Is(err, ocpp.ErrClosed) {
			t.Errorf("Serve: %v", err)
		}
	})
	return srv, ln.Addr().(*net.TCPAddr).Port
}

func waitSession(t *testing.T, srv *server.Server, id string) *server.Session {
	t.Helper()
	var sess *server.Session
	eventually(t, 30*time.Second, "the station's session", func() bool {
		s, ok := srv.Station(id)
		sess = s
		return ok
	})
	return sess
}

// TestReferenceStationAgainstCSMS runs each reference station against the
// go-ocpp server as CSMS: every station sample of the fixture is sent by the
// reference, and every CSMS sample by go-ocpp, on one connection.
func TestReferenceStationAgainstCSMS(t *testing.T) {
	for _, a := range adapters(t) {
		for _, v := range versions {
			t.Run(a.name+"/"+v.String(), func(t *testing.T) {
				refCaps := a.role(t, v, "client")
				f := a.fixture(t, v)
				id, _ := f.station()
				d := newDispatcher(f, f.Answers.CSMS, v)
				srv, port := newCSMS(t, server.WithHandler(v, d), server.WithoutAuthentication())
				ref := startStation(t, a, v, port)
				sess := waitSession(t, srv, id)
				if sess.Version() != v {
					t.Fatalf("negotiated %v", sess.Version())
				}
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()

				t.Run("from-station", func(t *testing.T) {
					for _, name := range f.samplesFrom("station") {
						s := f.Samples[name]
						t.Run(name, func(t *testing.T) {
							if evidence, ok := knownIssues[a.name+"/"+v.String()+"/"+name]; ok {
								t.Skipf("known upstream defect of %s: %s", a.name, evidence)
							}
							if !slices.Contains(refCaps.Sends, s.Action) {
								t.Skipf("%s does not declare sending %s", a.name, s.Action)
							}
							r := ref.call(t, "", s.Action, s.Payload)
							checkRefCall(t, f, s, r)
							checkReceived(t, d, s)
						})
					}
				})

				t.Run("from-csms", func(t *testing.T) {
					for _, name := range f.samplesFrom("csms") {
						s := f.Samples[name]
						t.Run(name, func(t *testing.T) {
							if evidence, ok := knownIssues[a.name+"/"+v.String()+"/"+name]; ok {
								t.Skipf("known upstream defect of %s: %s", a.name, evidence)
							}
							var resp json.RawMessage
							err := sess.Call(ctx, s.Action, s.Payload, &resp)
							checkGoCall(t, f, s, resp, err)
						})
					}
				})

				t.Run("unknown-action", func(t *testing.T) {
					err := sess.Call(ctx, "NoSuchAction", nil, nil)
					var ce *ocpp.CallError
					if !errors.As(err, &ce) || !noAnswerCodes[ce.Code] {
						t.Fatalf("want CALLERROR NotImplemented or NotSupported, got %v", err)
					}
				})

				t.Run("close", func(t *testing.T) {
					ref.stop(t)
					select {
					case <-sess.Done():
					case <-time.After(15 * time.Second):
						t.Fatal("the session did not end when the station closed")
					}
				})
			})
		}
	}
}

// TestReferenceStationBasicAuth has each reference station connect with
// security profile 1 to a go-ocpp server that checks the fixture's password.
func TestReferenceStationBasicAuth(t *testing.T) {
	for _, a := range adapters(t) {
		for _, v := range versions {
			t.Run(a.name+"/"+v.String(), func(t *testing.T) {
				a.role(t, v, "client")
				a.securityProfile(t, v, 1)
				f := a.fixture(t, v)
				id, password := f.station()
				d := newDispatcher(f, f.Answers.CSMS, v)
				auth := server.BasicAuth(func(ctx context.Context, station string) (string, bool) {
					return password, station == id
				})
				srv, port := newCSMS(t, server.WithHandler(v, d), server.WithAuthenticator(auth))
				startStation(t, a, v, port, "--security-profile", "1")
				sess := waitSession(t, srv, id)
				var resp json.RawMessage
				if err := sess.Call(context.Background(), "ClearCache", json.RawMessage(`{}`), &resp); err != nil {
					t.Fatalf("call over the authenticated connection: %v", err)
				}
			})
		}
	}
}
