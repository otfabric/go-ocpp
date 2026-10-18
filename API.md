# API Reference: otfabric/go-ocpp

go-ocpp has one package per role and shared packages for everything both
roles use:

| Role or concern | Package | Reference |
|---|---|---|
| Charging Station (OCPP 1.6: Charge Point) | `client` | [API_CLIENT.md](API_CLIENT.md) |
| CSMS (OCPP 1.6: Central System) | `server` | [API_SERVER.md](API_SERVER.md) |
| Shared types: versions, calls, handlers, errors, observability | `ocpp` | [below](#package-ocpp) |
| Typed messages, validation, call functions, handlers | `ocpp16`, `ocpp201`, `ocpp21` | [below](#packages-ocpp16-ocpp201-and-ocpp21) |
| OCPP-J frame codec | `ocppj` | [below](#package-ocppj) |
| Testing an application's handlers | `ocpptest` | [below](#package-ocpptest) |
| Certificate hash data | `ocppcert` | [below](#package-ocppcert) |

Errors are in [ERRORS.md](ERRORS.md), logs and metrics in
[OBSERVABILITY.md](OBSERVABILITY.md), the design in
[ARCHITECTURE.md](ARCHITECTURE.md). pkg.go.dev has every generated type.
The exported API of `ocpp`, `ocppj`, `client`, `server`, `ocpptest` and
`ocppcert` is recorded in [testdata/api](testdata/api); `TestAPI` fails on
any change that is not recorded there, and every change is listed in
[RELEASE.md](RELEASE.md). Stability rules are in
[COMPATIBILITY.md](COMPATIBILITY.md#api-stability).

## Contents

- [Package ocpp](#package-ocpp)
- [Packages ocpp16, ocpp201 and ocpp21](#packages-ocpp16-ocpp201-and-ocpp21)
- [Package ocppj](#package-ocppj)
- [Package ocpptest](#package-ocpptest)
- [Package ocppcert](#package-ocppcert)
- [Concurrency](#concurrency)
- [Departures from other OTFabric libraries](#departures-from-other-otfabric-libraries)

## Package ocpp

### Versions and roles

```go
type Version uint8 // V16 "1.6" (ocpp1.6), V201 "2.0.1" (ocpp2.0.1), V21 "2.1" (ocpp2.1)

func Versions() []Version
func (v Version) Valid() bool
func (v Version) String() string
func (v Version) Subprotocol() string
func ParseVersion(s string) (Version, bool)
func ParseSubprotocol(s string) (Version, bool) // exact: subprotocol tokens are case-sensitive

type Role uint8 // ChargingStation, CSMS
func (r Role) String() string
```

### Calls and handlers

```go
type Caller interface {
    Call(ctx context.Context, action string, req, resp any) error
}

type Peer interface {
    ID() string      // the station identity, on both ends
    Version() Version
    Caller
}

type Dispatcher interface {
    Dispatch(ctx context.Context, p Peer, action string, payload json.RawMessage) (json.RawMessage, error)
}
type DispatcherFunc func(ctx context.Context, p Peer, action string, payload json.RawMessage) (json.RawMessage, error)

// OCPP 2.1 SEND: implemented by *client.Client and *server.Session.
type Sender interface {
    Send(ctx context.Context, action string, req any) error
}
// Implemented by a Dispatcher that also receives SENDs (the ocpp21 dispatchers do).
type SendDispatcher interface {
    DispatchSend(ctx context.Context, p Peer, action string, payload json.RawMessage) error
}
```

- `req` is marshalled with `encoding/json`. A `json.RawMessage` is sent as
  is, and nil as `{}`.
- `resp` receives the CALLRESULT payload. It must be a pointer, a
  `*json.RawMessage` (to receive the payload unchanged), or nil (to discard
  it).
- A `Dispatcher` returns the CALLRESULT payload, which must be a JSON
  object. Errors become a CALLERROR: a `*CallError` with a code the version
  defines is sent as is. Anything else is sent as `InternalError` without
  its text.
- A handler's context ends when the connection closes. A handler may call
  the peer (`p.Call`) while it handles a CALL.
- `AfterResponse(ctx, f)` runs `f` once the handler's response is on its
  way: what a handler sends there goes out after the response. Use it for
  the messages a use case sends after answering (NotifyReport after
  GetBaseReport, status notifications after UpdateFirmware or GetLog, the
  triggered message after TriggerMessage, a reboot after Reset).

```go
func AfterResponse(ctx context.Context, f func()) bool // false outside a CALL handler

func (h station) GetBaseReport(ctx context.Context, p ocpp.Peer, req *ocpp201.GetBaseReportRequest) (*ocpp201.GetBaseReportResponse, error) {
    ocpp.AfterResponse(ctx, func() { h.sendReport(p, req.RequestID) }) // NotifyReport after the response
    return &ocpp201.GetBaseReportResponse{Status: ocpp201.GenericDeviceModelStatusAccepted}, nil
}
```
- `Send` (OCPP 2.1 only; `ErrUnsupportedVersion` otherwise) queues a SEND
  and returns: nothing comes back. It does not wait for the outgoing CALL
  slot. `DispatchSend` is called for incoming SENDs, in arrival order with
  the CALLs; its error is logged and counted, never sent. A dispatcher
  without `DispatchSend` drops SENDs with a log entry, so a wrapper around
  an ocpp21 dispatcher implements `SendDispatcher` too and forwards to it.
- On 2.1 a CALLRESULT that fails decoding or `Validate` is answered with a
  CALLRESULTERROR, and the call returns the error.
- Raw calls (`Call` with `json.RawMessage`, a `DispatcherFunc`) bypass the
  typed validation: that is how vendor actions, and messages of stations
  that do not conform, are handled where an application chooses to.

### CALLERROR

```go
type ErrorCode string // ERRORS.md says which version defines each and when go-ocpp sends it
const (
    NotImplemented, NotSupported, InternalError, ProtocolError, SecurityError,
    PropertyConstraintViolation, TypeConstraintViolation, GenericError ErrorCode // all versions
    FormationViolation, OccurenceConstraintViolation ErrorCode                    // 1.6 spellings
    FormatViolation, OccurrenceConstraintViolation,
    MessageTypeNotSupported, RpcFrameworkError ErrorCode                          // 2.x
)

type CallError struct {
    Code        ErrorCode
    Description string
    Details     json.RawMessage // a JSON object, or nil for {}
}
func NewCallError(code ErrorCode, description string) *CallError
func (e *CallError) Error() string
```

### Validation

```go
type ValidationError struct {
    Code   ErrorCode // the CALLERROR code, spelt as the version spells it
    Field  string    // e.g. "meterValue[0].sampledValue[1].measurand"
    Reason string
}
func (e *ValidationError) CallError() *CallError
func (e *ValidationError) Error() string
func (e *ValidationError) Unwrap() error // ErrInvalidMessage
var ErrInvalidMessage error // wrapped by every *ValidationError

func Ptr[T any](v T) *T // for optional fields
```

### Observability

```go
type State uint8     // Disconnected, Connecting, Connected, Closed
type Direction uint8 // Outgoing, Incoming
type ConnInfo struct{ StationID string; Version Version; Role Role; SessionID string }
type CallInfo struct{ ConnInfo; Action, MessageID string; Direction Direction }

type Interceptor func(ctx context.Context, info CallInfo, next func(context.Context) error) error
type FrameObserver func(info ConnInfo, dir Direction, frame []byte)
type Metrics interface { /* OnConnect, OnConnectFailed, OnDisconnect, OnReconnectAttempt,
                            OnCallDone, OnCallHandled, OnProtocolError: see OBSERVABILITY.md */ }
type NopMetrics struct{} // embed it
```

### Errors

```go
// How the peer closed the connection, wrapped next to ErrConnectionLost.
type CloseError struct{ Code int; Reason string }
func (e *CloseError) Error() string
func (e *CloseError) Normal() bool // 1000 or 1001: closed on purpose
```

The sentinels (`ErrClosed`, `ErrNotConnected`, `ErrConnectFailed`,
`ErrConnectionLost`, `ErrTimeout`, `ErrBusy`, `ErrWrongDirection`,
`ErrUnsupportedVersion`, `ErrProtocol`, `ErrInvalidOption`,
`ErrHandlerPanic`, `ErrUnexpectedResponse`, `ErrInvalidMessage`) are
described in [ERRORS.md](ERRORS.md).

## Packages ocpp16, ocpp201 and ocpp21

Generated from the OCA schemas (`schemas/`). Each holds, for every action
`X` of its version:

```go
type XRequest struct{ ... }  // and every object and enumeration it uses
type XResponse struct{ ... }
func (x *XRequest) Validate() error
func X(ctx context.Context, c ocpp.Caller, req *XRequest) (*XResponse, error)
```

and for the version as a whole:

| | ocpp16 | ocpp201 | ocpp21 |
|---|---|---|---|
| Charging station handlers | `ChargePointHandler`, `UnimplementedChargePointHandler`, `ChargePoint(h) ocpp.Dispatcher` | `ChargingStationHandler`, `UnimplementedChargingStationHandler`, `ChargingStation(h)` | as ocpp201 |
| CSMS handlers | `CentralSystemHandler`, `UnimplementedCentralSystemHandler`, `CentralSystem(h)` | `CSMSHandler`, `UnimplementedCSMSHandler`, `CSMS(h)` | as ocpp201; the dispatchers also implement `ocpp.SendDispatcher` |
| Actions | 39 (28 core, 11 security) | 64 | 91, one of them a SEND |

```go
type Action struct{ Name string; From ocpp.Role; Send bool } // From zero for DataTransfer
var Actions []Action
func ValidateRequest(action string, payload json.RawMessage) error
func ValidateResponse(action string, payload json.RawMessage) error
```

The SEND action of 2.1, NotifyPeriodicEventStream, has no response:

```go
func NotifyPeriodicEventStream(ctx context.Context, s ocpp.Sender, req *NotifyPeriodicEventStreamRequest) error
// CSMSHandler method:
NotifyPeriodicEventStream(ctx context.Context, p ocpp.Peer, req *NotifyPeriodicEventStreamRequest) error
```

- Call functions check, before sending, that the connection is of their
  version (`ErrUnsupportedVersion`), that the role sends the action
  (`ErrWrongDirection`) and that the request is valid
  (`*ocpp.ValidationError`); the response is validated on arrival.
- Handler methods are `X(ctx, p ocpp.Peer, req *XRequest) (*XResponse, error)`.
  `p` is the other end: call it to send a CALL back while handling.
- Field names follow Go's initialisms: `idTag` is `IDTag`, `evseId` is
  `EVSEID`. Enumeration constants are the type name without "EnumType" or
  "Type", then the value: `ocpp201.RegistrationStatusAccepted`,
  `ocpp16.ResetSoft`, `ocpp16.MeasurandEnergyActiveImportRegister`.
- Optional fields are pointers (`ocpp.Ptr`); required arrays must not be
  nil (an empty array is `[]T{}`).
- `CustomData{VendorID, Fields}` (2.x) keeps vendor properties as raw JSON.
- `coverage.json` in each package lists every action with its schemas and
  their SHA-256.

## Package ocppj

The frame codec, with no I/O.

```go
type MessageType uint8 // Call=2, CallResult=3, CallError=4, CallResultError=5 (2.1), Send=6 (2.1)
func (t MessageType) Allowed(v ocpp.Version) bool
func (t MessageType) String() string

type Frame struct {
    Type        MessageType
    ID          string
    Action      string          // Call, Send
    Payload     json.RawMessage // Call, CallResult, Send
    ErrorCode   ocpp.ErrorCode  // CallError, CallResultError
    Description string
    Details     json.RawMessage
}
type Limits struct{ MaxFrameSize int } // 0: DefaultMaxFrameSize (64 KiB)
const MaxMessageIDLength = 36

func Decode(data []byte, v ocpp.Version, l Limits) (Frame, error) // errors are *DecodeError, wrapping ErrMalformed
func Encode(f Frame) ([]byte, error)

type DecodeError struct{ Type MessageType; ID string; IDKnown, UnknownType bool; Reason string }
func (e *DecodeError) Error() string
func (e *DecodeError) Unwrap() error // ErrMalformed
var ErrMalformed error

func ErrorCodes(v ocpp.Version) []ocpp.ErrorCode
func ValidErrorCode(v ocpp.Version, code ocpp.ErrorCode) bool
func FormatErrorCode(v ocpp.Version) ocpp.ErrorCode     // FormationViolation / FormatViolation
func OccurrenceErrorCode(v ocpp.Version) ocpp.ErrorCode // spelt per version
func RPCErrorCode(v ocpp.Version) ocpp.ErrorCode        // FormationViolation / RpcFrameworkError
```

## Package ocpptest

Testing an application's handlers against a real go-ocpp peer over a
loopback WebSocket, as `net/http/httptest` does for HTTP handlers.
Everything it starts stops when the test ends.

```go
func CSMS(t testing.TB, opts ...server.Option) (*server.Server, string) // base URL "ws://127.0.0.1:port/ocpp/"
func Station(t testing.TB, url string, opts ...client.Option) *client.Client // connected
func Session(t testing.TB, srv *server.Server, id string) *server.Session  // waits for it
const Timeout = 10 * time.Second

type Recorder struct{ ... } // ocpp.Dispatcher and ocpp.SendDispatcher
func NewRecorder() *Recorder
func (r *Recorder) Answer(action, payload string) *Recorder        // CALLRESULT payload
func (r *Recorder) AnswerError(action string, err *ocpp.CallError) *Recorder
func (r *Recorder) Received(action string) []json.RawMessage
func (r *Recorder) Wait(t testing.TB, action string) json.RawMessage
```

```go
func TestMyCSMS(t *testing.T) {
    stationSide := ocpptest.NewRecorder().Answer("Reset", `{"status":"Accepted"}`)
    srv, url := ocpptest.CSMS(t, server.WithHandler(ocpp.V201, ocpp201.CSMS(myCSMS{})))
    c := ocpptest.Station(t, url+"CS1", client.WithHandler(ocpp.V201, stationSide))
    boot, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{...})
    ...
    reset, err := ocpp201.Reset(ctx, ocpptest.Session(t, srv, "CS1"), &ocpp201.ResetRequest{...})
}
```

`CSMS` adds `server.WithoutAuthentication` when no authentication option
is given, and `Station` adds `client.WithInsecureTransport`: the test
server is plain WebSocket on loopback. A `Recorder` does not validate;
wrap it in a version package's dispatcher where validation matters.

## Package ocppcert

Certificate identifiers, with the standard library only; the library
keeps no certificates.

```go
type HashData struct{ HashAlgorithm, IssuerNameHash, IssuerKeyHash, SerialNumber string }
func Hash(cert, issuer *x509.Certificate, alg crypto.Hash) (HashData, error) // SHA-256, -384, -512
var ErrUnsupportedHash error
```

`HashData` is CertificateHashDataType (2.x Part 2 §2.6; 1.6 Security
Whitepaper): the issuer name hash over the DER of the certificate's issuer
field, the issuer key hash over the issuer's subject public key (as in
OCSP), the serial number in hexadecimal without leading zeroes. Copy the
fields into `ocpp201.CertificateHashDataType` and its siblings.

## Concurrency

- `Client`, `Server`, `Session` and `Recorder` are safe for concurrent use.
  Any number of goroutines may call `Call` at the same time. Their CALLs are
  sent one at a time, because OCPP allows one outstanding CALL per
  direction. SENDs do not wait for the slot.
- Handlers of one connection run one at a time, in arrival order, on a
  goroutine of their own. Handlers of different connections run
  concurrently.
- Callbacks (metrics, frame observers, state handlers, session handlers)
  run synchronously. They must not block, and must not call `Close`.

## Departures from other OTFabric libraries

- Logging takes a `*slog.Logger` instead of a library-specific `Logger`
  interface ([ADR 0005](docs/adr/0005-observability.md)).
- Version packages are named `ocpp16`, `ocpp201` and `ocpp21`
  ([ADR 0001](docs/adr/0001-package-layout.md)).
- `Client.Connect` does not retry. With `WithReconnect`, redialling starts
  only after an open connection is lost.
