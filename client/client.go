// SPDX-License-Identifier: MIT

// Package client is the Charging Station role of OCPP (OCPP 1.6: Charge
// Point): it connects to a CSMS, sends CALLs to it and answers the CALLs the
// CSMS sends.
package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/engine"
	"github.com/otfabric/go-ocpp/internal/transport"
	"github.com/otfabric/go-ocpp/ocppj"
)

// Client is a charging station's connection to its CSMS. It is safe for
// concurrent use, and implements ocpp.Peer, so that the call functions of
// the version packages accept it.
type Client struct {
	url    string
	id     string
	o      options
	log    *slog.Logger
	header http.Header
	// draining is set by Shutdown: new calls fail with ErrClosed.
	draining bool
	http     *http.Client

	mu        sync.Mutex
	state     ocpp.State
	ep        *engine.Endpoint
	version   ocpp.Version
	closed    bool
	closeCh   chan struct{}
	superDone chan struct{} // closed when the supervisor has exited; nil if none
}

// New returns a client for the CSMS endpoint url. The station identity is
// the last path segment of url (ws[s]://host/path/<identity>). New does not
// connect; call Connect.
func New(rawURL string, opts ...Option) (*Client, error) {
	o := options{}
	for _, opt := range opts {
		opt(&o)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: url: %w", ocpp.ErrInvalidOption, err)
	}
	switch u.Scheme {
	case "wss":
	case "ws":
		if !o.insecure {
			return nil, fmt.Errorf("%w: ws:// sends everything in clear text; use wss:// or WithInsecureTransport", ocpp.ErrInvalidOption)
		}
	default:
		return nil, fmt.Errorf("%w: url scheme must be ws or wss", ocpp.ErrInvalidOption)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: credentials in the url; use WithBasicAuth", ocpp.ErrInvalidOption)
	}
	id, err := identity(u)
	if err != nil {
		return nil, err
	}
	if len(o.versions) == 0 {
		for _, v := range []ocpp.Version{ocpp.V21, ocpp.V201, ocpp.V16} {
			if o.handlers[v] != nil {
				o.versions = append(o.versions, v)
			}
		}
	}
	if len(o.versions) == 0 {
		return nil, fmt.Errorf("%w: no OCPP version: use WithVersions or WithHandler", ocpp.ErrInvalidOption)
	}
	seen := map[ocpp.Version]bool{}
	for _, v := range o.versions {
		if !v.Valid() || seen[v] {
			return nil, fmt.Errorf("%w: version %v invalid or repeated", ocpp.ErrInvalidOption, v)
		}
		seen[v] = true
	}
	for v := range o.handlers {
		if !seen[v] {
			return nil, fmt.Errorf("%w: handler for %v, which is not offered", ocpp.ErrInvalidOption, v)
		}
	}
	if o.tlsConfig != nil && o.httpClient != nil {
		return nil, fmt.Errorf("%w: WithTLS and WithHTTPClient together", ocpp.ErrInvalidOption)
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
	if o.pingInterval < 0 || o.maxBadMessages < 0 || o.incomingRate < 0 || o.incomingBurst < 0 {
		return nil, fmt.Errorf("%w: ping interval, bad message limit and rate limit must not be negative", ocpp.ErrInvalidOption)
	}
	if o.metrics == nil {
		o.metrics = ocpp.NopMetrics{}
	}
	if r := o.reconnect; r != nil {
		if err := r.check(); err != nil {
			return nil, err
		}
	}
	log := o.logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	log = log.With("component", "ocpp.client")
	o.logger = log // the engine's entries carry the component too
	c := &Client{url: rawURL, id: id, o: o, log: log.With("station_id", id), header: http.Header{}, closeCh: make(chan struct{})}
	for _, h := range o.headers {
		if err := checkHeader(h[0], h[1]); err != nil {
			return nil, err
		}
		c.header.Add(h[0], h[1])
	}
	if o.basicAuth && o.username != id {
		// 2.x Part 2 A00, 1.6 Security Whitepaper: the user name is the
		// identity; a CSMS refuses any other.
		return nil, fmt.Errorf("%w: the Basic authentication user name %q is not the station identity %q", ocpp.ErrInvalidOption, o.username, id)
	}
	if o.basicAuth {
		c.header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(o.username+":"+o.password)))
	}
	switch {
	case o.httpClient != nil:
		c.http = o.httpClient
	case o.tlsConfig != nil:
		cfg := o.tlsConfig.Clone()
		if cfg.MinVersion < tls.VersionTLS12 {
			cfg.MinVersion = tls.VersionTLS12
		}
		c.http = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: cfg, ForceAttemptHTTP2: false}}
	}
	return c, nil
}

func identity(u *url.URL) (string, error) {
	p := u.EscapedPath()
	seg := p[strings.LastIndex(p, "/")+1:]
	id, err := url.PathUnescape(seg)
	if err != nil || id == "" {
		return "", fmt.Errorf("%w: the url must end with the station identity", ocpp.ErrInvalidOption)
	}
	return id, nil
}

// ID returns the station identity.
func (c *Client) ID() string { return c.id }

// Role returns ocpp.ChargingStation. The call functions of the version
// packages use it to refuse an action the CSMS sends.
func (c *Client) Role() ocpp.Role { return ocpp.ChargingStation }

// Version returns the version negotiated on the current connection, or the
// last one; zero before the first connection.
func (c *Client) Version() ocpp.Version {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

// State returns the connection state.
func (c *Client) State() ocpp.State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Connect opens the connection and returns once it is open, or with an
// error wrapping ocpp.ErrConnectFailed. It does not retry: with
// WithReconnect, redialling starts only after an open connection is lost.
// Connect on a connected client does nothing.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	switch {
	case c.closed:
		c.mu.Unlock()
		return ocpp.ErrClosed
	case c.state == ocpp.Connected || c.state == ocpp.Connecting:
		c.mu.Unlock()
		return nil
	}
	c.setStateLocked(ocpp.Connecting, nil)
	c.mu.Unlock()

	ep, v, err := c.dial(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		if ep != nil {
			go func() { _ = ep.Close() }()
		}
		return ocpp.ErrClosed
	}
	if err != nil {
		c.setStateLocked(ocpp.Disconnected, err)
		return err
	}
	c.attachLocked(ep, v)
	done := make(chan struct{})
	c.superDone = done
	go c.supervise(ep, done)
	return nil
}

func (c *Client) attachLocked(ep *engine.Endpoint, v ocpp.Version) {
	c.ep, c.version = ep, v
	ep.Start()
	c.o.metrics.OnConnect(c.info(v))
	c.log.Info("connected", "ocpp_version", v.String())
	c.setStateLocked(ocpp.Connected, nil)
}

func (c *Client) info(v ocpp.Version) ocpp.ConnInfo {
	return ocpp.ConnInfo{StationID: c.id, Version: v, Role: ocpp.ChargingStation}
}

// dial opens one connection and returns its endpoint, not yet started.
func (c *Client) dial(ctx context.Context) (*engine.Endpoint, ocpp.Version, error) {
	ctx, cancel := context.WithTimeout(ctx, c.o.handshakeTimeout)
	defer cancel()
	subprotocols := make([]string, len(c.o.versions))
	for i, v := range c.o.versions {
		subprotocols[i] = v.Subprotocol()
	}
	conn, resp, err := transport.Dial(ctx, c.url, transport.DialOptions{
		HTTPClient: c.http, Header: c.header, Subprotocols: subprotocols, ReadLimit: int64(c.o.maxMessageSize),
		Compression: c.o.compression,
	})
	if err != nil {
		if resp != nil {
			err = fmt.Errorf("%w: %w: %w", ocpp.ErrConnectFailed, &HandshakeError{StatusCode: resp.StatusCode}, err)
		} else {
			err = fmt.Errorf("%w: %w", ocpp.ErrConnectFailed, err)
		}
		c.o.metrics.OnConnectFailed(c.info(0), err)
		c.log.Warn("connect failed", "error", err)
		return nil, 0, err
	}
	// coder/websocket refuses a subprotocol that was not offered, so what
	// remains to refuse is a CSMS that selected none.
	v, ok := ocpp.ParseSubprotocol(conn.Subprotocol())
	if !ok {
		_ = conn.Close(transport.StatusPolicy, "no OCPP subprotocol")
		err := fmt.Errorf("%w: %w: the CSMS selected subprotocol %q", ocpp.ErrConnectFailed, ocpp.ErrUnsupportedVersion, conn.Subprotocol())
		c.o.metrics.OnConnectFailed(c.info(0), err)
		c.log.Warn("connect failed", "error", err)
		return nil, 0, err
	}
	ep := engine.New(conn, engine.Config{
		Info:               c.info(v),
		Peer:               c,
		Dispatcher:         c.o.handlers[v],
		ResponseTimeout:    c.o.responseTimeout,
		MaxQueuedCalls:     c.o.maxQueuedCalls,
		Limits:             ocppj.Limits{MaxFrameSize: c.o.maxMessageSize},
		WriteTimeout:       c.o.writeTimeout,
		PingInterval:       c.o.pingInterval,
		MaxBadMessages:     c.o.maxBadMessages,
		IncomingRate:       c.o.incomingRate,
		IncomingBurst:      c.o.incomingBurst,
		Logger:             c.o.logger,
		Metrics:            c.o.metrics,
		CallInterceptor:    c.o.callInterceptor,
		HandlerInterceptor: c.o.handlerInterceptor,
		FrameObserver:      c.o.frameObserver,
	})
	return ep, v, nil
}

// supervise waits for the connection to end and, with WithReconnect,
// redials until a connection opens or the client is closed.
func (c *Client) supervise(ep *engine.Endpoint, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-ep.Done():
		case <-c.closeCh:
			<-ep.Done()
			return
		}
		cause := ep.Err()
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.ep = nil
		c.o.metrics.OnDisconnect(c.info(c.version), cause)
		c.setStateLocked(ocpp.Disconnected, cause)
		if c.o.reconnect == nil {
			c.mu.Unlock()
			return
		}
		c.setStateLocked(ocpp.Connecting, nil)
		c.mu.Unlock()

		for attempt := 1; ; attempt++ {
			select {
			case <-time.After(c.backoff(attempt)):
			case <-c.closeCh:
				return
			}
			c.o.metrics.OnReconnectAttempt(c.info(0), attempt)
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				select {
				case <-c.closeCh:
					cancel()
				case <-ctx.Done():
				}
			}()
			next, v, err := c.dial(ctx)
			cancel()
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				if next != nil {
					_ = next.Close()
				}
				return
			}
			if err == nil {
				c.attachLocked(next, v)
				ep = next
				c.mu.Unlock()
				break
			}
			c.mu.Unlock()
		}
	}
}

// backoff returns the delay before reconnection attempt n (OCPP 2.x Part 4
// §5.4; see Reconnect). The doubling saturates instead of overflowing.
func (c *Client) backoff(attempt int) time.Duration {
	c.mu.Lock()
	r := *c.o.reconnect
	c.mu.Unlock()
	d := r.WaitMinimum
	for i := 1; i < attempt && i <= r.RepeatTimes && d < math.MaxInt64/2; i++ {
		d *= 2
	}
	if r.RandomRange > 0 {
		d += rand.N(r.RandomRange + 1)
	}
	return d
}

// SetReconnect changes the reconnection back-off from the next attempt on:
// for a CSMS that sets the RetryBackOff variables of OCPPCommCtrlr (2.x)
// while the station runs. Nil stops reconnecting after the next loss; a
// reconnect already waiting finishes with the settings it had.
func (c *Client) SetReconnect(r *Reconnect) error {
	if r != nil {
		if err := r.check(); err != nil {
			return err
		}
		cp := *r
		r = &cp
	}
	c.mu.Lock()
	c.o.reconnect = r
	c.mu.Unlock()
	return nil
}

// setStateLocked records a state change and reports it. Every caller
// changes the state: the transitions are Disconnected → Connecting →
// Connected → Disconnected (→ Connecting …), and any → Closed once.
func (c *Client) setStateLocked(s ocpp.State, err error) {
	c.state = s
	if c.o.stateHandler != nil {
		c.o.stateHandler(s, err)
	}
}

// Call sends a CALL to the CSMS and waits for its result (see ocpp.Caller).
// While the client is not connected it fails at once with
// ocpp.ErrNotConnected; nothing is queued.
func (c *Client) Call(ctx context.Context, action string, req, resp any) error {
	c.mu.Lock()
	ep, closed := c.ep, c.closed || c.draining
	c.mu.Unlock()
	if closed {
		return ocpp.ErrClosed
	}
	if ep == nil {
		return ocpp.ErrNotConnected
	}
	return ep.Call(ctx, action, req, resp)
}

// Send sends a SEND to the CSMS (OCPP 2.1; see ocpp.Sender). It is never
// answered and does not wait for an outstanding CALL. While the client is
// not connected it fails at once with ocpp.ErrNotConnected.
func (c *Client) Send(ctx context.Context, action string, req any) error {
	c.mu.Lock()
	ep, closed := c.ep, c.closed || c.draining
	c.mu.Unlock()
	if closed {
		return ocpp.ErrClosed
	}
	if ep == nil {
		return ocpp.ErrNotConnected
	}
	return ep.Send(ctx, action, req)
}

// Close closes the connection and stops reconnecting. Pending calls fail
// with ocpp.ErrClosed. Close is idempotent. Called from a handler, it
// returns without waiting for that handler.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	ep, done := c.ep, c.superDone
	c.ep = nil
	c.mu.Unlock()
	inHandler := false
	if ep != nil {
		inHandler = ep.HandlerRunning()
		_ = ep.Close()
	}
	if done != nil && !inHandler {
		<-done
	}
	c.mu.Lock()
	if ep != nil && c.state == ocpp.Connected {
		c.o.metrics.OnDisconnect(c.info(c.version), nil)
	}
	c.setStateLocked(ocpp.Closed, nil)
	c.mu.Unlock()
	return nil
}

// HandshakeError is the HTTP status with which the CSMS refused the
// WebSocket handshake. Connect's error wraps it next to
// ocpp.ErrConnectFailed, so that an application can decide how to go on:
// 401 wrong credentials, 404 unknown identity, 409 already connected, 503
// try later.
//
//	var he *client.HandshakeError
//	if errors.As(err, &he) && he.StatusCode == http.StatusUnauthorized { ... }
type HandshakeError struct {
	StatusCode int
}

// Error implements error.
func (e *HandshakeError) Error() string {
	return fmt.Sprintf("HTTP %d %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// checkHeader refuses header names that are not HTTP tokens, values with
// control characters, and the headers the client sets itself.
func checkHeader(name, value string) error {
	if name == "" || strings.IndexFunc(name, func(r rune) bool {
		return r <= ' ' || r >= 0x7f || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, r)
	}) >= 0 {
		return fmt.Errorf("%w: header name %q", ocpp.ErrInvalidOption, name)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%w: header %s has a control character", ocpp.ErrInvalidOption, name)
	}
	switch c := http.CanonicalHeaderKey(name); {
	case c == "Authorization", c == "Host", c == "Connection", c == "Upgrade", strings.HasPrefix(c, "Sec-Websocket-"):
		return fmt.Errorf("%w: header %s is set by the client", ocpp.ErrInvalidOption, c)
	}
	return nil
}

// Shutdown stops new calls (they fail with ocpp.ErrClosed) and the dispatch
// of CALLs from the CSMS that have not started, waits until what is in
// flight is done (the calls already made have their answers, the handlers
// that have started have answered, and every queued frame, SENDs and
// answers included, is written) or until ctx ends, and then closes like Close. It
// returns ctx.Err() when ctx ended first; the client is closed either way.
// Called from a handler, it waits for that handler and so until ctx ends:
// close from a handler with Close, or with Shutdown in ocpp.AfterResponse.
func (c *Client) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.draining = true
	ep := c.ep
	c.mu.Unlock()
	var err error
	if ep != nil {
		ep.Drain()
		t := time.NewTicker(2 * time.Millisecond)
		for !ep.Idle() && ep.Err() == nil && err == nil {
			select {
			case <-t.C:
			case <-ctx.Done():
				err = ctx.Err()
			}
		}
		t.Stop()
	}
	_ = c.Close()
	return err
}

var (
	_ ocpp.Peer   = (*Client)(nil)
	_ ocpp.Sender = (*Client)(nil)
)
