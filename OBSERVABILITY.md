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
| Info | Connected, disconnected, session replaced, connection lost; CALLERRORs a handler chose to send |
| Warn | Connect failures; refused connections; malformed frames; unexpected responses |
| Error | Handler errors and panics (answered with `InternalError`); handler error codes the version does not define |

Attributes: `station_id`, `ocpp_version`, `session_id`, `message_id`,
`action`, `direction`, `duration`, `error`, `error_code`, `remote`,
`status`.

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
| `OnDisconnect(info, err)` | An open connection ends; `err` is nil after a local close |
| `OnReconnectAttempt(info, n)` | Before a client's n-th redial |
| `OnCallDone(info, action, d, err)` | An outgoing CALL ends: nil, `*CallError`, `ErrTimeout`, or a connection error |
| `OnCallHandled(info, action, d, err)` | An incoming CALL has been answered |
| `OnProtocolError(info, err)` | A malformed frame, or a response matching no pending CALL (`ErrUnexpectedResponse`) |

Every callback receives the station identity in `ConnInfo`. Labelling by it
gives one series per station: the cardinality is the application's choice.

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
and direction.

## Frames

`WithFrameObserver(func(info, dir, frame))` sees every frame sent and
received, as raw JSON, on the connection's reader or writer goroutine. It is
meant for tracing tools such as ocppctl. It must not block or retain the
slice.

## Connection state

`client.WithStateHandler(func(state, err))` reports Connecting, Connected,
Disconnected (with the cause) and Closed. `server.WithSessionHandler` reports
sessions starting and ending.
