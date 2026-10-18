# Architecture

Status: **design (Phase 0)**. The packages below describe what is to be
built. The API sketches are the target for Phase 2 and may change while it is
implemented. Decisions that are hard to reverse are recorded as ADRs in
[docs/adr](docs/adr).

## Contents

- [Layers](#layers)
- [Packages and import rules](#packages-and-import-rules)
- [Versions](#versions)
- [The OCPP-J codec](#the-ocpp-j-codec)
- [The engine](#the-engine)
- [Messages, handlers and calls](#messages-handlers-and-calls)
- [Client: the Charging Station role](#client-the-charging-station-role)
- [Server: the CSMS role](#server-the-csms-role)
- [Security](#security)
- [Observability](#observability)
- [Testing](#testing)
- [What the library does not do](#what-the-library-does-not-do)
- [Normative state transitions](#normative-state-transitions)

## Layers

```text
 application handlers and calls           (charging policy: not in this module)
──────────────────────────────────────────
 ocpp16 / ocpp201 / ocpp21                typed messages, validation,
                                          handler interfaces, call functions
──────────────────────────────────────────
 client            server                 the two roles: connecting, accepting,
                                          authentication, reconnect, sessions
──────────────────────────────────────────
 internal/engine                          one RPC endpoint per connection:
                                          correlation, scheduling, timeouts,
                                          dispatch, lifecycle
──────────────────────────────────────────
 ocppj                                    OCPP-J frames: encode, decode, limits
──────────────────────────────────────────
 internal/transport                       WebSocket messages (ADR 0002)
```

Both roles are built from the same engine. The only difference between a
client connection and a server session is which side opened the WebSocket.
After the handshake, both sides can send CALLs and both must answer them.

```text
Charging Station                      CSMS
  client.Client                         server.Session
       │──── WebSocket upgrade ───────────▶│
       │──── CALL BootNotification ───────▶│  handler: CSMS role
       │◀─── CALLRESULT ───────────────────│
       │◀─── CALL GetVariables ────────────│  session.Call
       │──── CALLRESULT ──────────────────▶│  handler: station role
```

## Packages and import rules

```text
go-ocpp/
├── *.go               package ocpp: Version, Peer, Caller, Dispatcher, CallError,
│                      sentinel errors, Metrics, interceptors, State
├── ocppj/             OCPP-J frame codec; error codes per version
├── ocpp16/            OCPP 1.6J messages, enums, validation, handlers (generated + hand-written)
├── ocpp201/           OCPP 2.0.1, same shape
├── ocpp21/            OCPP 2.1, same shape; reuses nothing from ocpp201 at the type level (ADR 0001)
├── client/            Charging Station role
├── server/            CSMS role
├── internal/
│   ├── engine/        the RPC endpoint shared by client and server
│   ├── transport/     the WebSocket adapter (ADR 0002)
│   └── gen/           the schema-to-Go generator (go run ./internal/gen)
├── e2e/               this module's client against this module's server
├── interop/           against otfabric/ocpp-interop images (build tag interop)
├── examples/          station16, csms16, station201, csms201, ...
└── docs/adr/          architecture decision records
```

The import rules are checked by a test in Phase 2:

- The root package `ocpp` imports only the standard library.
- `ocppj` imports only `ocpp` and the standard library.
- `ocpp16`, `ocpp201` and `ocpp21` import `ocpp` and `ocppj`, and never
  `client`, `server` or each other.
- `client` and `server` import `ocpp`, `ocppj` and `internal/...`. They do not
  import any version package. A version is plugged in as an
  `ocpp.Dispatcher` (see below).
- Nothing imports `e2e`, `interop` or `examples`.

The version packages are named `ocpp16`, `ocpp201` and `ocpp21`, not `v16`,
`v201` and `v21`. A path element of the form `vN` looks like a Go module
major version suffix (`github.com/otfabric/go-ocpp/v16` would be a valid
path for major version 16 of this module), which confuses `go get` and
readers. See [ADR 0001](docs/adr/0001-package-layout.md). Each version is a
single package. Splitting messages and types into subpackages would only add
an import, because every message refers to the types.

## Versions

```go
package ocpp

type Version uint8

const (
    V16  Version = iota + 1 // subprotocol "ocpp1.6"
    V201                    // "ocpp2.0.1"
    V21                     // "ocpp2.1"
)

func (v Version) Subprotocol() string
func ParseSubprotocol(s string) (Version, bool)
```

A client offers a list of versions in order of preference. A server accepts
the versions it has handlers for and picks the first of the client's
offers that it supports. The chosen version is fixed for the life of the
connection and can be read from both ends (`Client.Version`,
`Session.Version`).

OCPP 1.6 and 2.x have different application models: connectors versus
EVSEs and connectors, Start/StopTransaction versus TransactionEvent,
configuration keys versus the component/variable device model. Each version
package uses its own specification's terms and types. Nothing translates one
version into another.

## The OCPP-J codec

Package `ocppj` is the frame codec. It does no I/O and can be fuzzed on its
own.

```go
package ocppj

type MessageType uint8 // Call=2, CallResult=3, CallError=4, CallResultError=5, Send=6

type Frame struct {
    Type        MessageType
    ID          string          // message ID
    Action      string          // Call, Send
    Payload     json.RawMessage // Call, CallResult, Send
    ErrorCode   string          // CallError, CallResultError
    Description string
    Details     json.RawMessage
}

type Limits struct{ MaxFrameSize int } // checked before parsing

func Decode(data []byte, v ocpp.Version, l Limits) (Frame, error)
func Encode(f Frame) ([]byte, error)
```

Decoding enforces what the version allows. For example, types 5 and 6 are
errors on a 1.6 or 2.0.1 connection, and message IDs are limited to 36
characters. A decode error says whether the message ID could still be
recovered, because that decides whether the engine can answer with a
CALLERROR or can only log and drop the frame (SPECIFICATIONS.md T9). Error
codes are typed per version, because 1.6 and 2.x differ:
`FormationViolation` in 1.6 is `FormatViolation` in 2.x, and 2.x adds codes
such as `MessageTypeNotSupported` and `RpcFrameworkError`.

The payload stays `json.RawMessage` at this layer. Typed decoding and
validation belong to the version packages.

## The engine

`internal/engine` implements one RPC endpoint over one WebSocket connection.
`client.Client` owns one engine at a time (a new one after each reconnect).
`server.Session` owns exactly one.

```text
             ┌──────────── engine.Endpoint ──────────────┐
 transport ─▶│ reader ──▶ responses ──▶ pending call (≤1) │──▶ Call returns
             │    │                                       │
             │    └────▶ incoming CALLs ──▶ dispatcher ───│──▶ ocpp.Dispatcher
 transport ◀─│ writer ◀── outbound queue (bounded) ◀──────│◀── Call / results
             └───────────────────────────────────────────┘
```

- **Reader.** One goroutine reads frames. It checks the size limit, decodes
  the frame, then routes it. Responses go to the pending call. Incoming CALLs
  and SENDs go to the dispatcher. Malformed frames get a CALLERROR or are
  dropped, as the version requires.
- **Writer.** One goroutine writes, so the transport is never written
  concurrently. Its queue is bounded. Results of incoming CALLs take priority
  over new outgoing CALLs.
- **Outgoing CALLs.** At most one CALL is outstanding at a time (T6). Other
  callers wait in a bounded queue, in FIFO order, until the slot is free,
  their context ends, or the endpoint closes. A full queue returns `ErrBusy`.
  SEND frames (2.1) do not take the slot (T5). The slot is released when the
  response arrives, the request times out, or the connection closes.
- **Timeouts.** Each CALL has a response timeout (default 30 s, configurable
  per client or server). A context deadline shortens it but never lengthens
  it. A CALL that times out returns `ErrTimeout`. A response that arrives
  later is dropped with a log entry and a metric (R4.5).
- **Incoming CALLs.** Incoming CALLs are dispatched one at a time, in arrival
  order, on a goroutine separate from the reader. The reader therefore keeps
  delivering responses while a handler runs. This prevents deadlock when a
  handler sends a CALL of its own: for example, a CSMS that sends
  GetConfiguration from inside its BootNotification handler. The handler
  context is cancelled when the connection closes.
- **Lifecycle.** States are Connecting, Open, Closing and Closed. `Close` is
  idempotent and returns after the goroutines have exited. Pending and
  queued calls then fail with `ErrConnectionLost` (on a broken connection)
  or `ErrClosed` (after `Close`). A `Close` called from a handler does not
  wait for that handler.

The default 30 s timeout is an implementation choice. OCPP 2.x lets the
CSMS configure the station's message timeout through the device model
(`OCPPCommCtrlr.MessageTimeout`). The engine does not read the device model;
the application passes the value in as an option.

## Messages, handlers and calls

The root package defines the small, version-neutral contracts that connect
the layers:

```go
package ocpp

// Peer is the other end of one connection, as seen from a handler.
type Peer interface {
    ID() string            // station identity
    Version() Version
    Caller
}

// Caller sends a CALL and waits for its result. req and resp are values
// of a version package (or json.RawMessage for raw use).
type Caller interface {
    Call(ctx context.Context, action string, req, resp any) error
}

// Dispatcher handles incoming CALLs of one version.
type Dispatcher interface {
    Dispatch(ctx context.Context, p Peer, action string, payload json.RawMessage) (json.RawMessage, error)
}

// CallError is a CALLERROR received or to be sent.
type CallError struct {
    Code        string
    Description string
    Details     json.RawMessage
}
```

Each version package generates the following from the schemas:

1. A request and response type for every action, with a `Validate() error`
   method and JSON tags that match the schema. Enums are named string types
   with constants. Optional fields are pointers or `omitempty`, according to
   the schema.
2. One typed call function per action, which validates the request, calls
   and validates the response:

   ```go
   resp, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{
       ChargePointVendor: "OTFabric",
       ChargePointModel:  "TestStation",
   })
   ```

3. One handler interface per role, with one method per action that the role
   receives. There is also an `Unimplemented...` struct to embed, which
   answers `NotImplemented` for methods the application does not override.
   This keeps an application compiling when a later version of the library
   adds a method:

   ```go
   type CentralSystemHandler interface {
       BootNotification(ctx context.Context, p ocpp.Peer, req *BootNotificationRequest) (*BootNotificationResponse, error)
       Heartbeat(ctx context.Context, p ocpp.Peer, req *HeartbeatRequest) (*HeartbeatResponse, error)
       // ... every Charge Point → Central System action
   }

   func CentralSystem(h CentralSystemHandler, opts ...DispatchOption) ocpp.Dispatcher
   func ChargePoint(h ChargePointHandler, opts ...DispatchOption) ocpp.Dispatcher
   ```

   For 2.x the roles are `CSMSHandler` and `ChargingStationHandler`.
4. The action table and the coverage manifest: actions, direction, request
   and response types, and test status.

A handler returns either a response or an error. A `*ocpp.CallError` is sent
as is. Any other error is sent as `InternalError` with a generic
description, so that internal details do not leak to the peer, and is logged
with its full text. A nil response with a nil error is also sent as
`InternalError`.

The direction of every action is known. The engine refuses to send an action
in the wrong direction, for example a station sending `RemoteStartTransaction`,
and returns `ErrWrongDirection`.

Vendor extensions use what the protocol defines: DataTransfer in every
version, and `customData` in 2.x. `customData` is kept as raw JSON on every
generated type. Raw use (`json.RawMessage` request and response, and a raw
`Dispatcher`) is supported for tools like ocppctl, a proxy, or an
application handling DataTransfer itself.

Validation is generated as code, not interpreted from schemas at runtime
([ADR 0003](docs/adr/0003-schema-generation.md)). It is on by default for
incoming and outgoing messages. A `DispatchOption` and a client or server
option can exempt named actions, never everything, and each exemption is
logged once at start-up.

## Client: the Charging Station role

```go
c, err := client.New("wss://csms.example.com/ocpp/STATION001",
    client.WithVersions(ocpp.V201, ocpp.V16),           // offered in this order
    client.WithHandler(ocpp.V201, ocpp201.ChargingStation(h201)),
    client.WithHandler(ocpp.V16, ocpp16.ChargePoint(h16)),
    client.WithBasicAuth("STATION001", password),       // profile 2 with wss
    client.WithReconnect(client.Reconnect{MinDelay: time.Second, MaxDelay: 2 * time.Minute}),
    client.WithStateHandler(func(s ocpp.State, err error) { ... }),
    client.WithLogger(logger),
)
if err != nil { return err }
if err := c.Connect(ctx); err != nil { return err }
defer c.Close()

resp, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{...})
```

- The station identity is the last path segment of the URL (T2), and is
  checked when `New` is called.
- `Connect` dials, negotiates and returns when the connection is open or
  `ctx` ends. With reconnect enabled, a dropped connection is re-dialled
  with exponential backoff and full jitter between `MinDelay` and `MaxDelay`.
  `Close` stops this.
- While disconnected, `Call` fails immediately with `ErrNotConnected`. The
  library has no queue of its own. The state handler tells the application
  when to stop and resume sending, and when to send BootNotification again.
  A pluggable offline queue will be added only if the implementation phases
  show a design that is common to most applications.
- The client implements `ocpp.Caller` and `ocpp.Peer`, so call functions
  accept it directly.

## Server: the CSMS role

```go
srv, err := server.New(
    server.WithHandler(ocpp.V16, ocpp16.CentralSystem(h16)),
    server.WithHandler(ocpp.V201, ocpp201.CSMS(h201)),
    server.WithAuthenticator(auth),                     // runs before the upgrade
    server.WithSessionHandler(onConnect, onDisconnect),
    server.WithMaxStations(1000),
)
http.Handle("/ocpp/", srv)                             // srv is an http.Handler

station, ok := srv.Station("STATION001")
if ok {
    _, err := ocpp201.Reset(ctx, station, &ocpp201.ResetRequest{Type: ocpp201.ResetEnumTypeImmediate})
}
```

- `Server` implements `http.Handler`. It takes the station identity from the
  last path segment, so it can be mounted under any prefix. `ListenAndServe`
  and `Serve(net.Listener)` are provided for applications without an HTTP
  server.
- The versions offered are those that have a handler. A handshake without a
  supported subprotocol is refused with an HTTP error before the upgrade.
- The authenticator receives the identity, the Basic credentials if present,
  and the TLS connection state. It decides before the upgrade. A server must
  be given an authenticator, or `WithoutAuthentication()` (which exists for
  tests and closed networks and has that name for that reason). This
  prevents an open CSMS by omission.
- Each session is a `*server.Session` that implements `ocpp.Peer`.
  `Station(id)` and `Stations()` look sessions up.
- Session replacement: when an authenticated connection arrives for an
  identity that already has a session, the default `ReplaceExisting` policy
  closes the old session first and then registers the new one. Stations
  commonly reconnect before the CSMS notices that the old TCP connection is
  dead, and because authentication runs first, an unauthenticated client
  cannot use this to evict a station. `RejectNew` is the alternative. Under
  no policy are two sessions active for one identity. Whether a newer edition
  specifies this behaviour is to be checked (SPECIFICATIONS.md S4).
- Limits: the maximum number of stations, the frame size (default 64 KiB,
  overridable per version), the handshake timeout and the response timeout.
  When a limit is hit, the request is refused or the connection closed, never
  buffered without bound.
- `Shutdown(ctx)` stops accepting connections and closes sessions with a
  normal close. It returns when they have ended or `ctx` ends. `Close` does
  not wait.

## Security

| Profile | Transport | Authentication | Configuration |
|---|---|---|---|
| 1 | `ws://` | HTTP Basic | client: `WithInsecureTransport()`; server: plain listener |
| 2 | `wss://` | HTTP Basic, server certificate | default |
| 3 | `wss://` | client certificate (mutual TLS) | `WithTLS(cfg)` with certificates |

- `ws://` URLs are refused unless `WithInsecureTransport()` is given.
- The library never sets `InsecureSkipVerify`. A `tls.Config` supplied by the
  application is used as given, with a minimum of TLS 1.2.
- Basic credentials are not logged and do not appear in `error` strings. The
  type holding them has a redacting `String` method.
- For certificate rotation, `WithTLS` accepts a `tls.Config` whose
  `GetCertificate`, `GetClientCertificate` and `VerifyPeerCertificate`
  callbacks can change at runtime. The library holds no certificate store.
- OCPP-level security messages (SignCertificate, CertificateSigned,
  InstallCertificate, SecurityEventNotification and others) are ordinary
  actions. The library transports and validates them. Generating keys and
  storing certificates are left to the application.

[SECURITY.md](SECURITY.md) has the deployment guidance.

## Observability

- **Logging.** `WithLogger(*slog.Logger)`. A nil logger is silent. Log
  attributes include `station_id`, `ocpp_version`, `session_id`,
  `message_id`, `action`, `direction`, `duration` and `error_code`. Payloads
  are logged only at debug level, and only when the application turns
  payload logging on.
- **Metrics.** `WithMetrics(ocpp.Metrics)`, a callback interface. Embedding
  `ocpp.NopMetrics` lets an implementation compile when callbacks are added.
  Callbacks receive the station ID as an argument. Whether to label with it
  is the application's choice, which keeps cardinality under its control.
  The callbacks cover:
  - connections: established, failed, disconnected, reconnect attempts;
  - CALLs sent and received, and their results and latency;
  - timeouts;
  - protocol errors and validation failures;
  - late and unknown responses;
  - the depth of the pending queue.
- **Interceptors.** `WithCallInterceptor` and `WithHandlerInterceptor` wrap
  outgoing and incoming calls with the call's metadata. This is where an
  application starts and ends OpenTelemetry spans, without the library
  depending on OpenTelemetry.
- **Frame observer.** `WithFrameObserver` sees every frame sent and received.
  ocppctl's trace and replay features need it (R13).

Details will be in OBSERVABILITY.md once implemented (Phase 2).

## Testing

| Layer | How |
|---|---|
| `ocppj` | table tests with byte-level vectors; fuzzing of `Decode` |
| version packages | generated round-trip tests per message; valid and invalid payloads; fuzzing of each dispatcher |
| `internal/engine` | in-memory transport; ordering, timeouts, late responses, cancellation, close during a call; goroutine-leak check |
| `client`, `server` | `httptest` servers; handshake, auth, TLS, reconnect, session replacement, limits |
| `e2e` | every version, both directions, many concurrent stations, `-race` |
| `interop` | build tag `interop`; images of `otfabric/ocpp-interop` pinned by digest; both roles |

The interop harness follows go-iec104's: images are consumed only as
published (Makefile, `interop/harness.go` and the workflow carry the same
digests), and the harness reads `print-capabilities` and skips only
scenarios that an adapter declares unsupported. A skipped scenario is
reported as skipped, never as passed.

## What the library does not do

- It does not send BootNotification, Heartbeat or StatusNotification by
  itself. They are application messages. A helper to schedule heartbeats
  may be added if every application needs one.
- It does not start or stop charging, download firmware, store
  configuration or the device model, or keep local authorization lists.
- It does not persist anything, and does not queue messages while offline.
- It does not translate between OCPP versions.

## Normative state transitions

To be filled in as the engine is implemented. Each entry names the state
change, the version, and the section of the specification it follows.

| Transition | Versions | Source |
|---|---|---|
| Outgoing CALL slot released on response or timeout | all | T6 |
| Unknown message type: ignore, do not answer | 2.0.1, 2.1 | T9 (to confirm) |
| SEND neither answered nor counted against the CALL slot | 2.1 | T5 |
