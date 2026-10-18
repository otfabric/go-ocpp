// SPDX-License-Identifier: MIT

// Package transport is the WebSocket underneath the engine (ADR 0002): text
// messages in and out, a close with a status code, and the negotiated
// subprotocol. The coder/websocket implementation is used on the network;
// Pipe connects two endpoints in memory for tests.
package transport

import (
	"context"
	"errors"
)

// Close status codes of RFC 6455 §7.4.1 that go-ocpp sends.
const (
	StatusNormal        = 1000
	StatusGoingAway     = 1001
	StatusUnsupported   = 1003 // a binary message: OCPP-J uses text only
	StatusPolicy        = 1008
	StatusTooBig        = 1009
	StatusInternalError = 1011
	StatusTryAgainLater = 1013
)

// Conn is one WebSocket connection carrying OCPP-J frames.
//
// ReadMessage is called by one goroutine at a time, and so is WriteMessage.
// Close may be called concurrently with both and more than once.
type Conn interface {
	// ReadMessage returns the next text message. A binary message is an
	// error, and the connection is closed with StatusUnsupported. When ctx
	// ends the connection is closed.
	ReadMessage(ctx context.Context) ([]byte, error)
	// WriteMessage writes one text message.
	WriteMessage(ctx context.Context, p []byte) error
	// Close closes the connection with a status code and a reason.
	Close(code int, reason string) error
	// Subprotocol returns the negotiated subprotocol.
	Subprotocol() string
}

// ErrClosed is returned by ReadMessage and WriteMessage once the connection
// is closed.
var ErrClosed = errors.New("transport: connection closed")

// CloseError is returned by ReadMessage when the peer closed the connection
// with a close frame.
type CloseError struct {
	Code   int
	Reason string
}

// Error implements error.
func (e *CloseError) Error() string {
	if e.Reason == "" {
		return "transport: closed by peer with status " + itoa(e.Code)
	}
	return "transport: closed by peer with status " + itoa(e.Code) + ": " + e.Reason
}

// Unwrap returns ErrClosed.
func (e *CloseError) Unwrap() error { return ErrClosed }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
