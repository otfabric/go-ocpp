// SPDX-License-Identifier: MIT

package ocpp

import "errors"

// Sentinel errors shared by the client, the server and the version packages.
// They are usually wrapped with detail; test for them with errors.Is.
// ERRORS.md describes when each is returned.
var (
	// ErrClosed is returned by operations on a client, server or session
	// after Close.
	ErrClosed = errors.New("ocpp: closed")

	// ErrNotConnected is returned by a call on a client that has no open
	// connection, including while it reconnects. Nothing is queued.
	ErrNotConnected = errors.New("ocpp: not connected")

	// ErrConnectFailed is returned when a client cannot open a connection:
	// the dial, the TLS handshake, the WebSocket handshake or the
	// subprotocol negotiation failed.
	ErrConnectFailed = errors.New("ocpp: connect failed")

	// ErrConnectionLost is returned to calls that were pending or queued
	// when the connection broke.
	ErrConnectionLost = errors.New("ocpp: connection lost")

	// ErrTimeout is returned when no response to a CALL arrived in time.
	ErrTimeout = errors.New("ocpp: timeout")

	// ErrBusy is returned when the queue of calls waiting to be sent is
	// full. OCPP allows one outstanding CALL per direction.
	ErrBusy = errors.New("ocpp: busy")

	// ErrWrongDirection is returned when an action is sent by a role that
	// the specification does not let send it.
	ErrWrongDirection = errors.New("ocpp: action not sent in this direction")

	// ErrUnsupportedVersion is returned when no common OCPP version can be
	// negotiated, or when a version is used that was not configured.
	ErrUnsupportedVersion = errors.New("ocpp: unsupported version")

	// ErrProtocol is returned when the peer violates the OCPP-J framing.
	ErrProtocol = errors.New("ocpp: protocol error")

	// ErrInvalidOption is returned by constructors given an invalid option.
	ErrInvalidOption = errors.New("ocpp: invalid option")
)

// ErrUnexpectedResponse is reported through Metrics.OnProtocolError for a
// CALLRESULT or CALLERROR that matches no pending CALL: a late response
// after a timeout, a duplicate, or an unknown message ID.
var ErrUnexpectedResponse = errors.New("ocpp: unexpected response")
