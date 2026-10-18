# Errors

Errors are wrapped with detail. Test for them with `errors.Is` and
`errors.As`, never by comparing strings.

## Quick reference

| You want to know | Test |
|---|---|
| The peer answered with a CALLERROR | `errors.As(err, &ce)` with `ce *ocpp.CallError` |
| No response in time | `errors.Is(err, ocpp.ErrTimeout)` |
| The connection broke | `errors.Is(err, ocpp.ErrConnectionLost)` |
| It was closed locally | `errors.Is(err, ocpp.ErrClosed)` |
| A client could not connect | `errors.Is(err, ocpp.ErrConnectFailed)` |
| Too many calls waiting | `errors.Is(err, ocpp.ErrBusy)` |

## Sentinels

| Error | Returned when |
|---|---|
| `ErrClosed` | An operation is made on a client, server or session after `Close`; pending calls when `Close` is called; `Serve` after `Shutdown` |
| `ErrNotConnected` | A client without an open connection is asked to call, including while it reconnects. Nothing is queued |
| `ErrConnectFailed` | `Client.Connect` failed: dial, TLS, HTTP status (in the message), WebSocket handshake, or no common subprotocol (then it also wraps `ErrUnsupportedVersion`) |
| `ErrConnectionLost` | A call was pending or queued when the connection broke; `Session.Err` after the station went away |
| `ErrTimeout` | No response to a CALL arrived within the response timeout |
| `ErrBusy` | The queue of calls waiting for the single outgoing slot is full |
| `ErrWrongDirection` | A role sends an action that the specification does not let it send (enforced once the version packages exist) |
| `ErrUnsupportedVersion` | No common version could be negotiated |
| `ErrProtocol` | A response could not be decoded into `resp`; also reported through metrics for frames that violate OCPP-J |
| `ErrUnexpectedResponse` | Reported through `Metrics.OnProtocolError` for late, duplicate or unknown responses; never returned by a call |
| `ErrInvalidOption` | A constructor was given an invalid option |

Package `server` adds:

| Error | Meaning |
|---|---|
| `server.ErrReplaced` | `Session.Err` of a session replaced by a new connection of the station. It wraps `ocpp.ErrClosed` |
| `server.ErrUnauthorized` | What `server.BasicAuth` returns for refused credentials (logged, not sent) |

## CALLERROR from the peer

A CALLERROR is returned as `*ocpp.CallError`, carrying the code, the
description and the details. It is not a sentinel:

```go
var ce *ocpp.CallError
if errors.As(err, &ce) && ce.Code == ocpp.NotSupported { ... }
```

## CALLERROR to the peer

A handler returns `ocpp.NewCallError(code, description)` to answer with that
CALLERROR. Any other error, a panic, a nil response or a response that is not
a JSON object is answered with `InternalError` and an empty description. The
error itself is logged locally, because it may contain internal details. A
code the connection's version does not define is also replaced with
`InternalError`.

## What is not an error

Business rejections are responses, not errors. Examples are a
BootNotification answered with `Rejected` and an Authorize answered with
`Blocked`. A call returns them with a nil error.

## Context and timeouts

- Every `Call` has a response timeout (default 30 s). When it expires,
  `Call` returns `ErrTimeout` and the next CALL may be sent.
- When `ctx` ends first, `Call` returns `ctx.Err()` at once. If the CALL was
  already sent, it keeps the outgoing slot until its response arrives or its
  response timeout expires. OCPP allows one outstanding CALL per direction,
  so the next CALL waits for that.
- Waiting for the slot respects `ctx`. A queue that is already full fails at
  once with `ErrBusy`.
- `Connect` respects `ctx` and the handshake timeout, whichever ends first.

## Retries

The library retries nothing. A client with `WithReconnect` redials, but
requests that were pending when the connection broke fail with
`ErrConnectionLost`, and resending them is the application's decision. Many
OCPP requests are not idempotent.
