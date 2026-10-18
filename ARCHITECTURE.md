# Architecture

Status: **Phase 5**. The root package, `ocppj`, the engine, the transport,
`client`, `server`, and the version packages `ocpp16` (OCPP 1.6J with the
Security Whitepaper extensions) and `ocpp201` (OCPP 2.0.1) are implemented
as described here. `ocpp21` is Phase 6. Decisions that are hard to reverse are
recorded as ADRs in [docs/adr](docs/adr). Choices this document makes where
the specifications are silent are collected in
[Implementation choices](#implementation-choices).

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
- [Implementation choices](#implementation-choices)

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
├── ocpp16/            OCPP 1.6J messages, enums, validation, handlers (generated)
├── ocpp201/           OCPP 2.0.1, same shape
├── ocpp21/            OCPP 2.1, same shape (Phase 6); reuses nothing from ocpp201 at the type level (ADR 0001)
├── schemas/           the OCA JSON schemas, unmodified (CC BY-ND 4.0, ADR 0007)
├── client/            Charging Station role
├── server/            CSMS role
├── internal/
│   ├── engine/        the RPC endpoint shared by client and server
│   ├── transport/     the WebSocket adapter (ADR 0002)
│   ├── gen/           the schema-to-Go generator (go generate in each version package)
│   ├── wire/          the runtime of the generated code
│   └── schematest/    the schema conformance suite (tests only)
├── e2e/               this module's client against this module's server
├── interop/           against otfabric/ocpp-interop images (build tag interop)
├── examples/          station16, csms16, station201, csms201, ...
└── docs/adr/          architecture decision records
```

The import rules are checked by a test (`imports_test.go`):

- The root package `ocpp` imports only the standard library.
- `ocppj` imports only `ocpp` and the standard library.
- `ocpp16`, `ocpp201` and `ocpp21` import `ocpp` and `internal/wire` (which
  imports `ocppj`), and never `client`, `server` or each other.
- `internal/schematest`, the only importer of the reference JSON Schema
  validator, is imported by tests only.
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
    ErrorCode   ocpp.ErrorCode  // CallError, CallResultError
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
CALLERROR or can only log and drop the frame (SPECIFICATIONS.md T9). The
error codes are constants of type `ocpp.ErrorCode` in the root package;
`ocppj.ErrorCodes(v)` and `ocppj.ValidErrorCode(v, code)` say which ones a
version defines, because 1.6 and 2.x differ: `FormationViolation` in 1.6 is
`FormatViolation` in 2.x, 1.6 spells `OccurenceConstraintViolation` with one
"r", and 2.x adds `MessageTypeNotSupported` and `RpcFrameworkError`.

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
  per client or server). A CALL that times out returns `ErrTimeout`, and the
  slot is free again. A response that arrives later is dropped with a log
  entry and a metric (R4.5). When the caller's context ends first, `Call`
  returns `ctx.Err()` at once, but the CALL already on the wire keeps the
  slot until its response or its response timeout, as T6 requires.
- **Incoming CALLs.** Incoming CALLs are dispatched one at a time, in arrival
  order, on a goroutine separate from the reader. The reader therefore keeps
  delivering responses while a handler runs. This prevents deadlock when a
  handler sends a CALL of its own: for example, a CSMS that sends
  GetConfiguration from inside its BootNotification handler. The handler
  context is cancelled when the connection closes.
- **Lifecycle.** An endpoint is created, then started once its owner has
  made it reachable, so that a handler calling back during the first frame
  finds it. `Close` is idempotent. It runs the WebSocket closing handshake
  and returns after the reader and writer have exited, and after the
  dispatcher too unless a handler is running (a `Close` from inside a
  handler must not wait for itself); `Done` reports when everything has
  returned. Pending and queued calls fail with `ErrClosed` after `Close`, and
  with `ErrConnectionLost` when the connection breaks. A frame observer or a
  metrics callback must not call `Close`.

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

Each version package is generated from the OCA schemas in `schemas/` by
`internal/gen` (ADR 0003), and holds:

1. A type for every request and response, and for every object and
   enumeration they use. Required fields are values; optional ones are
   pointers (`ocpp.Ptr` helps), or slices with `omitempty`. Enumerations are
   named string types with constants and a `Valid` method. Date-times are
   `time.Time`. `customData` (2.x) is a `CustomData` that keeps the
   vendor's properties as raw JSON. OCPP 1.6 defines its objects and
   enumerations inline; the generator names them after chapter 7 of the
   1.6 specification (`IDTagInfo`, `ChargePointStatus`, `Measurand`, …).
2. Strict decoding and validation. `UnmarshalJSON` refuses a missing
   required property and, where the schema says `additionalProperties:
   false`, an unknown one. `Validate` checks every constraint the schemas
   use: enumerations, string lengths in characters, minimum, maximum,
   multipleOf, array sizes and absolute URIs. Failures are
   `*ocpp.ValidationError`, which carry the CALLERROR code of the version.
3. One call function per action. It checks the connection's version and
   the role that may send the action, validates the request, calls, and
   validates the response:

   ```go
   resp, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{
       ChargePointVendor: "OTFabric",
       ChargePointModel:  "TestStation",
   })
   ```

4. One handler interface per role, with a method per action the role
   receives, and an `Unimplemented…Handler` to embed, which answers
   `NotSupported` for the methods an application does not override and
   keeps it compiling when methods are added:

   ```go
   type CentralSystemHandler interface {
       BootNotification(ctx context.Context, p ocpp.Peer, req *BootNotificationRequest) (*BootNotificationResponse, error)
       // ... every Charge Point → Central System action
   }

   func CentralSystem(h CentralSystemHandler) ocpp.Dispatcher
   func ChargePoint(h ChargePointHandler) ocpp.Dispatcher
   ```

   For 2.x the roles are `CSMSHandler`/`CSMS` and
   `ChargingStationHandler`/`ChargingStation`.
5. `Actions` (every action and the role that sends it), `ValidateRequest`
   and `ValidateResponse` (for raw payloads, e.g. ocppctl), and
   `coverage.json`, the coverage manifest, with the SHA-256 of every schema
   it was generated from.

A dispatcher decodes and validates each incoming CALL before a handler sees
it, and answers an invalid one with the CALLERROR its violation calls for:
`FormationViolation`/`FormatViolation` for structure and unknown properties,
`OccurenceConstraintViolation`/`OccurrenceConstraintViolation` for a missing
property or a wrong number of elements, `TypeConstraintViolation` for a
value of the wrong type, and `PropertyConstraintViolation` for a value
outside its constraints. An action the version defines but this role does
not receive is answered with `NotSupported`; an action the version does not
define with `NotImplemented` (OCPP-J 1.6 §4.2.3, 2.x Part 4 §4.3).

A handler returns either a response or an error. A `*ocpp.CallError` is sent
as is. Any other error is sent as `InternalError` with an empty description,
so that internal details do not leak to the peer, and is logged with its
full text. So is a nil response, or one that fails validation: that is a bug
of the application, which the peer should not receive.

The direction of every action comes from the specifications
(`internal/gen/directions.go`). A call function refuses an action the
connection's role does not send, for example a station sending
`RemoteStartTransaction`, with `ErrWrongDirection`. Raw calls are not
checked.

Vendor extensions use what the protocol defines: DataTransfer in every
version, and `customData` in 2.x. Raw use (`json.RawMessage` request and
response, and a raw `Dispatcher`) remains available for tools like ocppctl,
a proxy, or an application handling DataTransfer itself.

Validation is generated code, not a schema interpreter at runtime, and is
always on in the call functions and dispatchers. A test suite
(`internal/schematest`) checks it for every message against the schemas
and an independent JSON Schema validator: complete and minimal messages
are accepted and survive a round trip; every constraint, broken one at a
time, is refused by both, with the expected code. Exempting actions from
validation for a non-conforming peer is not implemented; it would be added
only per action and logged, never globally.

## Client: the Charging Station role

```go
c, err := client.New("wss://csms.example.com/ocpp/STATION001",
    client.WithVersions(ocpp.V201, ocpp.V16),           // offered in this order
    client.WithHandler(ocpp.V201, ocpp201.ChargingStation(h201)),
    client.WithHandler(ocpp.V16, ocpp16.ChargePoint(h16)),
    client.WithBasicAuth("STATION001", password),       // profile 2 with wss
    client.WithReconnect(client.DefaultReconnect),
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
  with the back-off of 2.x Part 4 §5.4: RetryBackOffWaitMinimum plus up to
  RetryBackOffRandomRange, doubled up to RetryBackOffRepeatTimes times
  (`client.Reconnect`; `client.DefaultReconnect` when the CSMS has set none).
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
  registers the new session and then closes the old one, with WebSocket
  status 1008 and `server.ErrReplaced` as its `Err`. From that moment
  `Station(id)` returns only the new session. The old one may take up to the
  WebSocket closing handshake to end, and nothing can look it up meanwhile.
  Stations commonly reconnect before the CSMS notices that the old TCP
  connection is dead, and because authentication runs first, an
  unauthenticated client cannot use this to evict a station. `RejectNew`
  refuses the new connection with HTTP 409 instead. Whether a newer edition
  specifies this behaviour is to be checked (SPECIFICATIONS.md S4).
- Limits: the maximum number of stations (default 10 000, then HTTP 503), the
  message size (default 64 KiB; a larger message closes the connection with
  1009), the time to read the request headers when the server listens itself
  (default 10 s), the response timeout (default 30 s) and the queue of calls
  waiting for the outgoing slot (default 64, then `ErrBusy`). When a limit is
  hit, the request is refused or the connection closed, never buffered
  without bound.
- Compression: the server accepts `permessage-deflate` (RFC 7692) when a
  station offers it, as a 2.x CSMS SHALL (2.0.1 Part 4 §3.3; 2.1 §3.4),
  without context takeover, so that idle connections hold no compression
  state. The message size limit applies after decompression. A client
  offers compression with `WithCompression`.
- Refusals before the upgrade are HTTP errors: 404 without an identity or
  for an identity the authenticator does not know (`ErrUnknownStation`),
  401 when authentication fails (with `WWW-Authenticate: Basic`), 409 and
  503 as above. A station that offers no supported subprotocol gets a
  completed handshake without one and an immediate close (T1).
- `Shutdown(ctx)` stops accepting connections and closes the sessions with
  status 1001 (going away). It returns when they have ended or `ctx` ends.
  `Close` does not wait. `Serve` and `ListenAndServe` then return
  `ocpp.ErrClosed`.

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
  are never logged; a frame observer can see them.
- **Metrics.** `WithMetrics(ocpp.Metrics)`, a callback interface. Embedding
  `ocpp.NopMetrics` lets an implementation compile when callbacks are added.
  Callbacks receive the station ID as an argument. Whether to label with it
  is the application's choice, which keeps cardinality under its control.
  The callbacks cover:
  - connections: established, failed, disconnected, reconnect attempts;
  - outgoing CALLs, with their result (including timeouts) and latency;
  - incoming CALLs, with the handler's result and duration;
  - protocol errors, including late and unknown responses.

  A request refused by validation shows as `OnCallHandled` with the
  CALLERROR it was answered with; a response refused by validation as
  `OnCallDone` with an error wrapping `ocpp.ErrInvalidMessage`.
- **Interceptors.** `WithCallInterceptor` and `WithHandlerInterceptor` wrap
  outgoing and incoming calls with the call's metadata. This is where an
  application starts and ends OpenTelemetry spans, without the library
  depending on OpenTelemetry.
- **Frame observer.** `WithFrameObserver` sees every frame sent and received.
  ocppctl's trace and replay features need it (R13).

[OBSERVABILITY.md](OBSERVABILITY.md) has the details.

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

Each entry names the state change, the versions, and the rule in
SPECIFICATIONS.md it follows. The rules are still marked "secondary" there
until checked against the OCA text (S4).

| Transition | Versions | Source | Code |
|---|---|---|---|
| A CALL takes the outgoing slot; the slot is freed by its response, its response timeout, or the end of the connection, and not by the caller's context | all | T6 | `internal/engine/call.go` |
| A response whose message ID matches no pending CALL is dropped | all | T6, T7 | `respond` |
| A malformed CALL whose message ID can be read is answered with a CALLERROR | all | T3 | `malformed` |
| A frame of an unknown message type is dropped without an answer | all | T9 (to confirm) | `malformed` |
| Subprotocol: the server selects the first of the station's offers it supports; no common one refuses the upgrade | all | T1 | `server.selectVersion` |
| SEND neither answered nor counted against the CALL slot | 2.1 | T5 | Phase 6 |

## Implementation choices

Where the specifications leave a decision open, go-ocpp decides as follows.
Each choice can be revisited if an edition or erratum settles it.

| Choice | Reason |
|---|---|
| A malformed CALL on a 1.6 connection is answered with `FormationViolation`; on 2.x with `RpcFrameworkError` | 1.6 has no RPC-level code; `FormationViolation` ("not conform the PDU structure") is the closest |
| Frames of an unknown type are dropped, not answered with `MessageTypeNotSupported` | Reported errata (T9) remove that answer; dropping is also safe under the older text |
| A handler error other than `*ocpp.CallError` becomes `InternalError` with an empty description | Error texts can contain internal details; they are logged locally instead |
| A `*ocpp.CallError` with a code the version does not define becomes `InternalError` | The peer must only see codes its version knows |
| Message IDs are random UUIDs (36 characters) | Unique per connection without coordination, and within the 36-character limit |
| Default frame size limit 64 KiB, response timeout 30 s, write timeout 10 s, 64 queued calls | The specifications set none; OCPP 2.x lets the CSMS configure the station's message timeout, which the application passes in |
| Incoming CALLs are queued up to 16 before the reader waits | Bounds memory if a peer ignores T6; the reader keeps backpressure on the connection |
| Session replacement registers the new session first, then closes the old one | No moment without a session for a reconnecting station; never two that `Station` can return |
| Binary WebSocket messages close the connection with 1003 | OCPP-J uses text messages only |
| Compression without context takeover, accepted for every version | Lower memory per connection; supporting it on 1.6 connections is allowed and harmless |
| `client.DefaultReconnect`: 5 s, plus up to 10 s, doubled up to 5 times | Part 4 §5.4 defines the algorithm, not default values |
| An action the version defines but the role does not receive is answered with `NotSupported`, an unhandled one too (Unimplemented handlers) | "Recognized but not supported", where `NotImplemented` means "not known" |
