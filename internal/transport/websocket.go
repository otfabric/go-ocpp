// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
func Dial(ctx context.Context, url string, o DialOptions) (Conn, *http.Response, error) {
	c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient:      o.HTTPClient,
		HTTPHeader:      o.Header,
		Subprotocols:    o.Subprotocols,
		CompressionMode: compression(o.Compression),
	})
	if err != nil {
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
		var ce websocket.CloseError
		reason := ""
		if errors.As(err, &ce) {
			reason = ce.Reason
		}
		return &CloseError{Code: int(code), Reason: reason}
	}
	return fmt.Errorf("%w: %w", ErrClosed, err)
}
