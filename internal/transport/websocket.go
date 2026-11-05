// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

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
// Dial does not outlast ctx when the HTTP client's transport is an
// *http.Transport (http.DefaultClient's is): a copy of it closes the
// connections it opens once ctx ends, until Dial returns. coder/websocket
// reads the start of a broken 101 response under a 3 s timer of its own,
// which would otherwise hold Dial past ctx.
func Dial(ctx context.Context, url string, o DialOptions) (Conn, *http.Response, error) {
	hc, release := bound(ctx, o.HTTPClient)
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:      hc,
		HTTPHeader:      o.Header,
		Subprotocols:    o.Subprotocols,
		CompressionMode: compression(o.Compression),
	})
	if err = released(ctx, c, err, release()); err != nil {
		return nil, resp, err
	}
	if o.ReadLimit > 0 {
		c.SetReadLimit(o.ReadLimit)
	}
	return &wsConn{c: c}, resp, nil
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
// an HTTP error response.
func Accept(w http.ResponseWriter, r *http.Request, o AcceptOptions) (Conn, error) {
	var subprotocols []string
	if o.Subprotocol != "" {
		subprotocols = []string{o.Subprotocol}
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: subprotocols, CompressionMode: compression(o.Compression),
		OriginPatterns: o.OriginPatterns})
	if err != nil {
		return nil, err
	}
	if c.Subprotocol() != o.Subprotocol {
		_ = c.Close(StatusPolicy, "subprotocol not negotiated")
		return nil, fmt.Errorf("transport: subprotocol %q not negotiated", o.Subprotocol)
	}
	if o.ReadLimit > 0 {
		c.SetReadLimit(o.ReadLimit)
	}
	return &wsConn{c: c}, nil
}

type wsConn struct{ c *websocket.Conn }

func (w *wsConn) ReadMessage(ctx context.Context) ([]byte, error) {
	typ, p, err := w.c.Read(ctx)
	if err != nil {
		return nil, convert(err)
	}
	if typ != websocket.MessageText {
		_ = w.c.Close(StatusUnsupported, "OCPP-J uses text messages only")
		return nil, errors.New("transport: binary message received")
	}
	return p, nil
}

func (w *wsConn) WriteMessage(ctx context.Context, p []byte) error {
	return convert(w.c.Write(ctx, websocket.MessageText, p))
}

// Close runs the closing handshake, which coder/websocket bounds to a few
// seconds. Closing an already closed connection is not an error.
func (w *wsConn) Close(code int, reason string) error {
	_ = w.c.Close(websocket.StatusCode(code), reason)
	return nil
}

func (w *wsConn) Subprotocol() string { return w.c.Subprotocol() }

func (w *wsConn) Abort() { _ = w.c.CloseNow() }

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

// bound returns a copy of hc whose connections are closed when ctx ends,
// and release, which stops that and reports whether none was closed. hc is
// returned as it is when its transport is not an *http.Transport, or dials
// with the deprecated Dial or DialTLS functions.
func bound(ctx context.Context, hc *http.Client) (*http.Client, func() bool) {
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
		return hc, func() bool { return true }
	}
	tr := base.Clone()
	var mu sync.Mutex
	var stops []func() bool
	wrap := func(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
		return func(dctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(dctx, network, addr)
			if err != nil {
				return nil, err
			}
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			mu.Lock()
			stops = append(stops, stop)
			mu.Unlock()
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
	return &cp, func() bool {
		tr.CloseIdleConnections() // the copy is not used again
		mu.Lock()
		defer mu.Unlock()
		ok := true
		for _, stop := range stops {
			if !stop() {
				ok = false
			}
		}
		return ok
	}
}
