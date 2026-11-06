# Observability

go-ocpp logs through `log/slog`, reports metrics through callbacks, and lets
tracing hook in through interceptors. None of it needs a dependency, and
nothing is logged or measured unless the application asks.

## Logging

`client.WithLogger(*slog.Logger)` and `server.WithLogger(*slog.Logger)`.
Without them the library is silent.

| Level | What |
|---|---|
| Debug | Each call done or handled, with its duration; connections closed locally |
| Info | Listening, connected, disconnected, session replaced, connection lost; CALLERRORs a handler chose to send |
| Warn | Connect failures; refused connections; malformed frames; unexpected responses; a missing pong; too many malformed frames |
| Error | Handler errors and panics (answered with `InternalError`; a panic with its `panic` value and `stack`); handler error codes the version does not define; a panic in the server's onConnect session handler (the session goes on) |

Attributes: `component` (`ocpp.client` or `ocpp.server`, on every entry),
`station_id`, `ocpp_version`, `session_id`, `message_id`, `action`,
`direction`, `duration`, `error`, `error_code`, `remote`, `status`, `addr`,
`panic`, `stack`.

A handler panic is also reported to `Metrics.OnCallHandled` with an error
wrapping `ocpp.ErrHandlerPanic`.

Payloads, passwords and authorization headers are never logged. A test
checks that a password used for Basic authentication appears in no log line.

## Metrics

`WithMetrics(ocpp.Metrics)`. Callbacks are synchronous. They must be safe for
concurrent use, must not block, and must not call `Close`. Embed
`ocpp.NopMetrics` so that an implementation keeps compiling when callbacks
are added.

| Callback | When |
|---|---|
| `OnConnect(info)` | A connection opens |
| `OnConnectFailed(info, err)` | A client's connect fails, or the server refuses a connection (subprotocol, authentication, limits, handshake) |
| `OnDisconnect(info, err)` | An open connection ends; `err` is nil after a local close, `server.ErrReplaced` for a replaced session |
| `OnReconnectAttempt(info, n)` | Before a client's n-th redial |
| `OnCallDone(info, action, d, err)` | An outgoing CALL ends: nil, `*CallError`, `ErrTimeout`, a connection error, or a response that failed validation. An outgoing SEND (2.1) ends when it is queued |
| `OnCallHandled(info, action, d, err)` | An incoming CALL has been answered, or an incoming SEND (2.1) handled |
| `OnProtocolError(info, err)` | A malformed frame, a response matching no pending CALL (`ErrUnexpectedResponse`), or a CALLRESULTERROR received (2.1) |

`ConnInfo.Compression` says whether the connection's handshake negotiated
permessage-deflate (RFC 7692): with the client's `WithCompression`, when the
CSMS accepted it; at the server, when the station offered it. It is false
before the handshake completes (`OnConnectFailed`, `OnReconnectAttempt`).

Every callback receives the station identity in `ConnInfo`. Labelling by it
gives one series per station: the cardinality is the application's choice.
Two values are chosen by the peer, not the application: the `action` of an
incoming CALL or SEND (also an unknown one, answered with `NotImplemented`),
and the identity in `OnConnectFailed`, which a refused connection reports
before any authentication. Map actions outside the version's set (`ocpp16.Actions`
and the like) to a fixed value, and leave the identity out of failed-connection labels, or a hostile
peer can create any number of series ([LIMITATIONS.md](LIMITATIONS.md#bounds-left-to-the-application)).

## Tracing

`WithCallInterceptor` wraps every outgoing CALL, and `WithHandlerInterceptor`
the handling of every incoming one:

```go
func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
    ctx, span := tracer.Start(ctx, "ocpp "+info.Action)
    defer span.End()
    return next(ctx)
}
```

`CallInfo` carries the station, version, role, session, action, message ID
and direction. For incoming CALLs the action is the peer's: a span name built
from it has the cardinality the peer chooses (see Metrics).

## Frames

`WithFrameObserver(func(info, dir, frame))` sees every frame sent and
received, as raw JSON, on the connection's reader or writer goroutine. It is
meant for tracing tools such as ocppctl. It must not block or retain the
slice.

## Connection state

`client.WithStateHandler(func(state, err))` reports Connecting, Connected,
Disconnected (with the cause) and Closed. `server.WithSessionHandler` reports
sessions starting and ending.
