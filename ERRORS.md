# Errors

Errors are wrapped with detail. Test for them with `errors.Is` and
`errors.As`, never by comparing strings.

## Sentinels

| Error | Returned when |
|---|---|
| `ErrClosed` | An operation is made on a client, server or session after `Close` |
| `ErrNotConnected` | A client without an open connection is asked to call, including while it reconnects. Nothing is queued |
| `ErrConnectionLost` | A call was pending or queued when the connection broke |
| `ErrTimeout` | No response to a CALL arrived within the response timeout |
| `ErrBusy` | The queue of calls waiting for the single outgoing slot is full |
| `ErrWrongDirection` | A role sends an action that the specification does not let it send |
| `ErrUnsupportedVersion` | No common version could be negotiated, or an unconfigured version was used |
| `ErrProtocol` | The peer violated the OCPP-J framing |
| `ErrInvalidOption` | A constructor was given an invalid option |

## CALLERROR from the peer

From Phase 2 on, a CALLERROR from the peer is returned as `*ocpp.CallError`,
which carries the error code, description and details. It is not a sentinel.
Use `errors.As` to get it.

## What is not an error

Business rejections are responses, not errors. Examples are a
BootNotification answered with `Rejected` and an Authorize answered with
`Blocked`. A call returns them with a nil error.

## Context and timeouts

To be completed in Phase 2. The design is in
[ARCHITECTURE.md](ARCHITECTURE.md#the-engine).
