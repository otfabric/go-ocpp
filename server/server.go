// SPDX-License-Identifier: MIT

// Package server is the CSMS role of OCPP (OCPP 1.6: Central System): it
// accepts charging stations' connections, answers their CALLs and sends
// CALLs to them.
//
// A Server is an http.Handler: mount it in an application's http.Server or
// mux under any path, or let it listen with ListenAndServe. The station
// identity is the last path segment of the URL.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/engine"
	"github.com/otfabric/go-ocpp/internal/transport"
	"github.com/otfabric/go-ocpp/ocppj"
)

// ErrReplaced is the Err of a session closed because the station connected
// again (ReplaceExisting). It wraps ocpp.ErrClosed.
var ErrReplaced = fmt.Errorf("%w: replaced by a new connection of the station", ocpp.ErrClosed)

// Server is a CSMS endpoint for any number of charging stations. It is safe
// for concurrent use.
type Server struct {
	o   options
	log *slog.Logger

	mu        sync.Mutex
	sessions  map[string]*Session
	admitting int // connections past the checks, not yet registered
	closing   bool
	listeners []*http.Server
	active    sync.WaitGroup
}

// New returns a Server.
func New(opts ...Option) (*Server, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	if len(o.handlers) == 0 {
		return nil, fmt.Errorf("%w: no OCPP version: use WithHandler", ocpp.ErrInvalidOption)
	}
	for v, d := range o.handlers {
		if !v.Valid() || d == nil {
			return nil, fmt.Errorf("%w: invalid version or nil dispatcher", ocpp.ErrInvalidOption)
		}
	}
	if o.auth == nil && !o.noAuth {
		return nil, fmt.Errorf("%w: no authenticator: use WithAuthenticator, or WithoutAuthentication on closed networks", ocpp.ErrInvalidOption)
	}
	if o.auth != nil && o.noAuth {
		return nil, fmt.Errorf("%w: WithAuthenticator and WithoutAuthentication together", ocpp.ErrInvalidOption)
	}
	if o.replace != ReplaceExisting && o.replace != RejectNew {
		return nil, fmt.Errorf("%w: unknown replace policy", ocpp.ErrInvalidOption)
	}
	if o.maxStations <= 0 {
		o.maxStations = DefaultMaxStations
	}
	if o.handshakeTimeout <= 0 {
		o.handshakeTimeout = DefaultHandshakeTimeout
	}
	if o.responseTimeout <= 0 {
		o.responseTimeout = DefaultResponseTimeout
	}
	if o.maxQueuedCalls <= 0 {
		o.maxQueuedCalls = DefaultMaxQueuedCalls
	}
	if o.maxMessageSize <= 0 {
		o.maxMessageSize = DefaultMaxMessageSize
	}
	if o.pingInterval < 0 || o.maxBadMessages < 0 {
		return nil, fmt.Errorf("%w: ping interval and bad message limit must not be negative", ocpp.ErrInvalidOption)
	}
	if o.metrics == nil {
		o.metrics = ocpp.NopMetrics{}
	}
	log := o.logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Server{o: o, log: log, sessions: map[string]*Session{}}, nil
}

// Versions returns the versions the server accepts, newest first.
func (s *Server) Versions() []ocpp.Version {
	var vs []ocpp.Version
	for _, v := range []ocpp.Version{ocpp.V21, ocpp.V201, ocpp.V16} {
		if s.o.handlers[v] != nil {
			vs = append(vs, v)
		}
	}
	return vs
}

// Station returns the session of the station with the given identity.
func (s *Server) Station(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// Stations returns the current sessions, ordered by identity.
func (s *Server) Stations() []*Session {
	s.mu.Lock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// ServeHTTP accepts one station's WebSocket connection and serves it until
// it ends. Before the upgrade it authenticates and applies the limits; a
// refusal is an HTTP error response. A station that offers no supported
// subprotocol gets a completed handshake without one, and the connection
// is closed at once (OCPP-J 1.6 §3.1.2; 2.x Part 4 §3.2).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id, ok := stationID(r.URL)
	if !ok {
		http.Error(w, "the URL must end with the station identity", http.StatusNotFound)
		return
	}
	log := s.log.With("station_id", id, "remote", r.RemoteAddr)
	info := ocpp.ConnInfo{StationID: id, Role: ocpp.CSMS}

	v, supported := s.selectVersion(r.Header)
	info.Version = v
	if s.o.auth != nil {
		user, pass, hasBasic := r.BasicAuth()
		err := s.o.auth.Authenticate(r.Context(), ConnectRequest{
			StationID: id, Username: user, Password: Secret(pass), HasBasicAuth: hasBasic,
			TLS: r.TLS, RemoteAddr: r.RemoteAddr, Request: r,
		})
		switch {
		case errors.Is(err, ErrUnknownStation):
			// OCPP-J 1.6 §3.1.2, 2.x Part 4 §3.2: an identity the CSMS
			// does not recognise SHOULD get 404.
			s.refuse(w, log, info, http.StatusNotFound, "unknown station", fmt.Errorf("authentication: %w", err))
			return
		case err != nil:
			w.Header().Set("WWW-Authenticate", `Basic realm="OCPP", charset="UTF-8"`)
			s.refuse(w, log, info, http.StatusUnauthorized, "unauthorized", fmt.Errorf("authentication: %w", err))
			return
		}
	}
	if !supported {
		err := fmt.Errorf("%w: offered %q", ocpp.ErrUnsupportedVersion, r.Header.Values("Sec-WebSocket-Protocol"))
		s.o.metrics.OnConnectFailed(info, err)
		log.Warn("connection refused", "error", err)
		if conn, aerr := transport.Accept(w, r, transport.AcceptOptions{}); aerr == nil {
			_ = conn.Close(transport.StatusPolicy, "no supported OCPP subprotocol")
		}
		return
	}

	s.mu.Lock()
	existing := s.sessions[id]
	switch {
	case s.closing:
		s.mu.Unlock()
		s.refuse(w, log, info, http.StatusServiceUnavailable, "shutting down", ocpp.ErrClosed)
		return
	case existing != nil && s.o.replace == RejectNew:
		s.mu.Unlock()
		s.refuse(w, log, info, http.StatusConflict, "station already connected", errors.New("station already connected"))
		return
	case existing == nil && len(s.sessions)+s.admitting >= s.o.maxStations:
		s.mu.Unlock()
		s.refuse(w, log, info, http.StatusServiceUnavailable, "too many stations", errors.New("station limit reached"))
		return
	}
	s.admitting++
	s.active.Add(1)
	s.mu.Unlock()
	defer s.active.Done()

	// A CSMS SHALL support RFC 7692 compression (2.0.1 Part 4 §3.3, 2.1
	// §3.4); it is used when the station offers it.
	conn, err := transport.Accept(w, r, transport.AcceptOptions{Subprotocol: v.Subprotocol(), ReadLimit: int64(s.o.maxMessageSize), Compression: true})
	if err != nil {
		s.mu.Lock()
		s.admitting--
		s.mu.Unlock()
		s.o.metrics.OnConnectFailed(info, err)
		log.Warn("WebSocket handshake failed", "error", err)
		return
	}

	sess := &Session{srv: s, id: id, version: v, remote: r.RemoteAddr, sessionID: engine.NewUUID(), connectedAt: time.Now()}
	info.SessionID = sess.sessionID
	sess.ep = engine.New(conn, engine.Config{
		Info:               info,
		Peer:               sess,
		Dispatcher:         s.o.handlers[v],
		ResponseTimeout:    s.o.responseTimeout,
		MaxQueuedCalls:     s.o.maxQueuedCalls,
		Limits:             ocppj.Limits{MaxFrameSize: s.o.maxMessageSize},
		WriteTimeout:       s.o.writeTimeout,
		PingInterval:       s.o.pingInterval,
		MaxBadMessages:     s.o.maxBadMessages,
		Logger:             s.o.logger,
		Metrics:            s.o.metrics,
		CallInterceptor:    s.o.callInterceptor,
		HandlerInterceptor: s.o.handlerInterceptor,
		FrameObserver:      s.o.frameObserver,
	})

	s.mu.Lock()
	s.admitting--
	if s.closing {
		s.mu.Unlock()
		sess.ep.CloseWith(ocpp.ErrClosed, transport.StatusGoingAway, "shutting down")
		<-sess.ep.Done()
		return
	}
	old := s.sessions[id]
	s.sessions[id] = sess
	s.mu.Unlock()
	sess.ep.Start()
	if old != nil {
		log.Info("session replaced", "old_session_id", old.sessionID)
		old.ep.CloseWith(ErrReplaced, transport.StatusPolicy, "replaced by a new connection")
	}

	s.o.metrics.OnConnect(info)
	log.Info("station connected", "ocpp_version", v.String(), "session_id", sess.sessionID)
	if s.o.onConnect != nil {
		s.o.onConnect(sess)
	}

	<-sess.ep.Done()

	s.mu.Lock()
	if s.sessions[id] == sess {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	cause := sess.ep.Err()
	// A local Close or Shutdown is reported as nil; a replacement is not
	// the application's doing, so it is reported as ErrReplaced.
	var reported error
	if !errors.Is(cause, ocpp.ErrClosed) || errors.Is(cause, ErrReplaced) {
		reported = cause
	}
	s.o.metrics.OnDisconnect(info, reported)
	log.Info("station disconnected", "session_id", sess.sessionID, "cause", cause)
	if s.o.onDisconnect != nil {
		s.o.onDisconnect(sess, reported)
	}
}

func (s *Server) refuse(w http.ResponseWriter, log *slog.Logger, info ocpp.ConnInfo, status int, msg string, err error) {
	s.o.metrics.OnConnectFailed(info, err)
	log.Warn("connection refused", "status", status, "error", err)
	http.Error(w, msg, status)
}

// selectVersion picks the first subprotocol the station offers that the
// server has a handler for (SPECIFICATIONS.md T1).
func (s *Server) selectVersion(h http.Header) (ocpp.Version, bool) {
	for _, value := range h.Values("Sec-WebSocket-Protocol") {
		for _, token := range strings.Split(value, ",") {
			v, ok := ocpp.ParseSubprotocol(strings.TrimSpace(token))
			if ok && s.o.handlers[v] != nil {
				return v, true
			}
		}
	}
	return 0, false
}

func stationID(u *url.URL) (string, bool) {
	p := u.EscapedPath()
	id, err := url.PathUnescape(p[strings.LastIndex(p, "/")+1:])
	if err != nil || id == "" {
		return "", false
	}
	return id, true
}

// ListenAndServe listens on addr and serves stations until Shutdown or
// Close, after which it returns ocpp.ErrClosed. With WithTLS it serves TLS.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve serves stations on ln until Shutdown or Close, after which it
// returns ocpp.ErrClosed. With WithTLS it serves TLS.
func (s *Server) Serve(ln net.Listener) error {
	if s.o.tlsConfig != nil {
		cfg := s.o.tlsConfig.Clone()
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
		cfg.NextProtos = []string{"http/1.1"}
		ln = tls.NewListener(ln, cfg)
	}
	hs := &http.Server{Handler: s, ReadHeaderTimeout: s.o.handshakeTimeout, ErrorLog: slog.NewLogLogger(s.log.Handler(), slog.LevelWarn)}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		_ = ln.Close()
		return ocpp.ErrClosed
	}
	s.listeners = append(s.listeners, hs)
	s.mu.Unlock()
	err := hs.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return ocpp.ErrClosed
	}
	return err
}

// Shutdown stops accepting connections, closes every session with status
// 1001 (going away), and waits until they have ended or ctx ends.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	listeners := s.listeners
	sessions := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, hs := range listeners {
		_ = hs.Close()
	}
	for _, sess := range sessions {
		sess.ep.CloseWith(ocpp.ErrClosed, transport.StatusGoingAway, "shutting down")
	}
	done := make(chan struct{})
	go func() { s.active.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close is Shutdown without waiting.
func (s *Server) Close() error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Shutdown(ctx)
	return nil
}

// Session is one station's connection to the server. It implements
// ocpp.Peer, so that the call functions of the version packages accept it.
type Session struct {
	srv         *Server
	id          string
	version     ocpp.Version
	remote      string
	sessionID   string
	connectedAt time.Time
	ep          *engine.Endpoint
}

// ID returns the station identity.
func (s *Session) ID() string { return s.id }

// Role returns ocpp.CSMS, the role of this end of the session. The call
// functions of the version packages use it to refuse an action a station
// sends.
func (s *Session) Role() ocpp.Role { return ocpp.CSMS }

// Version returns the version negotiated for this connection.
func (s *Session) Version() ocpp.Version { return s.version }

// SessionID returns a random identifier of this connection, for logs.
func (s *Session) SessionID() string { return s.sessionID }

// RemoteAddr returns the station's network address.
func (s *Session) RemoteAddr() string { return s.remote }

// ConnectedAt returns when the connection was accepted.
func (s *Session) ConnectedAt() time.Time { return s.connectedAt }

// Call sends a CALL to the station and waits for its result (see
// ocpp.Caller).
func (s *Session) Call(ctx context.Context, action string, req, resp any) error {
	return s.ep.Call(ctx, action, req, resp)
}

// Send sends a SEND to the station (OCPP 2.1; see ocpp.Sender).
func (s *Session) Send(ctx context.Context, action string, req any) error {
	return s.ep.Send(ctx, action, req)
}

// Close closes the connection to the station.
func (s *Session) Close() error { return s.ep.Close() }

// Done is closed when the session has ended.
func (s *Session) Done() <-chan struct{} { return s.ep.Done() }

// Err returns why the session ended: ocpp.ErrClosed after Close or
// Shutdown, ErrReplaced, or an error wrapping ocpp.ErrConnectionLost. It
// is nil while the session runs.
func (s *Session) Err() error { return s.ep.Err() }

var (
	_ ocpp.Peer   = (*Session)(nil)
	_ ocpp.Sender = (*Session)(nil)
)
