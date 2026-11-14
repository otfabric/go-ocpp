// SPDX-License-Identifier: MIT

package transport

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// defaultCloseTimeout bounds a closing handshake, whatever the peer sends
// meanwhile. After the bound the underlying connection is closed.
const defaultCloseTimeout = 10 * time.Second

// closeBound overrides defaultCloseTimeout when positive (nanoseconds).
var closeBound atomic.Int64

func closeTimeout() time.Duration {
	if d := closeBound.Load(); d > 0 {
		return time.Duration(d)
	}
	return defaultCloseTimeout
}

// SetCloseTimeout sets the bound of a closing handshake, for tests, and
// returns a function that restores the previous one.
func SetCloseTimeout(d time.Duration) (restore func()) {
	prev := closeBound.Swap(int64(d))
	return func() { closeBound.Store(prev) }
}

// DialOptions configure Dial.
type DialOptions struct {
	HTTPClient   *http.Client // nil: http.DefaultClient
	Header       http.Header
	Subprotocols []string
	// ReadLimit is the largest message accepted, in bytes, after
	// decompression; a larger one closes the connection with StatusTooBig.
	ReadLimit int64
	// Compression offers permessage-deflate (RFC 7692), without context
	// takeover.
	Compression bool
}

// Dial opens a WebSocket to url. The *http.Response is returned when the
// server answered the handshake, also on failure, so that its status can be
// reported.
//
// The closing handshake of the connection is bounded (defaultCloseTimeout)
// when the HTTP client's transport is an *http.Transport that dials with
// DialContext or DialTLSContext (or neither); with another transport the
// connection is out of reach, and a peer that stalls the closing handshake
// can hold Close for as long as it keeps the connection open.
//
// Dial does not outlast ctx when the HTTP client's transport is an
// *http.Transport (http.DefaultClient's is): a copy of it closes the
// connections it opens once ctx ends, until Dial returns. coder/websocket
// reads the start of a broken 101 response under a 3 s timer of its own,
// which would otherwise hold Dial past ctx.
func Dial(ctx context.Context, url string, o DialOptions) (Conn, *http.Response, error) {
	hc, d := bound(ctx, o.HTTPClient)
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:      hc,
		HTTPHeader:      o.Header,
		Subprotocols:    o.Subprotocols,
		CompressionMode: compression(o.Compression),
	})
	if err = released(ctx, c, err, d.release()); err != nil {
		return nil, resp, err
	}
	if o.ReadLimit > 0 {
		c.SetReadLimit(o.ReadLimit)
	}
	return &wsConn{c: c, raw: d.closer(), compression: o.Compression && deflate(resp.Header)}, resp, nil
}

// AcceptOptions configure Accept.
type AcceptOptions struct {
	// Subprotocol is the subprotocol to select. The caller has already
	// chosen it from the client's offers. Empty selects none: the
	// handshake completes without a Sec-WebSocket-Protocol header.
	Subprotocol string
	// ReadLimit is the largest message accepted, after decompression.
	ReadLimit int64
	// Compression accepts permessage-deflate (RFC 7692) when the client
	// offers it, without context takeover.
	Compression bool
	// OriginPatterns are the hosts, besides the request's own, that a
	// request's Origin header may name (path.Match patterns).
	OriginPatterns []string
}

func compression(on bool) websocket.CompressionMode {
	if on {
		// No context takeover: no per-connection compression state, which
		// matters for a CSMS with many mostly idle connections.
		return websocket.CompressionNoContextTakeover
	}
	return websocket.CompressionDisabled
}

// Accept upgrades an HTTP request to a WebSocket. On failure it has written
// an HTTP error response with Connection: close, so that the connection
// is not kept for another request.
func Accept(w http.ResponseWriter, r *http.Request, o AcceptOptions) (Conn, error) {
	var subprotocols []string
	if o.Subprotocol != "" {
		subprotocols = []string{o.Subprotocol}
	}
	cw := closingWriter{w}
	var hw *hijackWriter
	var aw http.ResponseWriter = cw
	if h, ok := hijacker(w); ok {
		hw = &hijackWriter{closingWriter: cw, h: h}
		aw = hw
	}
	c, err := websocket.Accept(aw, r, &websocket.AcceptOptions{Subprotocols: subprotocols, CompressionMode: compression(o.Compression),
		OriginPatterns: o.OriginPatterns})
	if err != nil {
		return nil, err
	}
	ws := &wsConn{c: c}
	if hw != nil && hw.conn != nil {
		ws.raw = func() { _ = hw.conn.Close() }
	}
	if c.Subprotocol() != o.Subprotocol {
		_ = ws.Close(StatusPolicy, "subprotocol not negotiated")
		return nil, fmt.Errorf("transport: subprotocol %q not negotiated", o.Subprotocol)
	}
	if o.ReadLimit > 0 {
		c.SetReadLimit(o.ReadLimit)
	}
	// coder/websocket sets the extension it accepts on w's header.
	ws.compression = o.Compression && deflate(w.Header())
	return ws, nil
}

// CheckOrigin applies the Origin check of Accept (coder/websocket's): a
// request without Origin, or whose Origin names the request's host or a
// host matching one of patterns (path.Match, case-insensitive; a pattern
// with "://" matches scheme://host), passes. It lets a server refuse a
// cross-origin request before anything else looks at it.
func CheckOrigin(r *http.Request, patterns []string) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("transport: Origin %q is not a URL: %w", origin, err)
	}
	if strings.EqualFold(r.Host, u.Host) {
		return nil
	}
	for _, p := range patterns {
		target := u.Host
		if strings.Contains(p, "://") {
			target = u.Scheme + "://" + u.Host
		}
		ok, err := path.Match(strings.ToLower(p), strings.ToLower(target))
		if err != nil {
			return fmt.Errorf("transport: origin pattern %q: %w", p, err)
		}
		if ok {
			return nil
		}
	}
	return fmt.Errorf("transport: Origin %q is not allowed for host %q", origin, r.Host)
}

// closingWriter adds Connection: close to every response but a 101, so
// that a handshake refused by coder/websocket (a foreign Origin, a
// malformed upgrade request) ends the connection.
type closingWriter struct{ http.ResponseWriter }

func (w closingWriter) WriteHeader(status int) {
	if status != http.StatusSwitchingProtocols {
		w.Header().Set("Connection", "close")
	}
	w.ResponseWriter.WriteHeader(status)
	// Some frameworks' writers (gin's) only record the status until
	// WriteHeaderNow; coder/websocket calls it after its 101, but on w
	// itself, which hides it.
	if status == http.StatusSwitchingProtocols {
		if now, ok := w.ResponseWriter.(interface{ WriteHeaderNow() }); ok {
			now.WriteHeaderNow()
		}
	}
}

// Unwrap lets http.ResponseController reach the writer underneath.
func (w closingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// hijackWriter keeps the connection that coder/websocket hijacks, so that
// a closing handshake can be bounded by closing it.
type hijackWriter struct {
	closingWriter
	h    http.Hijacker
	conn net.Conn
}

func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, brw, err := w.h.Hijack()
	w.conn = c
	return c, brw, err
}

// hijacker finds the http.Hijacker underneath w as coder/websocket does:
// w itself, or through Unwrap.
func hijacker(w http.ResponseWriter) (http.Hijacker, bool) {
	for {
		switch t := w.(type) {
		case http.Hijacker:
			return t, true
		case interface{ Unwrap() http.ResponseWriter }:
			w = t.Unwrap()
		default:
			return nil, false
		}
	}
}

// deflate reports whether the handshake's Sec-WebSocket-Extensions
// header (the server's answer) holds permessage-deflate.
func deflate(h http.Header) bool {
	for _, v := range h.Values("Sec-WebSocket-Extensions") {
		for ext := range strings.SplitSeq(v, ",") {
			name, _, _ := strings.Cut(ext, ";")
			if strings.EqualFold(strings.TrimSpace(name), "permessage-deflate") {
				return true
			}
		}
	}
	return false
}

type wsConn struct {
	c *websocket.Conn
	// raw closes the underlying connection, which ends a closing handshake
	// that is not over within the bound; nil when the connection is out of
	// reach (Dial with a transport of its own).
	raw         func()
	compression bool
}

// bounded runs a closing handshake, closing the underlying connection when
// it has not ended within closeTimeout.
func (w *wsConn) bounded(code int, reason string) {
	if w.raw != nil {
		t := time.AfterFunc(closeTimeout(), w.raw)
		defer t.Stop()
	}
	_ = w.c.Close(websocket.StatusCode(code), reason)
}

func (w *wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	typ, p, err := w.c.Read(ctx)
	if err != nil {
		return nil, convert(err)
	}
	if typ != websocket.MessageText {
		w.bounded(StatusUnsupported, "OCPP-J uses text messages only")
		return nil, errors.New("transport: binary message received")
	}
	return p, nil
}

func (w *wsConn) WriteMessage(ctx context.Context, p []byte) error {
	return convert(w.c.Write(ctx, websocket.MessageText, p))
}

// Close runs the closing handshake, bounded by closeTimeout; a Close of a
// connection that is already closing waits for that, within the same bound.
// Closing an already closed connection is not an error.
func (w *wsConn) Close(code int, reason string) error {
	w.bounded(code, reason)
	return nil
}

func (w *wsConn) Subprotocol() string { return w.c.Subprotocol() }

func (w *wsConn) Compression() bool { return w.compression }

// Abort closes the underlying connection first: coder/websocket's CloseNow
// of a connection that is already closing only waits for that close.
func (w *wsConn) Abort() {
	if w.raw != nil {
		w.raw()
	}
	_ = w.c.CloseNow()
}

func (w *wsConn) Ping(ctx context.Context) error { return convert(w.c.Ping(ctx)) }

func convert(err error) error {
	if err == nil {
		return nil
	}
	if code := websocket.CloseStatus(err); code != -1 {
		reason := ""
		if ce, ok := errors.AsType[websocket.CloseError](err); ok {
			reason = ce.Reason
		}
		return &CloseError{Code: int(code), Reason: reason}
	}
	return fmt.Errorf("%w: %w", ErrClosed, err)
}

// released returns the error of a Dial. When ctx ended as the handshake
// completed (ok is false: bound closed a connection), it closes c and says
// why; a failed handshake whose ctx ended is reported with ctx's error,
// which closing its connection may have hidden.
func released(ctx context.Context, c *websocket.Conn, err error, ok bool) error {
	if !ok && err == nil {
		_ = c.CloseNow()
		return fmt.Errorf("%w: %w", ErrClosed, ctx.Err())
	}
	if cerr := ctx.Err(); err != nil && !ok && !errors.Is(err, cerr) {
		return fmt.Errorf("%w: %w", err, cerr)
	}
	return err
}

// dialed holds the connections that a Dial's HTTP client opened.
type dialed struct {
	mu    sync.Mutex
	conns []net.Conn
	stops []func() bool
	tr    *http.Transport
}

// release stops closing the connections when Dial's ctx ends and reports
// whether none was closed so. A nil d reports true.
func (d *dialed) release() bool {
	if d == nil {
		return true
	}
	d.tr.CloseIdleConnections() // the copy is not used again
	d.mu.Lock()
	defer d.mu.Unlock()
	ok := true
	for _, stop := range d.stops {
		if !stop() {
			ok = false
		}
	}
	d.stops = nil
	return ok
}

// closer returns a function closing every connection Dial opened, nil for
// a nil d. One carries the WebSocket; the others (of redirects) are
// already closed.
func (d *dialed) closer() func() {
	if d == nil {
		return nil
	}
	return func() {
		d.mu.Lock()
		conns := d.conns
		d.mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}
}

// bound returns a copy of hc whose connections are closed when ctx ends,
// and the record of the connections it opens. hc is returned as it is, with
// a nil record, when its transport is not an *http.Transport, or dials with
// the deprecated Dial or DialTLS functions.
func bound(ctx context.Context, hc *http.Client) (*http.Client, *dialed) {
	if hc == nil {
		hc = http.DefaultClient
	}
	rt := hc.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	base, ok := rt.(*http.Transport)
	//lint:ignore SA1019 checked, so that a transport that dials with them is left alone
	if !ok || base.Dial != nil || base.DialTLS != nil { //nolint:staticcheck // SA1019: checked, see above
		return hc, nil
	}
	tr := base.Clone()
	d := &dialed{tr: tr}
	wrap := func(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
		return func(dctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(dctx, network, addr)
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			d.mu.Lock()
			d.conns = append(d.conns, conn)
			d.stops = append(d.stops, stop)
			d.mu.Unlock()
			return conn, nil
		}
	}
	dial := tr.DialContext
	if dial == nil {
		dial = new(net.Dialer).DialContext
	}
	tr.DialContext = wrap(dial)
	if tr.DialTLSContext != nil {
		tr.DialTLSContext = wrap(tr.DialTLSContext)
	}
	cp := *hc
	cp.Transport = tr
	return &cp, d
}
