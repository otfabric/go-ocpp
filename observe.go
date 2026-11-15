// SPDX-License-Identifier: MIT

package ocpp

import (
	"context"
	"time"
)

// State is the state of a client's connection.
type State uint8

// The client states.
const (
	Disconnected State = iota // not connected, and not trying to (yet)
	Connecting                // dialling or waiting to redial
	Connected                 // the WebSocket is open
	Closed                    // Close was called; final
)

// String returns the state's name.
func (s State) String() string {
	switch s {
	case Disconnected:
		return "Disconnected"
	case Connecting:
		return "Connecting"
	case Connected:
		return "Connected"
	case Closed:
		return "Closed"
	default:
		return "State(" + itoa(uint8(s)) + ")"
	}
}

// Direction says whether a frame or call was sent or received.
type Direction uint8

// The directions.
const (
	Outgoing Direction = iota + 1
	Incoming
)

// String returns "outgoing" or "incoming".
func (d Direction) String() string {
	switch d {
	case Outgoing:
		return "outgoing"
	case Incoming:
		return "incoming"
	default:
		return "Direction(" + itoa(uint8(d)) + ")"
	}
}

// ConnInfo identifies a connection in callbacks.
type ConnInfo struct {
	StationID string
	Version   Version // zero before the handshake completes
	Role      Role    // the local role
	SessionID string  // random, unique per connection; for correlating logs
	// Compression is true when the handshake negotiated permessage-deflate
	// (RFC 7692); false before the handshake completes.
	Compression bool
}

// CallInfo describes one CALL, for interceptors.
type CallInfo struct {
	ConnInfo
	Action    string
	MessageID string
	Direction Direction // Outgoing: a call this end sends; Incoming: one it handles
}

// Interceptor wraps an outgoing call (client and server option
// WithCallInterceptor) or the handling of an incoming one
// (WithHandlerInterceptor). It must call next exactly once, with ctx or a
// context derived from it, and return its error or an error of its own.
// This is where tracing spans start and end.
//
// A handler interceptor runs on the connection's dispatcher goroutine,
// around the handler of an incoming CALL or SEND. When it does not return
// normally it is recovered like a handler: logged with its stack, reported
// to Metrics.OnCallHandled as ErrHandlerPanic, a CALL is answered with
// InternalError, and the connection goes on. A call interceptor runs on
// the goroutine that calls Call or Send, and is not recovered: what it
// raises reaches that caller.
type Interceptor func(ctx context.Context, info CallInfo, next func(context.Context) error) error

// FrameObserver sees every OCPP-J frame sent or received, as raw JSON. It
// runs synchronously on the connection's reader or writer, so it must not
// block, and it must not retain or modify frame, which is borrowed for the
// call. It must not call Close, and it must return normally: it is not
// recovered.
type FrameObserver func(info ConnInfo, dir Direction, frame []byte)

// Metrics receives events for metrics. Callbacks are synchronous and must
// be safe for concurrent use and must not block. They run on the goroutine
// where the event happens: the connection's reader or dispatcher, the
// goroutine of a Call, Send, Connect or Close, the client's supervisor, or
// the HTTP request of a station's handshake. They must not call Close, and
// must return normally: they are not recovered.
//
// Every callback gets the station identity in ConnInfo. Whether it becomes
// a metric label is the implementation's choice; labelling by station gives
// one series per station.
//
// Embed NopMetrics so that an implementation keeps compiling when callbacks
// are added.
type Metrics interface {
	// OnConnect is called when a connection opens.
	OnConnect(info ConnInfo)
	// OnConnectFailed is called when a client's dial or a server's
	// handshake fails. info.Version is zero.
	OnConnectFailed(info ConnInfo, err error)
	// OnDisconnect is called when an open connection ends; err is nil after
	// a local Close.
	OnDisconnect(info ConnInfo, err error)
	// OnReconnectAttempt is called before a client redials.
	OnReconnectAttempt(info ConnInfo, attempt int)
	// OnCallDone is called once for every CALL handed to a connection, when
	// it ends, with the error its Call returns: nil, a *CallError,
	// ErrTimeout, ErrBusy, the error of the caller's context, a connection
	// error, or, for a response that does not decode or fails validation,
	// an error wrapping ErrProtocol. d is the round trip time, from when
	// the CALL took the outgoing slot; for a CALL that ended before it had
	// the slot, the time it waited. A Call that fails before it reaches a
	// connection is not reported: on a client without a connection or
	// after its Close, once Shutdown has begun, for a wrong direction or
	// version, or with a request that cannot be marshalled. An outgoing
	// SEND (OCPP 2.1) is reported when it is queued.
	OnCallDone(info ConnInfo, action string, d time.Duration, err error)
	// OnCallHandled is called when an incoming CALL has been answered, with
	// the handler's duration and its error.
	OnCallHandled(info ConnInfo, action string, d time.Duration, err error)
	// OnProtocolError is called for a frame that violates OCPP-J, and for a
	// response that matches no pending CALL (late or unknown).
	OnProtocolError(info ConnInfo, err error)
}

// NopMetrics implements Metrics and does nothing.
type NopMetrics struct{}

// OnConnect does nothing.
func (NopMetrics) OnConnect(ConnInfo) {}

// OnConnectFailed does nothing.
func (NopMetrics) OnConnectFailed(ConnInfo, error) {}

// OnDisconnect does nothing.
func (NopMetrics) OnDisconnect(ConnInfo, error) {}

// OnReconnectAttempt does nothing.
func (NopMetrics) OnReconnectAttempt(ConnInfo, int) {}

// OnCallDone does nothing.
func (NopMetrics) OnCallDone(ConnInfo, string, time.Duration, error) {}

// OnCallHandled does nothing.
func (NopMetrics) OnCallHandled(ConnInfo, string, time.Duration, error) {}

// OnProtocolError does nothing.
func (NopMetrics) OnProtocolError(ConnInfo, error) {}
