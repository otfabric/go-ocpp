# Errors

Errors are wrapped with detail. Test for them with `errors.Is` and
`errors.As`, never by comparing strings. Which method returns which error
is listed per role in [API_CLIENT.md](API_CLIENT.md#errors-by-method) and
[API_SERVER.md](API_SERVER.md#errors-by-method).

## Contents

- [Quick reference](#quick-reference)
- [Sentinels](#sentinels)
- [Other packages](#other-packages)
- [CALLERROR codes by version](#callerror-codes-by-version)
- [CALLERROR from the peer](#callerror-from-the-peer)
- [CALLERROR to the peer](#callerror-to-the-peer)
- [Validation errors](#validation-errors)
- [Malformed frames](#malformed-frames)
- [CALLRESULTERROR (OCPP 2.1)](#callresulterror-ocpp-21)
- [Refused connections](#refused-connections)
- [WebSocket close codes](#websocket-close-codes)
- [What is not an error](#what-is-not-an-error)
- [Context and timeouts](#context-and-timeouts)
- [Retries](#retries)

## Quick reference

| You want to know | Test |
|---|---|
| The peer answered with a CALLERROR | `errors.As(err, &ce)` with `ce *ocpp.CallError` |
| No response in time | `errors.Is(err, ocpp.ErrTimeout)` |
| The connection broke | `errors.Is(err, ocpp.ErrConnectionLost)` |
| It was closed locally | `errors.Is(err, ocpp.ErrClosed)` |
| A client could not connect | `errors.Is(err, ocpp.ErrConnectFailed)` |
| A payload breaks its schema | `errors.As(err, &ve)` with `ve *ocpp.ValidationError` (wraps `ocpp.ErrInvalidMessage`) |
| Too many calls waiting | `errors.Is(err, ocpp.ErrBusy)` |
| The action is not one the role sends | `errors.Is(err, ocpp.ErrWrongDirection)` |
| The version does not match the connection | `errors.Is(err, ocpp.ErrUnsupportedVersion)` |
| A session was replaced by a reconnect | `errors.Is(err, server.ErrReplaced)` |
| The peer closed on purpose, not lost | `errors.As(err, &ce)` with `ce *ocpp.CloseError`, then `ce.Normal()` (1000, 1001) |
| The HTTP status of a refused connect | `errors.As(err, &he)` with `he *client.HandshakeError`, then `he.StatusCode` |
| A handler panicked (metrics) | `errors.Is(err, ocpp.ErrHandlerPanic)` |

## Sentinels

| Error | Returned when |
|---|---|
| `ErrClosed` | An operation is made on a client, server or session after `Close`; pending calls when `Close` is called; `Serve` after `Shutdown` |
| `ErrNotConnected` | A client without an open connection is asked to call, including while it reconnects. Nothing is queued |
| `ErrConnectFailed` | `Client.Connect` failed: dial, TLS, HTTP status (in the message), WebSocket handshake, or no common subprotocol (then it also wraps `ErrUnsupportedVersion`) |
| `ErrConnectionLost` | A call was pending or queued when the connection broke; `Session.Err` after the station went away |
| `ErrTimeout` | No response to a CALL arrived within the response timeout |
| `ErrBusy` | The queue of calls waiting for the single outgoing slot is full |
| `ErrWrongDirection` | A call function is used for an action the connection's role does not send |
| `ErrUnsupportedVersion` | No common version could be negotiated; a call function of another version's package was used on the connection; `Send` on a connection that is not OCPP 2.1 |
| `ErrProtocol` | A response could not be decoded into `resp`; also reported through metrics for frames that violate OCPP-J |
| `ErrUnexpectedResponse` | Reported through `Metrics.OnProtocolError` for late, duplicate or unknown responses; never returned by a call |
| `ErrInvalidMessage` | A request (before sending) or a response (on arrival) does not conform to its schema; wrapped by every `*ValidationError`. A response failing validation is also wrapped in `ErrProtocol`; on OCPP 2.1 it is also answered with a CALLRESULTERROR |
| `ErrInvalidOption` | A constructor was given an invalid option |
| `ErrHandlerPanic` | Reported through `Metrics.OnCallHandled` when a handler panicked; the CALL is answered with `InternalError`. Never returned by a call |

Package `server` adds:

| Error | Meaning |
|---|---|
| `server.ErrReplaced` | `Session.Err` of a session replaced by a new connection of the station. It wraps `ocpp.ErrClosed` |
| `server.ErrUnauthorized` | What `server.BasicAuth` returns for refused credentials (HTTP 401; logged, not sent) |
| `server.ErrUnknownStation` | An authenticator's error for an identity it does not know (HTTP 404) |

## Other packages

| Error | Package | Meaning |
|---|---|---|
| `ocppj.ErrMalformed` | `ocppj` | Wrapped by every `Decode` error, a `*ocppj.DecodeError` that says how far decoding got (`Type`, `ID`, `IDKnown`, `UnknownType`, `Reason`) |
| `ocppcert.ErrUnsupportedHash` | `ocppcert` | A hash other than SHA-256, SHA-384 and SHA-512, the ones HashAlgorithmEnumType defines |

Typed errors, not sentinels:

| Type | Package | Carries |
|---|---|---|
| `*ocpp.CallError` | `ocpp` | A CALLERROR: `Code`, `Description`, `Details` |
| `*ocpp.ValidationError` | `ocpp` | A schema violation: `Code`, `Field`, `Reason` |
| `*ocpp.CloseError` | `ocpp` | How the peer closed: `Code`, `Reason`, `Normal()`; wrapped next to `ErrConnectionLost` |
| `*client.HandshakeError` | `client` | The HTTP status of a refused handshake, `StatusCode`; wrapped next to `ErrConnectFailed` |
| `*server.StatusError` | `server` | Returned by an authenticator to refuse with its own HTTP status (`Code`, `Err`) |
| `*ocppj.DecodeError` | `ocppj` | How far a malformed frame decoded |

## CALLERROR codes by version

The codes a version defines (OCPP-J 1.6 §4.2.3; 2.0.1 and 2.1 Part 4
§4.3), and when go-ocpp sends each. `ocppj.ErrorCodes(v)` lists them; a
handler's `*ocpp.CallError` with a code its version does not define is sent
as `InternalError`.

| Code | 1.6 | 2.0.1, 2.1 | go-ocpp sends it for |
|---|---|---|---|
| `NotImplemented` | yes | yes | An action the version does not define; a dispatcher-less connection; `Unimplemented…Handler` is `NotSupported` instead |
| `NotSupported` | yes | yes | An action the version defines but the handler does not implement, or that the role does not receive; a SEND action sent as a CALL (2.1) |
| `InternalError` | yes | yes | A handler error that is not a `*ocpp.CallError`, a panic, a nil or non-object response, a code the version does not define |
| `ProtocolError` | yes | yes | Only when a handler returns it |
| `SecurityError` | yes | yes | Only when a handler returns it |
| `FormationViolation` | yes | — | 1.6: a payload that is not valid JSON or has an unknown property where the schema closes the object; a malformed CALL |
| `FormatViolation` | — | yes | 2.x: the same as `FormationViolation` in 1.6 |
| `PropertyConstraintViolation` | yes | yes | A value out of its enumeration, too long, out of range, not a multiple, not a URI |
| `OccurenceConstraintViolation` (1.6 spelling) | yes | — | A required property missing; an array too short or too long |
| `OccurrenceConstraintViolation` | — | yes | The same in 2.x |
| `TypeConstraintViolation` | yes | yes | A value of the wrong JSON type, `null` where the schema has none, a bad date-time |
| `GenericError` | yes | yes | Only when a handler returns it |
| `MessageTypeNotSupported` | — | yes (deprecated by errata) | Never: frames of unknown types are dropped (SPECIFICATIONS.md T9) |
| `RpcFrameworkError` | — | yes | 2.x: a malformed CALL, or a frame whose message type cannot be read |

## CALLERROR from the peer

A CALLERROR is returned as `*ocpp.CallError`, carrying the code, the
description and the details. It is not a sentinel:

```go
var ce *ocpp.CallError
if errors.As(err, &ce) && ce.Code == ocpp.NotSupported { ... }
```

## CALLERROR to the peer

A handler returns `ocpp.NewCallError(code, description)`, or an
`*ocpp.CallError` with `Details` (a JSON object), to answer with that
CALLERROR; its details are sent as they are (`{}` when they are not a JSON
object). Any other error, a panic, a nil response or a response that is not
a JSON object is answered with `InternalError` and an empty description. The
error itself is logged locally, because it may contain internal details. A
code the connection's version does not define is also replaced with
`InternalError`.

## Validation errors

`*ocpp.ValidationError` says what is wrong with a payload: `Code` (the
CALLERROR code for the violation, spelt as the version spells it), `Field`
(a path such as `meterValue[0].sampledValue[1].measurand`) and `Reason`. A
dispatcher answers an invalid incoming request with that CALLERROR; a call
function returns it for an invalid request before anything is sent.

## Malformed frames

A frame that does not decode (SPECIFICATIONS.md T3, T9) is reported to
`Metrics.OnProtocolError` with an error wrapping `ocpp.ErrProtocol`, logged
at Warn, and then:

| Frame | 1.6 | 2.x |
|---|---|---|
| A corrupt CALL whose message ID can be read | CALLERROR `FormationViolation` with that ID | CALLERROR `RpcFrameworkError` with that ID |
| A frame whose message type or ID cannot be read | dropped | CALLERROR `RpcFrameworkError` with ID `"-1"` |
| An unknown message type (or 5, 6 on 1.6 and 2.0.1) | dropped | dropped |
| A malformed CALLRESULT or CALLERROR | dropped | dropped |

With `WithMaxBadMessages(n)` the connection is closed with 1008 after the
n-th malformed frame; its error wraps `ocpp.ErrConnectionLost` and
`ocpp.ErrProtocol`.

## CALLRESULTERROR (OCPP 2.1)

- Sent by go-ocpp when the response to one of its CALLs does not decode or
  fails validation: `[5, id, code, description, {}]`, with the code of the
  violation (`FormatViolation`, `OccurrenceConstraintViolation`, …). The
  call returns the `*ocpp.ValidationError`, wrapped in `ocpp.ErrProtocol`.
- Received: logged at Warn and reported to `Metrics.OnProtocolError`
  (wrapping `ocpp.ErrProtocol` and the `*ocpp.CallError`); the CALL it
  refers to has already completed, so no call returns it.

## Refused connections

The server answers a refused upgrade with an HTTP status; the client's
`Connect` returns `ocpp.ErrConnectFailed` wrapping a
`*client.HandshakeError` with the status.
Every refusal is reported to `Metrics.OnConnectFailed` and logged at Warn.

| HTTP | Why |
|---|---|
| 400 | A 2.x station identity that is not an identifierString of at most 48 characters without ":" |
| 401 | Authentication failed (`WWW-Authenticate: Basic`), or a `*server.StatusError` with a code outside 400–599 |
| any 4xx/5xx | A `*server.StatusError` from the authenticator: its code (e.g. 403 for a profile downgrade) |
| 403 | A browser Origin of another host, not allowed by `WithOriginPatterns` |
| 404 | No identity in the URL, or `server.ErrUnknownStation` from the authenticator |
| 409 | The identity is connected and the policy is `RejectNew` |
| 503 | `WithMaxStations` reached, or the server is shutting down |
| 101, then close 1008 | No OCPP subprotocol in common: the handshake completes without one and the connection is closed (OCPP-J 1.6 §3.1.2; 2.x Part 4 §3.2); the client's error also wraps `ocpp.ErrUnsupportedVersion` |

## WebSocket close codes

| Code | Sent when | The other end's error |
|---|---|---|
| 1000 | `Close` | `ocpp.ErrConnectionLost` with an `*ocpp.CloseError`, `Normal()` |
| 1001 | Server `Shutdown` or `Close` | `ocpp.ErrConnectionLost` with an `*ocpp.CloseError`, `Normal()` |
| 1003 | A binary message arrived: OCPP-J uses text only | `ocpp.ErrConnectionLost` |
| 1008 | No subprotocol in common; a session replaced; `WithMaxBadMessages` reached | `ocpp.ErrConnectionLost` |
| 1009 | A message over `WithMaxMessageSize`, after decompression | `ocpp.ErrConnectionLost` |
| 1011 | A read error the library cannot attribute | `ocpp.ErrConnectionLost` |
| none (aborted) | No pong within `WithPingInterval`; a write over `WithWriteTimeout` | `ocpp.ErrConnectionLost` |

On the closing side, `Session.Err` and the session handler report
`ocpp.ErrClosed` for a local close, `server.ErrReplaced` for a replaced
session, and an error wrapping `ocpp.ErrConnectionLost` otherwise.

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
