// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// Every version, CALLs in both directions, including a CSMS that calls the
// station from inside its BootNotification handler.
func TestBidirectional(t *testing.T) {
	noLeak(t)
	for _, v := range ocpp.Versions() {
		t.Run(v.String(), func(t *testing.T) {
			csmsHandler := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
				if action == "BootNotification" {
					var got echoed
					if err := p.Call(ctx, "GetConfiguration", json.RawMessage(`{"key":["HeartbeatInterval"]}`), &got); err != nil {
						return nil, err
					}
					return json.RawMessage(`{"status":"Accepted","from":` + mustJSON(got) + `}`), nil
				}
				return echo(ctx, p, action, payload)
			})
			srv, base := csms(t, nil, server.WithHandler(v, csmsHandler), server.WithoutAuthentication())
			c := station(t, base+"CS-"+v.String(), client.WithInsecureTransport(), client.WithHandler(v, ocpp.DispatcherFunc(echo)))
			connect(t, c)
			if c.Version() != v || c.State() != ocpp.Connected {
				t.Fatalf("version %v state %v", c.Version(), c.State())
			}
			ctx := context.Background()

			var boot struct {
				Status string `json:"status"`
				From   echoed `json:"from"`
			}
			if err := c.Call(ctx, "BootNotification", map[string]string{"chargePointModel": "M"}, &boot); err != nil {
				t.Fatal(err)
			}
			if boot.Status != "Accepted" || boot.From.Action != "GetConfiguration" || boot.From.Station != "CS-"+v.String() {
				t.Fatalf("boot = %+v", boot)
			}

			var hb echoed
			if err := c.Call(ctx, "Heartbeat", nil, &hb); err != nil || hb.Action != "Heartbeat" || hb.Version != v.String() {
				t.Fatalf("heartbeat: %v %+v", err, hb)
			}

			sess := waitStation(t, srv, "CS-"+v.String())
			if sess.Version() != v {
				t.Fatalf("session version %v", sess.Version())
			}
			var reset echoed
			if err := sess.Call(ctx, "Reset", map[string]string{"type": "Soft"}, &reset); err != nil {
				t.Fatal(err)
			}
			if reset.Action != "Reset" || string(reset.Echo) != `{"type":"Soft"}` {
				t.Fatalf("reset = %+v", reset)
			}
		})
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// The server picks the first of the station's offers that it supports, and
// each session keeps its own version.
func TestVersionNegotiation(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil,
		server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)),
		server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)),
		server.WithoutAuthentication())
	for _, tt := range []struct {
		id    string
		offer []ocpp.Version
		want  ocpp.Version
	}{
		{"A", []ocpp.Version{ocpp.V201, ocpp.V16}, ocpp.V201},
		{"B", []ocpp.Version{ocpp.V16, ocpp.V201}, ocpp.V16},
		{"C", []ocpp.Version{ocpp.V21, ocpp.V16}, ocpp.V16},
	} {
		c := station(t, base+tt.id, client.WithInsecureTransport(), client.WithVersions(tt.offer...))
		connect(t, c)
		if c.Version() != tt.want {
			t.Errorf("%s: client version %v, want %v", tt.id, c.Version(), tt.want)
		}
		if s := waitStation(t, srv, tt.id); s.Version() != tt.want {
			t.Errorf("%s: session version %v, want %v", tt.id, s.Version(), tt.want)
		}
	}
	if got := len(srv.Stations()); got != 3 {
		t.Errorf("%d stations, want 3", got)
	}

	c := station(t, base+"D", client.WithInsecureTransport(), client.WithVersions(ocpp.V21))
	err := c.Connect(context.Background())
	if !errors.Is(err, ocpp.ErrConnectFailed) || !strings.Contains(err.Error(), "400") {
		t.Fatalf("unsupported version: %v", err)
	}
	if c.State() != ocpp.Disconnected {
		t.Errorf("state %v", c.State())
	}
}

func TestClientOptions(t *testing.T) {
	for _, tt := range []struct {
		url  string
		opts []client.Option
	}{
		{"ws://localhost/ocpp/A", []client.Option{client.WithVersions(ocpp.V16)}},            // plain ws without opting in
		{"http://localhost/ocpp/A", []client.Option{client.WithVersions(ocpp.V16)}},          // wrong scheme
		{"wss://u:p@localhost/ocpp/A", []client.Option{client.WithVersions(ocpp.V16)}},       // credentials in the URL
		{"wss://localhost/ocpp/", []client.Option{client.WithVersions(ocpp.V16)}},            // no identity
		{"wss://localhost/ocpp/A", nil},                                                      // no version
		{"wss://localhost/ocpp/A", []client.Option{client.WithVersions(ocpp.V16, ocpp.V16)}}, // repeated
		{"wss://localhost/ocpp/A", []client.Option{client.WithVersions(ocpp.V16), client.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo))}},
		{"wss://localhost/ocpp/A", []client.Option{client.WithVersions(ocpp.V16), client.WithReconnect(client.Reconnect{MinDelay: time.Second, MaxDelay: time.Millisecond})}},
	} {
		if _, err := client.New(tt.url, tt.opts...); !errors.Is(err, ocpp.ErrInvalidOption) {
			t.Errorf("New(%q): err = %v, want ErrInvalidOption", tt.url, err)
		}
	}
	c, err := client.New("wss://localhost/ocpp/STATION%2F1", client.WithVersions(ocpp.V16))
	if err != nil || c.ID() != "STATION/1" {
		t.Fatalf("escaped identity: %v %q", err, c.ID())
	}
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrNotConnected) {
		t.Errorf("call before connect: %v", err)
	}
	_ = c.Close()
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("call after close: %v", err)
	}
	if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("connect after close: %v", err)
	}
}

func TestServerOptions(t *testing.T) {
	for _, opts := range [][]server.Option{
		{server.WithoutAuthentication()},                          // no handler
		{server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo))}, // no authentication decision
		{server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
			server.WithAuthenticator(server.BasicAuth(nil))},
		{server.WithHandler(0, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication()},
	} {
		if _, err := server.New(opts...); !errors.Is(err, ocpp.ErrInvalidOption) {
			t.Errorf("err = %v, want ErrInvalidOption", err)
		}
	}
}

// Security profile 1 (Basic over ws://) and its refusals.
func TestBasicAuth(t *testing.T) {
	noLeak(t)
	passwords := map[string]string{"CS1": "correct horse battery staple"}
	var seen atomic.Value
	auth := server.BasicAuth(func(ctx context.Context, id string) (string, bool) {
		p, ok := passwords[id]
		return p, ok
	})
	logging := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		seen.Store(fmt.Sprintf("%v %s %#v", r.Password, r.Password, r.Password))
		return auth.Authenticate(ctx, r)
	})
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(logging))

	ok := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithBasicAuth("CS1", "correct horse battery staple"))
	connect(t, ok)
	if s, _ := seen.Load().(string); strings.Contains(s, "horse") {
		t.Errorf("password printed: %s", s)
	}

	for _, tt := range []struct{ id, user, pass string }{
		{"CS1", "CS1", "wrong"},
		{"CS1", "CS2", "correct horse battery staple"}, // user name is not the identity
		{"CS2", "CS2", "anything"},                     // unknown station
	} {
		c := station(t, base+tt.id, client.WithInsecureTransport(), client.WithVersions(ocpp.V16), client.WithBasicAuth(tt.user, tt.pass))
		err := c.Connect(context.Background())
		if !errors.Is(err, ocpp.ErrConnectFailed) || !strings.Contains(err.Error(), "401") {
			t.Errorf("%+v: %v", tt, err)
		}
		if strings.Contains(fmt.Sprint(err), tt.pass) {
			t.Errorf("error reveals the password: %v", err)
		}
	}
	none := station(t, base+"CS1", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	if err := none.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) {
		t.Errorf("no credentials: %v", err)
	}
}

// Security profiles 2 and 3: TLS with server verification, and mutual TLS
// with the identity taken from the client certificate.
func TestTLS(t *testing.T) {
	noLeak(t)
	p := newPKI(t)
	serverCert := p.issue(t, "csms", true)
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.VerifyClientCertIfGiven,
		ClientCAs:    p.caPool,
	}
	certAuth := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			if r.TLS.PeerCertificates[0].Subject.CommonName == r.StationID {
				return nil
			}
			return errors.New("certificate is not the station's")
		}
		if r.HasBasicAuth && r.Username == r.StationID && string(r.Password) == "profile-2-password" {
			return nil
		}
		return errors.New("no credentials")
	})
	_, base := csms(t, tlsCfg, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(certAuth))

	// Profile 2.
	c2 := station(t, base+"P2", client.WithVersions(ocpp.V201), client.WithTLS(&tls.Config{RootCAs: p.caPool}),
		client.WithBasicAuth("P2", "profile-2-password"))
	connect(t, c2)

	// Profile 3.
	c3 := station(t, base+"P3", client.WithVersions(ocpp.V201),
		client.WithTLS(&tls.Config{RootCAs: p.caPool, Certificates: []tls.Certificate{p.issue(t, "P3", false)}}))
	connect(t, c3)
	if err := c3.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatal(err)
	}

	// Refused: a certificate of another station; an untrusted server.
	wrong := station(t, base+"P3", client.WithVersions(ocpp.V201),
		client.WithTLS(&tls.Config{RootCAs: p.caPool, Certificates: []tls.Certificate{p.issue(t, "OTHER", false)}}))
	if err := wrong.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) {
		t.Errorf("wrong certificate: %v", err)
	}
	untrusted := station(t, base+"P2", client.WithVersions(ocpp.V201), client.WithTLS(&tls.Config{}),
		client.WithBasicAuth("P2", "profile-2-password"))
	if err := untrusted.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) {
		t.Errorf("untrusted server: %v", err)
	}
}

func TestSessionReplacement(t *testing.T) {
	noLeak(t)
	var disconnects atomic.Int64
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithSessionHandler(nil, func(*server.Session, error) { disconnects.Add(1) }))
	first := station(t, base+"DUP", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	connect(t, first)
	old := waitStation(t, srv, "DUP")

	second := station(t, base+"DUP", client.WithInsecureTransport(), client.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)))
	connect(t, second)
	eventually(t, "the old session to end", func() bool {
		select {
		case <-old.Done():
			return true
		default:
			return false
		}
	})
	if !errors.Is(old.Err(), server.ErrReplaced) || !errors.Is(old.Err(), ocpp.ErrClosed) {
		t.Errorf("old session Err = %v", old.Err())
	}
	eventually(t, "the first client to notice", func() bool { return first.State() == ocpp.Disconnected })
	cur := waitStation(t, srv, "DUP")
	if cur == old {
		t.Fatal("Station returns the replaced session")
	}
	var got echoed
	if err := cur.Call(context.Background(), "Reset", nil, &got); err != nil || got.Action != "Reset" {
		t.Fatalf("call to the new session: %v", err)
	}
	if err := second.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatal(err)
	}
	if n := len(srv.Stations()); n != 1 {
		t.Errorf("%d sessions, want 1", n)
	}
	eventually(t, "one disconnect", func() bool { return disconnects.Load() == 1 })
}

func TestRejectNewAndLimits(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithReplacePolicy(server.RejectNew), server.WithMaxStations(1))
	a := station(t, base+"A", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	connect(t, a)
	dup := station(t, base+"A", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	if err := dup.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("duplicate under RejectNew: %v", err)
	}
	b := station(t, base+"B", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	if err := b.Connect(context.Background()); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("over the station limit: %v", err)
	}
	if err := a.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Errorf("first station disturbed: %v", err)
	}
}

func TestReconnect(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	log := &stateLog{}
	var attempts atomic.Int64
	c := station(t, base+"RC", client.WithInsecureTransport(), client.WithVersions(ocpp.V201),
		client.WithReconnect(client.Reconnect{MinDelay: 5 * time.Millisecond, MaxDelay: 20 * time.Millisecond}),
		client.WithStateHandler(log.handler), client.WithMetrics(&reconnectMetrics{n: &attempts}))
	connect(t, c)
	first := waitStation(t, srv, "RC")
	_ = first.Close()
	eventually(t, "a new session", func() bool {
		s, ok := srv.Station("RC")
		return ok && s != first
	})
	eventually(t, "the client to be connected again", func() bool { return c.State() == ocpp.Connected })
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() < 1 {
		t.Error("no reconnect attempt reported")
	}
	_ = c.Close()
	if c.State() != ocpp.Closed {
		t.Errorf("state after Close: %v", c.State())
	}
	want := "[Connecting Connected Disconnected Connecting Connected Closed]"
	if log.String() != want {
		t.Errorf("states %s, want %s", log, want)
	}
}

type reconnectMetrics struct {
	ocpp.NopMetrics
	n *atomic.Int64
}

func (m *reconnectMetrics) OnReconnectAttempt(ocpp.ConnInfo, int) { m.n.Add(1) }

// Reconnect attempts continue while the CSMS is down, and stop at Close.
func TestReconnectStopsOnClose(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	var attempts atomic.Int64
	c := station(t, base+"X", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithReconnect(client.Reconnect{MinDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}),
		client.WithMetrics(&reconnectMetrics{n: &attempts}))
	connect(t, c)
	_ = srv.Close() // refuses new connections from now on
	eventually(t, "several failed attempts", func() bool { return attempts.Load() >= 3 })
	if c.State() != ocpp.Connecting {
		t.Errorf("state %v, want Connecting", c.State())
	}
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); !errors.Is(err, ocpp.ErrNotConnected) {
		t.Errorf("call while reconnecting: %v", err)
	}
	_ = c.Close()
	n := attempts.Load()
	time.Sleep(20 * time.Millisecond)
	if attempts.Load() != n {
		t.Error("attempts continued after Close")
	}
}

// A call pending when the client closes fails with ErrClosed; on the CSMS
// side, a call pending when the station goes away fails with
// ErrConnectionLost.
func TestCloseDuringCall(t *testing.T) {
	noLeak(t)
	release := make(chan struct{})
	defer close(release)
	slow := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	})
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, slow), server.WithoutAuthentication())
	c := station(t, base+"S", client.WithInsecureTransport(), client.WithHandler(ocpp.V16, slow))
	connect(t, c)
	sess := waitStation(t, srv, "S")

	clientErr := make(chan error, 1)
	go func() { clientErr <- c.Call(context.Background(), "Heartbeat", nil, nil) }()
	serverErr := make(chan error, 1)
	go func() { serverErr <- sess.Call(context.Background(), "Reset", nil, nil) }()
	time.Sleep(50 * time.Millisecond)
	_ = c.Close()
	if err := <-clientErr; !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("client call: %v", err)
	}
	if err := <-serverErr; !errors.Is(err, ocpp.ErrConnectionLost) {
		t.Errorf("server call: %v", err)
	}
}

func TestShutdown(t *testing.T) {
	noLeak(t)
	srv, err := server.New(server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	c := station(t, "ws://"+ln.Addr().String()+"/ocpp/SD", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	connect(t, c)
	waitStation(t, srv, "SD")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-served; !errors.Is(err, ocpp.ErrClosed) {
		t.Errorf("Serve returned %v", err)
	}
	eventually(t, "the station to notice", func() bool { return c.State() == ocpp.Disconnected })
	if len(srv.Stations()) != 0 {
		t.Error("sessions left after Shutdown")
	}
}

// Many stations, calls in both directions at once, under -race.
func TestManyStations(t *testing.T) {
	noLeak(t)
	const stations, calls = 40, 10
	var handled atomic.Int64
	counting := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		handled.Add(1)
		return echo(ctx, p, action, payload)
	})
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, counting), server.WithHandler(ocpp.V201, counting), server.WithoutAuthentication())
	var wg sync.WaitGroup
	errs := make(chan error, stations*calls*2)
	for i := range stations {
		v := []ocpp.Version{ocpp.V16, ocpp.V201}[i%2]
		id := fmt.Sprintf("M%03d", i)
		c := station(t, base+id, client.WithInsecureTransport(), client.WithHandler(v, counting))
		connect(t, c)
		sess := waitStation(t, srv, id)
		for j := range calls {
			wg.Add(2)
			go func() {
				defer wg.Done()
				var got echoed
				if err := c.Call(context.Background(), "MeterValues", map[string]int{"n": j}, &got); err != nil {
					errs <- err
				} else if got.Station != id || string(got.Echo) != fmt.Sprintf(`{"n":%d}`, j) {
					errs <- fmt.Errorf("%s got %+v", id, got)
				}
			}()
			go func() {
				defer wg.Done()
				var got echoed
				if err := sess.Call(context.Background(), "GetConfiguration", map[string]int{"n": j}, &got); err != nil {
					errs <- err
				} else if got.Station != id || got.Version != v.String() {
					errs <- fmt.Errorf("%s got %+v", id, got)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := handled.Load(); n != stations*calls*2 {
		t.Errorf("%d calls handled, want %d", n, stations*calls*2)
	}
}

func TestBadRequests(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	httpBase := strings.Replace(base, "ws://", "http://", 1)
	for _, tt := range []struct {
		path   string
		header map[string]string
		status int
	}{
		{"", nil, 404},  // no identity
		{"A", nil, 400}, // no subprotocol
		{"A", map[string]string{"Sec-WebSocket-Protocol": "ocpp2.0.1"}, 400},
		{"A", map[string]string{"Sec-WebSocket-Protocol": "ocpp1.6"}, 426}, // not a WebSocket upgrade
	} {
		status, err := httpGet(httpBase+tt.path, tt.header)
		if err != nil {
			t.Fatal(err)
		}
		if status != tt.status {
			t.Errorf("GET /%s %v: status %d, want %d", tt.path, tt.header, status, tt.status)
		}
	}
}
