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
- No Local Controller role (planned, FEATURES.md, item 11); no reference stack
  implements it to test against.
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
  the close frame holds each for the WebSocket close timeouts (seconds),
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

## Verification

- Independently tested against the reference builds of
  [otfabric/ocpp-interop](https://github.com/otfabric/ocpp-interop), pinned by
  digest, as listed in [INTEROPERABILITY.md](INTEROPERABILITY.md#status).
  Interoperability does not establish conformance: implementations can share a
  misreading.
- Not covered by any independent implementation:
  - SEND and CALLRESULTERROR: no released reference implements them. This
    includes the 2.1 action NotifyPeriodicEventStream, which is a SEND.
  - Compression with go-ocpp as the CSMS against a second independent station.
  - 2.1 BatterySwap is exchanged in one direction only: no reference station
    can send it.
  - The station-initiated 2.1 messages that CitrineOS refuses have no second
    CSMS, and CitrineOS's answers to the others are checked only against the
    schema.
- Not tested against commercial charging stations or CSMS products.
- Certification: none. OCA certification has not been attempted, and go-ocpp
  does not claim it.

## Known deviations

Deviations of peers, not of go-ocpp, are listed with their upstream sources in
[INTEROPERABILITY.md](INTEROPERABILITY.md).
