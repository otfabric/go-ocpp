// SPDX-License-Identifier: MIT

package e2e

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type recorder struct {
	ocpp.NopMetrics
	connects, disconnects, failed, done, handled atomic.Int64
}

func (r *recorder) OnConnect(ocpp.ConnInfo)                                   { r.connects.Add(1) }
func (r *recorder) OnDisconnect(ocpp.ConnInfo, error)                         { r.disconnects.Add(1) }
func (r *recorder) OnConnectFailed(ocpp.ConnInfo, error)                      { r.failed.Add(1) }
func (r *recorder) OnCallDone(ocpp.ConnInfo, string, time.Duration, error)    { r.done.Add(1) }
func (r *recorder) OnCallHandled(ocpp.ConnInfo, string, time.Duration, error) { r.handled.Add(1) }

// Every observability option on both ends, over a server that listens
// itself with TLS; and no credential in the logs.
func TestObservability(t *testing.T) {
	noLeak(t)
	const password = "s3cret-password-value"
	p := newPKI(t)
	var logs syncBuffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	srvMetrics, cliMetrics := &recorder{}, &recorder{}
	var intercepted, frames atomic.Int64
	icpt := func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
		intercepted.Add(1)
		return next(ctx)
	}
	obs := func(ocpp.ConnInfo, ocpp.Direction, []byte) { frames.Add(1) }
	var connected, disconnected atomic.Int64

	srv, err := server.New(
		server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)),
		server.WithAuthenticator(server.BasicAuth(func(ctx context.Context, id string) (string, bool) { return password, id == "OBS" })),
		server.WithTLS(&tls.Config{Certificates: []tls.Certificate{p.issue(t, "csms", true)}}),
		server.WithLogger(logger), server.WithMetrics(srvMetrics),
		server.WithHandshakeTimeout(5*time.Second), server.WithResponseTimeout(5*time.Second),
		server.WithMaxQueuedCalls(4), server.WithMaxMessageSize(1<<15),
		server.WithCallInterceptor(icpt), server.WithHandlerInterceptor(icpt), server.WithFrameObserver(obs),
		server.WithSessionHandler(func(*server.Session) { connected.Add(1) }, func(*server.Session, error) { disconnected.Add(1) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if v := srv.Versions(); len(v) != 1 || v[0] != ocpp.V201 {
		t.Errorf("Versions() = %v", v)
	}
	ln, err := netListen()
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if err := <-served; !errors.Is(err, ocpp.ErrClosed) {
			t.Errorf("Serve: %v", err)
		}
	}()

	c := station(t, "wss://"+ln.Addr().String()+"/ocpp/OBS",
		client.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)),
		client.WithTLS(&tls.Config{RootCAs: p.caPool}), client.WithBasicAuth("OBS", password),
		client.WithLogger(logger), client.WithMetrics(cliMetrics),
		client.WithHandshakeTimeout(5*time.Second), client.WithResponseTimeout(5*time.Second),
		client.WithMaxQueuedCalls(4), client.WithMaxMessageSize(1<<15),
		client.WithCallInterceptor(icpt), client.WithHandlerInterceptor(icpt), client.WithFrameObserver(obs))
	connect(t, c)
	sess := waitStation(t, srv, "OBS")
	if sess.SessionID() == "" || sess.RemoteAddr() == "" || sess.ConnectedAt().IsZero() {
		t.Errorf("session accessors: %q %q %v", sess.SessionID(), sess.RemoteAddr(), sess.ConnectedAt())
	}
	if err := c.Call(context.Background(), "Heartbeat", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := sess.Call(context.Background(), "Reset", nil, nil); err != nil {
		t.Fatal(err)
	}
	bad := station(t, "wss://"+ln.Addr().String()+"/ocpp/OBS", client.WithVersions(ocpp.V201),
		client.WithTLS(&tls.Config{RootCAs: p.caPool}), client.WithBasicAuth("OBS", "wrong-"+password), client.WithLogger(logger))
	if err := bad.Connect(context.Background()); err == nil {
		t.Fatal("wrong password accepted")
	}
	_ = c.Close()
	eventually(t, "the session to end", func() bool { return disconnected.Load() == 1 })

	if intercepted.Load() != 4 {
		t.Errorf("interceptors ran %d times, want 4", intercepted.Load())
	}
	if frames.Load() != 8 {
		t.Errorf("observers saw %d frames, want 8", frames.Load())
	}
	if connected.Load() != 1 || srvMetrics.connects.Load() != 1 || srvMetrics.disconnects.Load() != 1 || srvMetrics.failed.Load() != 1 {
		t.Errorf("server metrics: connects %d disconnects %d failed %d", srvMetrics.connects.Load(), srvMetrics.disconnects.Load(), srvMetrics.failed.Load())
	}
	if cliMetrics.connects.Load() != 1 || cliMetrics.disconnects.Load() != 1 || cliMetrics.done.Load() != 1 || cliMetrics.handled.Load() != 1 {
		t.Errorf("client metrics: connects %d disconnects %d done %d handled %d",
			cliMetrics.connects.Load(), cliMetrics.disconnects.Load(), cliMetrics.done.Load(), cliMetrics.handled.Load())
	}
	out := logs.String()
	if strings.Contains(out, password) {
		t.Errorf("a password appears in the logs:\n%s", out)
	}
	for _, want := range []string{`"station_id":"OBS"`, `"msg":"station connected"`, `"msg":"call done"`, `"msg":"call handled"`, `"msg":"connection refused"`} {
		if !strings.Contains(out, want) {
			t.Errorf("logs lack %s", want)
		}
	}
}

func TestHTTPClientOption(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication())
	c := station(t, base+"H", client.WithInsecureTransport(), client.WithVersions(ocpp.V16), client.WithHTTPClient(&httpClient))
	connect(t, c)
	if _, err := client.New(base+"H", client.WithInsecureTransport(), client.WithVersions(ocpp.V16),
		client.WithHTTPClient(&httpClient), client.WithTLS(&tls.Config{})); !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Errorf("WithHTTPClient and WithTLS: %v", err)
	}
}
