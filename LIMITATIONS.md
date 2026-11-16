# Limitations

What go-ocpp does not do, and what has not been verified. The per-action
status is in [COMPATIBILITY.md](COMPATIBILITY.md) and
[docs/compatibility](docs/compatibility), the test evidence in
[INTEROPERABILITY.md](INTEROPERABILITY.md), and planned features in
[FEATURES.md](FEATURES.md).

## Scope not implemented

- OCPP 1.6 SOAP, and OCPP 1.5 and older: out of scope.
- BootNotification, Heartbeat and StatusNotification are not sent by the
  library. They are application messages.
- No charging logic: the library does not start or stop charging, download
  firmware, store configuration or the device model, or keep local
  authorization lists ([ARCHITECTURE.md](ARCHITECTURE.md#what-the-library-does-not-do)).
- Nothing is persisted and nothing is queued while offline: `Call` fails at
  once with `ErrNotConnected` or `ErrConnectionLost`, and the application
  queues. An offline queue is planned and needs an ADR (FEATURES.md, item 13).
- No translation between OCPP versions.
- After SetNetworkProfile, the client does not switch URL or security profile
  or fall back along NetworkConfigurationPriority: the application creates a
  new `Client` (planned, FEATURES.md, item 5).
- No Local Controller role (planned, FEATURES.md, item 11). ocpp-interop
  v0.10.0 has one reference Local Controller (ocpp-kit's) to test against.
- Validation cannot be exempted per peer: an application that must accept a
  station that does not conform handles that action with a raw dispatcher.
- Operating a certificate store is left to the application: OCPP-level
  security messages are ordinary messages, and the library holds no
  certificates. `ocppcert` covers CertificateHashData only.

## Bounds left to the application

What a hostile or broken peer can still make cost, because the library does
not bound it. Each is a design choice not yet made, listed here so that an
application can bound it itself.

- **Peer-chosen metric and span labels.** The `action` of `OnCallHandled`
  and `CallInfo.Action` of an incoming CALL or SEND is the action name the
  peer sent, also when it is unknown and answered with `NotImplemented`.
  Likewise `ConnInfo.StationID` of `OnConnectFailed` is the identity a
  refused connection asked for, before any authentication. A peer that
  sends random action names, or connects with random identities, creates
  one series (or span name) per name. Applications that label by them
  should map names outside the version's action set (`ocpp16.Actions`,
  `ocpp201.Actions`, `ocpp21.Actions`) to a fixed value such as `unknown`, and label failed connections
  without the identity ([OBSERVABILITY.md](OBSERVABILITY.md#metrics)).
- **`WithMaxStations` counts registered sessions only.** Two kinds of
  connection that are still closing are not counted: a session replaced by
  a new connection of its identity (`ReplaceExisting`), until its close
  handshake ends, and a connection refused after the WebSocket upgrade
  because it offered no supported subprotocol. A peer that does not answer
  the close frame holds each for at most the closing-handshake bound (10 s),
  so the number of open connections can exceed `WithMaxStations` by what
  arrives in that time. The refused-subprotocol connections are also not
  waited for by `Shutdown`. Rate-limit connection attempts in front of the
  server (a reverse proxy, or a listener wrapper) when that matters.
- **No length bound on an OCPP 1.6 identity.** For 2.x the server refuses an
  identity that is not an identifierString of at most 48 characters (Part 4
  §3.1.1). For 1.6 it applies no bound of its own: the request line is
  bounded only by the HTTP server's header limit (`http.Server`
  `MaxHeaderBytes`, 1 MiB by default, which `Serve` uses; the
  application's own server when it mounts the `Server` as an
  `http.Handler`). The identity is in `ConnInfo`, the `Authenticator`'s
  `ConnectRequest` and every log entry of the connection. An `Authenticator`
  that refuses identities it does not know (`ErrUnknownStation`, 404)
  keeps such a connection from being registered, but not from being logged
  once.

- **Responses that match no CALL are not counted.** `WithMaxBadMessages`
  counts the frames that do not decode. A well-formed CALLRESULT or
  CALLERROR whose message ID matches no pending CALL (late, repeated or
  never asked for), and a CALLRESULTERROR (2.1), each produce one Warn log
  entry and one `Metrics.OnProtocolError` call, and count towards no limit:
  a connected peer can make the log grow at the rate at which it sends
  small frames. `WithIncomingRateLimit` does not slow them, since it
  applies to CALLs and SENDs. An application that needs a bound counts
  `OnProtocolError` per station (the errors wrap `ErrUnexpectedResponse`,
  or `ErrProtocol` with a `*CallError` for a CALLRESULTERROR) and closes
  the session, or limits the rate in its log handler.
- **Functions registered with `ocpp.AfterResponse` are not bounded or
  waited for.** Each handled CALL whose handler registers any gets one
  goroutine, which runs them and ends when they return. `Close`, `Shutdown`
  and a session's `Done` do not wait for it (such a function may call them),
  so these functions can still run after the session has ended, when
  everything they send fails at once. How long they run, and how many run
  at a time, is the application's: a function that waits should watch the
  session (`Done`) or a context of its own.
- **Pings say nothing while a connection's incoming queue is full.** A pong
  is read only while the connection is read, and reading waits while 16
  incoming CALLs and SENDs are queued behind a slow handler or
  `WithIncomingRateLimit`. Pings sent in that time are not counted as
  lost, so a connection that goes half open then is noticed only when
  reading goes on, or by `WithWriteTimeout` when something is written. The
  peer's own pings are not answered in that time either, and a peer that
  pings may close the connection from its side.

- **The client's closing handshake is bounded only with an `*http.Transport`.**
  The client closes a connection whose closing handshake does not end within
  10 s through the connection its HTTP client dialed. With `WithHTTPClient`
  and a transport that is not an `*http.Transport` (or one that dials with
  the deprecated `Dial` or `DialTLS` functions), the library has no handle on
  that connection, and a CSMS that stops in the middle of the closing
  handshake can hold `Close`.

## Platform differences

- **The range of an integer field is that of the platform's `int`.** The
  generated types hold every schema `integer` in a Go `int`: 64 bits on
  64-bit builds, 32 bits on 32-bit builds (`386`, `arm`, `mips`, `mipsle`).
  A number above 2,147,483,647 or below −2,147,483,648 decodes on a 64-bit
  build and is refused with `TypeConstraintViolation` on a 32-bit build, so
  a 64-bit and a 32-bit build can answer the same message differently. A
  number beyond 64 bits is refused on every build, and so is one beyond
  ±2^53 written with a zero fractional part (`9223372036854775807.0`).
  Outgoing values are not checked against a range: an application on a
  64-bit build can send a value that a peer holding integers in 32 bits
  refuses. What the OCA specification text defines for integers is to be
  checked ([ARCHITECTURE.md](ARCHITECTURE.md#implementation-choices)). An
  application that wants 32 bits on every build checks its integer fields
  in its handlers and before it sends them. The tests run with a 32-bit
  `int` as `386` only (nightly, `TestIntegerRange` pins the boundaries);
  the other 32-bit architectures are not tested.

## Verification

- Independently tested against the reference builds of
  [otfabric/ocpp-interop](https://github.com/otfabric/ocpp-interop), pinned by
  digest, as listed in [INTEROPERABILITY.md](INTEROPERABILITY.md#status).
  Interoperability does not establish conformance: implementations can share a
  misreading.
- Not covered by any independent implementation:
  - The station-initiated 2.1 messages that CitrineOS refuses have no second
    CSMS, and CitrineOS's answers to the others are checked only against the
    schema.
- Not tested against commercial charging stations or CSMS products.
- Certification: none. OCA certification has not been attempted, and go-ocpp
  does not claim it.

## Known deviations

Deviations of peers, not of go-ocpp, are listed with their upstream sources in
[INTEROPERABILITY.md](INTEROPERABILITY.md).
