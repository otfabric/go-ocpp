// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"sync"
)

// Pipe returns two connected in-memory Conns, for tests. A message written
// to one is read from the other. Writes block until the message is read,
// as a slow network would. Closing either end closes both; the other end
// then reads a *CloseError with the code and reason given.
func Pipe(subprotocol string) (Conn, Conn) {
	p := &pipe{done: make(chan struct{})}
	ab, ba := make(chan []byte), make(chan []byte)
	return &pipeEnd{p: p, in: ba, out: ab, sub: subprotocol}, &pipeEnd{p: p, in: ab, out: ba, sub: subprotocol}
}

type pipe struct {
	once   sync.Once
	done   chan struct{}
	code   int
	reason string
}

func (p *pipe) close(code int, reason string) {
	p.once.Do(func() {
		p.code, p.reason = code, reason
		close(p.done)
	})
}

type pipeEnd struct {
	p       *pipe
	in, out chan []byte
	sub     string
}

func (e *pipeEnd) ReadMessage(ctx context.Context) ([]byte, error) {
	// Messages written before the close are read first, as on a WebSocket,
	// where the close frame follows them on the same stream.
	select {
	case m := <-e.in:
		return m, nil
	default:
	}
	select {
	case m := <-e.in:
		return m, nil
	case <-e.p.done:
		return nil, &CloseError{Code: e.p.code, Reason: e.p.reason}
	case <-ctx.Done():
		e.p.close(StatusGoingAway, "")
		return nil, ErrClosed
	}
}

func (e *pipeEnd) WriteMessage(ctx context.Context, p []byte) error {
	m := append([]byte(nil), p...)
	select {
	case e.out <- m:
		return nil
	case <-e.p.done:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *pipeEnd) Close(code int, reason string) error {
	e.p.close(code, reason)
	return nil
}

func (e *pipeEnd) Subprotocol() string { return e.sub }

func (e *pipeEnd) Abort() { e.p.close(StatusGoingAway, "") }

// Ping answers at once while the pipe is open: an in-memory peer is never
// unreachable.
func (e *pipeEnd) Ping(ctx context.Context) error {
	select {
	case <-e.p.done:
		return ErrClosed
	default:
		return nil
	}
}
