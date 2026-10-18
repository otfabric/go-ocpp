// SPDX-License-Identifier: MIT

package client

import (
	"crypto/tls"
	"log/slog"
	"net/http"
	"time"

	"github.com/otfabric/go-ocpp"
)

// Defaults of the client options.
const (
	DefaultHandshakeTimeout = 30 * time.Second
	DefaultResponseTimeout  = 30 * time.Second
	DefaultMaxQueuedCalls   = 64
	DefaultMaxMessageSize   = 64 << 10
	DefaultReconnectMin     = time.Second
	DefaultReconnectMax     = 2 * time.Minute
)

// Option configures a Client.
type Option func(*options)

type options struct {
	versions           []ocpp.Version
	handlers           map[ocpp.Version]ocpp.Dispatcher
	username, password string
	basicAuth          bool
	tlsConfig          *tls.Config
	httpClient         *http.Client
	insecure           bool
	reconnect          *Reconnect
	stateHandler       func(ocpp.State, error)
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

// WithVersions sets the OCPP versions the client offers, in order of
// preference. Without it the client offers the versions it has handlers
// for, newest first.
func WithVersions(v ...ocpp.Version) Option {
	return func(o *options) { o.versions = append([]ocpp.Version(nil), v...) }
}

// WithHandler sets the dispatcher for the CSMS's CALLs on connections of
// version v, typically built by a version package, e.g.
// ocpp201.ChargingStation(h). A CALL on a version without a dispatcher is
// answered with NotImplemented.
func WithHandler(v ocpp.Version, d ocpp.Dispatcher) Option {
	return func(o *options) {
		if o.handlers == nil {
			o.handlers = map[ocpp.Version]ocpp.Dispatcher{}
		}
		o.handlers[v] = d
	}
}

// WithBasicAuth sends HTTP Basic credentials in the opening handshake
// (security profiles 1 and 2). OCPP uses the station identity as the user
// name. The password is never logged.
func WithBasicAuth(username, password string) Option {
	return func(o *options) { o.username, o.password, o.basicAuth = username, password, true }
}

// WithTLS sets the TLS configuration for wss:// URLs: the trust roots
// (RootCAs) and, for security profile 3, the client certificate
// (Certificates or GetClientCertificate). The configuration is cloned, and
// its MinVersion raised to TLS 1.2 if it is lower.
func WithTLS(cfg *tls.Config) Option {
	return func(o *options) { o.tlsConfig = cfg }
}

// WithHTTPClient sets the HTTP client used for the opening handshake, for
// proxies or custom transports. It cannot be combined with WithTLS: configure
// TLS on the client's transport instead.
func WithHTTPClient(c *http.Client) Option {
	return func(o *options) { o.httpClient = c }
}

// WithInsecureTransport allows ws:// URLs: OCPP without TLS. Credentials
// and messages then cross the network in clear text (security profile 1).
// Use it only on closed networks and in tests.
func WithInsecureTransport() Option {
	return func(o *options) { o.insecure = true }
}

// Reconnect configures automatic reconnection. The delay before attempt n
// is chosen at random between MinDelay and min(MaxDelay, MinDelay·2ⁿ)
// (exponential backoff with jitter).
type Reconnect struct {
	MinDelay time.Duration // zero: DefaultReconnectMin
	MaxDelay time.Duration // zero: DefaultReconnectMax
}

// WithReconnect makes the client redial after an open connection is lost,
// until Close. Nothing is resent: requests that were pending fail with
// ErrConnectionLost, and the application decides what to send again.
func WithReconnect(r Reconnect) Option {
	return func(o *options) { o.reconnect = &r }
}

// WithStateHandler sets a function called on every state change, with the
// error that caused it, if any. It is called synchronously and in order; it
// must not block and must not call Close.
func WithStateHandler(f func(state ocpp.State, err error)) Option {
	return func(o *options) { o.stateHandler = f }
}

// WithLogger sets the logger. Without it nothing is logged.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.logger = l } }

// WithMetrics sets the metrics callbacks.
func WithMetrics(m ocpp.Metrics) Option { return func(o *options) { o.metrics = m } }

// WithHandshakeTimeout bounds the dial, TLS and WebSocket handshakes.
func WithHandshakeTimeout(d time.Duration) Option {
	return func(o *options) { o.handshakeTimeout = d }
}

// WithResponseTimeout sets how long a CALL waits for its response.
func WithResponseTimeout(d time.Duration) Option {
	return func(o *options) { o.responseTimeout = d }
}

// WithMaxQueuedCalls sets how many calls may wait for the outgoing slot
// before Call fails with ErrBusy.
func WithMaxQueuedCalls(n int) Option { return func(o *options) { o.maxQueuedCalls = n } }

// WithMaxMessageSize sets the largest message accepted, in bytes. A larger
// one closes the connection.
func WithMaxMessageSize(n int) Option { return func(o *options) { o.maxMessageSize = n } }

// WithCallInterceptor wraps every outgoing CALL.
func WithCallInterceptor(i ocpp.Interceptor) Option {
	return func(o *options) { o.callInterceptor = i }
}

// WithHandlerInterceptor wraps the handling of every incoming CALL.
func WithHandlerInterceptor(i ocpp.Interceptor) Option {
	return func(o *options) { o.handlerInterceptor = i }
}

// WithFrameObserver sees every frame sent and received.
func WithFrameObserver(f ocpp.FrameObserver) Option {
	return func(o *options) { o.frameObserver = f }
}
