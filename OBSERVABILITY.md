# Observability

go-ocpp logs through `log/slog`, reports metrics through callbacks, and lets
tracing hook in through interceptors. None of it needs a dependency, and
nothing is logged or measured unless the application asks.

## Logging

`client.WithLogger(*slog.Logger)` and `server.WithLogger(*slog.Logger)`.
Without them the library is silent.

| Level | What |
|---|---|
| Debug | Each call done or handled, with its duration; connections closed locally; a ping that is not counted because reading waits for the incoming queue |
| Info | Listening, connected, disconnected, session replaced, connection lost; CALLERRORs a handler chose to send |
| Warn | Connect failures; refused connections; malformed frames; unexpected responses; a missing pong; too many malformed frames |
| Error | Handler errors and panics (answered with `InternalError`; a panic with its `panic` value and `stack`); handler error codes the version does not define; a panic in the server's onConnect session handler (the session goes on) |
| Error | A handler interceptor, or a function registered with `ocpp.AfterResponse`, that does not return normally: recovered, and logged with the recovered value, the `stack`, and the `action` and `message_id` of the CALL (the connection goes on) |

Attributes: `component` (`ocpp.client` or `ocpp.server`, on every entry),
`station_id`, `ocpp_version`, `session_id`, `message_id`, `action`,
`direction`, `duration`, `error`, `error_code`, `remote`, `status`, `addr`,
`panic`, `stack`.

A handler panic is also reported to `Metrics.OnCallHandled` with an error
wrapping `ocpp.ErrHandlerPanic`.
So is a handler interceptor that does not return normally. A function
registered with `ocpp.AfterResponse` is logged only: its CALL has been
answered and reported by then.

Payloads, passwords and authorization headers are never logged. A test
checks that a password used for Basic authentication appears in no log line.

## Metrics

`WithMetrics(ocpp.Metrics)`. Callbacks are synchronous, on the goroutine
where the event happens (a connection's reader or dispatcher, the goroutine
of a `Call`, `Connect` or `Close`, the client's supervisor, the HTTP request
of a handshake). They must be safe for
concurrent use, must not block, must not call `Close`, and must return
normally: they are not recovered. Embed
`ocpp.NopMetrics` so that an implementation keeps compiling when callbacks
are added.

| Callback | When |
|---|---|
| `OnConnect(info)` | A connection opens |
| `OnConnectFailed(info, err)` | A client's connect fails, or the server refuses a connection (subprotocol, authentication, limits, handshake) |
| `OnDisconnect(info, err)` | An open connection ends; `err` is nil after a local close, `server.ErrReplaced` for a replaced session |
| `OnReconnectAttempt(info, n)` | Before a client's n-th redial |
| `OnCallDone(info, action, d, err)` | An outgoing CALL ends, once, with the error `Call` returns: nil, `*CallError`, `ErrTimeout`, `ErrBusy`, the error of the caller's context, a connection error, or, reported after the response is decoded, a response that failed validation (wrapping `ErrProtocol`). `d` is the time since the CALL took the outgoing slot, or the time it waited when it never had it. Not reported: a `Call` that fails before it reaches a connection (a client that is not connected or is closed, `Shutdown` begun, a wrong direction or version, a request that cannot be marshalled). An outgoing SEND (2.1) ends when it is queued |
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

A handler interceptor runs on the connection's dispatcher goroutine. When it
does not return normally it is recovered like a handler: the CALL is
answered with `InternalError`, and `OnCallHandled` gets `ErrHandlerPanic`. A
call interceptor runs on the goroutine that calls `Call` or `Send`, and is
not recovered.

`CallInfo` carries the station, version, role, session, action, message ID
and direction. For incoming CALLs the action is the peer's: a span name built
from it has the cardinality the peer chooses (see Metrics).

## Frames

`WithFrameObserver(func(info, dir, frame))` sees every frame sent and
received, as raw JSON, on the connection's reader or writer goroutine. It is
meant for tracing tools such as ocppctl. It must not block or retain the
slice, which is borrowed for the call, and it must return normally: it is
not recovered.

## Connection state

`client.WithStateHandler(func(state, err))` reports Connecting, Connected,
Disconnected (with the cause) and Closed. It is called with the client's
lock held, on the goroutine that changes the state, and must return
normally. `server.WithSessionHandler` reports
sessions starting and ending, on the goroutine of the station's HTTP
request; an onConnect that does not return normally is recovered and
logged.
