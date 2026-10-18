// SPDX-License-Identifier: MIT

package ocpp

import (
	"context"
	"encoding/json"

	"github.com/otfabric/go-ocpp/internal/after"
)

// Role is one of the two OCPP roles.
type Role uint8

// The roles. OCPP 1.6 calls them Charge Point and Central System.
const (
	ChargingStation Role = iota + 1
	CSMS
)

// String returns "ChargingStation" or "CSMS".
func (r Role) String() string {
	switch r {
	case ChargingStation:
		return "ChargingStation"
	case CSMS:
		return "CSMS"
	default:
		return "Role(" + itoa(uint8(r)) + ")"
	}
}

// Caller sends a CALL and waits for its result.
//
// req is marshalled with encoding/json; a json.RawMessage is sent as is.
// The result payload is unmarshalled into resp, which must be a pointer
// (a *json.RawMessage receives the payload unchanged) or nil to discard it.
// A CALLERROR is returned as a *CallError.
type Caller interface {
	Call(ctx context.Context, action string, req, resp any) error
}

// Peer is the other end of one connection, as a handler sees it: the
// station's identity, the negotiated version, and a way to send it CALLs.
type Peer interface {
	// ID returns the charging station identity of the connection, on both
	// ends: the client's own identity, or the identity of the station a
	// server session belongs to.
	ID() string
	// Version returns the OCPP version negotiated for the connection.
	Version() Version
	Caller
}

// Dispatcher handles the CALLs that arrive on a connection of one OCPP
// version. The version packages build dispatchers from typed handlers;
// DispatcherFunc handles raw payloads.
//
// Dispatch returns the CALLRESULT payload, or an error: a *CallError is
// sent as is, any other error as an InternalError whose description does
// not reveal the error text.
type Dispatcher interface {
	Dispatch(ctx context.Context, p Peer, action string, payload json.RawMessage) (json.RawMessage, error)
}

// Sender sends a SEND (OCPP 2.1): a message that is never answered and may
// be sent while a CALL is outstanding (2.1 Part 4 §4.1.1, §4.2.4). The
// client and server sessions implement it.
type Sender interface {
	Send(ctx context.Context, action string, req any) error
}

// SendDispatcher is implemented by a Dispatcher that also handles incoming
// SEND messages (OCPP 2.1). They are never answered, so DispatchSend
// returns only an error, which is logged.
type SendDispatcher interface {
	DispatchSend(ctx context.Context, p Peer, action string, payload json.RawMessage) error
}

// DispatcherFunc adapts a function to a Dispatcher.
type DispatcherFunc func(ctx context.Context, p Peer, action string, payload json.RawMessage) (json.RawMessage, error)

// Dispatch calls f.
func (f DispatcherFunc) Dispatch(ctx context.Context, p Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	return f(ctx, p, action, payload)
}

// AfterResponse registers f to run once the response to the CALL being
// handled is on its way: for what a handler must do after answering, such
// as sending the NotifyReport messages after accepting GetBaseReport, the
// FirmwareStatusNotifications after accepting UpdateFirmware, or the
// triggered message after TriggerMessage. ctx must be the context the
// handler received; f runs on a goroutine of its own, after the response
// (CALLRESULT or CALLERROR) has been queued ahead of any CALL f sends. It
// reports false, and f does not run, when ctx is not a handler's context
// (a SEND handler's included: a SEND has no response).
func AfterResponse(ctx context.Context, f func()) bool { return after.Add(ctx, f) }
