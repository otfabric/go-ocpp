// SPDX-License-Identifier: MIT

package server

import (
	"crypto/tls"
	"log/slog"
	"time"

	"github.com/otfabric/go-ocpp"
)

// Defaults of the server options.
const (
	DefaultMaxStations      = 10000
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultResponseTimeout  = 30 * time.Second
	DefaultMaxQueuedCalls   = 64
	DefaultMaxMessageSize   = 64 << 10
)

// ReplacePolicy decides what happens when a station connects with an
// identity that already has a session.
type ReplacePolicy uint8

const (
	// ReplaceExisting closes the existing session and accepts the new
	// connection. Authentication runs first, so only a client that passes
	// it can replace a session. This is the default: stations commonly
	// reconnect before the CSMS notices that the old TCP connection died.
	ReplaceExisting ReplacePolicy = iota
	// RejectNew refuses the new connection with HTTP 409 while a session
	// for the identity exists.
	RejectNew
)

// Option configures a Server.
type Option func(*options)

type options struct {
	handlers           map[ocpp.Version]ocpp.Dispatcher
	auth               Authenticator
	noAuth             bool
	onConnect          func(*Session)
	onDisconnect       func(*Session, error)
	maxStations        int
	replace            ReplacePolicy
	tlsConfig          *tls.Config
	logger             *slog.Logger
	metrics            ocpp.Metrics
	handshakeTimeout   time.Duration
	responseTimeout    time.Duration
	maxQueuedCalls     int
	maxMessageSize     int
	callInterceptor    ocpp.Interceptor
	handlerInterceptor ocpp.Interceptor
	frameObserver      ocpp.FrameObserver
}

// WithHandler sets the dispatcher for stations' CALLs on connections of
// version v, typically built by a version package, e.g.
// ocpp16.CentralSystem(h). The server accepts exactly the versions it has
// dispatchers for.
func WithHandler(v ocpp.Version, d ocpp.Dispatcher) Option {
	return func(o *options) {
		if o.handlers == nil {
			o.handlers = map[ocpp.Version]ocpp.Dispatcher{}
		}
		o.handlers[v] = d
	}
}

// WithAuthenticator sets the authenticator, which decides about every
// connection before the WebSocket upgrade. A server needs one, or
// WithoutAuthentication.
func WithAuthenticator(a Authenticator) Option { return func(o *options) { o.auth = a } }

// WithoutAuthentication accepts every station identity without
// credentials. It exists for tests and closed networks, and is named so
// that it cannot be chosen by accident.
func WithoutAuthentication() Option { return func(o *options) { o.noAuth = true } }

// WithSessionHandler sets functions called when a session starts and when
// it ends (with nil after a local Close, or the cause). onConnect runs
// after the connection is registered, while the station may already be
// sending; onDisconnect runs after the session is removed. Either may be
// nil. They must not block for long.
func WithSessionHandler(onConnect func(*Session), onDisconnect func(*Session, error)) Option {
	return func(o *options) { o.onConnect, o.onDisconnect = onConnect, onDisconnect }
}

// WithMaxStations limits the number of simultaneous sessions. Further
// connections are refused with HTTP 503.
func WithMaxStations(n int) Option { return func(o *options) { o.maxStations = n } }

// WithReplacePolicy sets the policy for a second connection with the same
// identity. The default is ReplaceExisting.
func WithReplacePolicy(p ReplacePolicy) Option { return func(o *options) { o.replace = p } }

// WithTLS makes ListenAndServe and Serve use TLS (security profiles 2 and
// 3; for profile 3 set ClientAuth and ClientCAs). It has no effect when the
// Server is mounted in an http.Server of the application, which then does
// TLS itself. MinVersion is raised to TLS 1.2 if it is lower.
func WithTLS(cfg *tls.Config) Option { return func(o *options) { o.tlsConfig = cfg } }

// WithLogger sets the logger. Without it nothing is logged.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithMetrics sets the metrics callbacks.
func WithMetrics(m ocpp.Metrics) Option { return func(o *options) { o.metrics = m } }

// WithHandshakeTimeout bounds reading the HTTP request headers when the
// server listens itself (ListenAndServe, Serve).
func WithHandshakeTimeout(d time.Duration) Option {
	return func(o *options) { o.handshakeTimeout = d }
}

// WithResponseTimeout sets how long a CALL to a station waits for its
// response.
func WithResponseTimeout(d time.Duration) Option {
	return func(o *options) { o.responseTimeout = d }
}

// WithMaxQueuedCalls sets how many calls to one station may wait for the
// outgoing slot before Call fails with ErrBusy.
func WithMaxQueuedCalls(n int) Option { return func(o *options) { o.maxQueuedCalls = n } }

// WithMaxMessageSize sets the largest message accepted, in bytes. A larger
// one closes the connection.
func WithMaxMessageSize(n int) Option { return func(o *options) { o.maxMessageSize = n } }

// WithCallInterceptor wraps every CALL sent to a station.
func WithCallInterceptor(i ocpp.Interceptor) Option {
	return func(o *options) { o.callInterceptor = i }
}

// WithHandlerInterceptor wraps the handling of every CALL from a station.
func WithHandlerInterceptor(i ocpp.Interceptor) Option {
	return func(o *options) { o.handlerInterceptor = i }
}

// WithFrameObserver sees every frame sent and received.
func WithFrameObserver(f ocpp.FrameObserver) Option {
	return func(o *options) { o.frameObserver = f }
}
