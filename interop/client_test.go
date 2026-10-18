//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
)

func newStation(t *testing.T, v ocpp.Version, url string, opts ...client.Option) *client.Client {
	t.Helper()
	c, err := client.New(url, append([]client.Option{client.WithInsecureTransport(), client.WithVersions(v)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestStationAgainstReferenceCSMS runs the go-ocpp client as a charging
// station against each reference CSMS: every station sample of the
// fixture is sent by go-ocpp, and every CSMS sample by the reference, on
// one connection.
func TestStationAgainstReferenceCSMS(t *testing.T) {
	for _, a := range adapters(t) {
		for _, v := range versions {
			t.Run(a.name+"/"+v.String(), func(t *testing.T) {
				refCaps := a.role(t, v, "server")
				f := a.fixture(t, v)
				id, _ := f.station()
				ref := startServer(t, a, v)
				d := newDispatcher(f, f.Answers.Station, v)
				c := newStation(t, v, fmt.Sprintf("ws://127.0.0.1:%d/ocpp/%s", ref.port, id), client.WithHandler(v, d))
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				if err := c.Connect(ctx); err != nil {
					t.Fatalf("connect: %v", err)
				}

				t.Run("from-station", func(t *testing.T) {
					for _, name := range f.samplesFrom("station") {
						s := f.Samples[name]
						t.Run(name, func(t *testing.T) {
							var resp json.RawMessage
							err := c.Call(ctx, s.Action, s.Payload, &resp)
							checkGoCall(t, f, s, resp, err)
						})
					}
				})

				t.Run("from-csms", func(t *testing.T) {
					for _, name := range f.samplesFrom("csms") {
						s := f.Samples[name]
						t.Run(name, func(t *testing.T) {
							if !slices.Contains(refCaps.Sends, s.Action) {
								t.Skipf("%s does not declare sending %s", a.name, s.Action)
							}
							r := ref.call(t, id, s.Action, s.Payload)
							checkRefCall(t, f, s, r)
							checkReceived(t, d, s)
						})
					}
				})

				t.Run("unknown-action", func(t *testing.T) {
					err := c.Call(ctx, "NoSuchAction", nil, nil)
					var ce *ocpp.CallError
					if !errors.As(err, &ce) || !noAnswerCodes[ce.Code] {
						t.Fatalf("want CALLERROR NotImplemented or NotSupported, got %v", err)
					}
				})

				t.Run("close", func(t *testing.T) {
					if err := c.Close(); err != nil {
						t.Fatal(err)
					}
					if c.State() != ocpp.Closed {
						t.Fatalf("state %v", c.State())
					}
				})
			})
		}
	}
}

// TestStationBasicAuth connects to each reference CSMS with security
// profile 1: HTTP Basic authentication with the fixture's password.
func TestStationBasicAuth(t *testing.T) {
	for _, a := range adapters(t) {
		for _, v := range versions {
			t.Run(a.name+"/"+v.String(), func(t *testing.T) {
				a.role(t, v, "server")
				a.securityProfile(t, v, 1)
				f := a.fixture(t, v)
				id, password := f.station()
				ref := startServer(t, a, v, "--security-profile", "1")
				url := fmt.Sprintf("ws://127.0.0.1:%d/ocpp/%s", ref.port, id)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				ok := newStation(t, v, url, client.WithBasicAuth(id, password))
				if err := ok.Connect(ctx); err != nil {
					t.Fatalf("correct password: %v", err)
				}
				if err := ok.Call(ctx, "Heartbeat", json.RawMessage(`{}`), nil); err != nil {
					t.Fatalf("heartbeat: %v", err)
				}
				_ = ok.Close()

				// Same length as the fixture's password: some upstreams refuse other
				// lengths before checking the value.
				wrong := []byte(password)
				wrong[len(wrong)-1] ^= 1
				bad := newStation(t, v, url, client.WithBasicAuth(id, string(wrong)))
				if err := bad.Connect(ctx); !errors.Is(err, ocpp.ErrConnectFailed) {
					t.Fatalf("wrong password: %v", err)
				}
				none := newStation(t, v, url)
				if err := none.Connect(ctx); !errors.Is(err, ocpp.ErrConnectFailed) {
					t.Fatalf("no credentials: %v", err)
				}
			})
		}
	}
}
