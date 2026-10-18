# API Reference: otfabric/go-ocpp

The reference for what is implemented (Phase 2): the root package, `ocppj`,
`client` and `server`. The version packages (`ocpp16`, `ocpp201`, `ocpp21`)
arrive in Phases 3, 5 and 6. Until then, requests and responses are any
JSON-marshallable Go value, or `json.RawMessage`. The design they will
follow is in [ARCHITECTURE.md](ARCHITECTURE.md#messages-handlers-and-calls).

## Contents

- [Package ocpp](#package-ocpp)
- [Package client](#package-client)
- [Package server](#package-server)
- [Package ocppj](#package-ocppj)
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
```

- `req` is marshalled with `encoding/json`. A `json.RawMessage` is sent as is,
  and nil as `{}`.
- `resp` receives the CALLRESULT payload. It must be a pointer, a
  `*json.RawMessage` (to receive the payload unchanged), or nil (to discard
  it).
- A `Dispatcher` returns the CALLRESULT payload, which must be a JSON object.
  Errors become a CALLERROR: a `*CallError` with a code the version defines
  is sent as is. Anything else is sent as `InternalError` without its text.
- A handler's context ends when the connection closes. A handler may call
  the peer (`p.Call`) while it handles a CALL.

### CALLERROR

```go
type ErrorCode string // NotImplemented, NotSupported, InternalError, ProtocolError, SecurityError,
                      // FormationViolation (1.6) / FormatViolation (2.x),
                      // OccurenceConstraintViolation (1.6) / OccurrenceConstraintViolation (2.x),
                      // PropertyConstraintViolation, TypeConstraintViolation, GenericError,
                      // MessageTypeNotSupported (2.x), RpcFrameworkError (2.x)

type CallError struct {
    Code        ErrorCode
    Description string
    Details     json.RawMessage // a JSON object, or nil for {}
}
func NewCallError(code ErrorCode, description string) *CallError
```

### Observability

```go
type State uint8     // Disconnected, Connecting, Connected, Closed
type Direction uint8 // Outgoing, Incoming
type ConnInfo struct{ StationID string; Version Version; Role Role; SessionID string }
type CallInfo struct{ ConnInfo; Action, MessageID string; Direction Direction }

type Interceptor func(ctx context.Context, info CallInfo, next func(context.Context) error) error
type FrameObserver func(info ConnInfo, dir Direction, frame []byte)
type Metrics interface { /* see OBSERVABILITY.md */ }
type NopMetrics struct{}
```

### Errors

The sentinels are described in [ERRORS.md](ERRORS.md).

## Package client

The Charging Station role.

```go
func New(url string, opts ...Option) (*Client, error)

func (c *Client) Connect(ctx context.Context) error
func (c *Client) Call(ctx context.Context, action string, req, resp any) error
func (c *Client) Close() error
func (c *Client) ID() string
func (c *Client) Version() ocpp.Version
func (c *Client) State() ocpp.State
```

- `url` is `wss://host/path/<identity>`, or `ws://` with
  `WithInsecureTransport`. Credentials in the URL are refused; use
  `WithBasicAuth`.
- `Connect` dials once. It returns `nil` when the connection is open, or an
  error wrapping `ocpp.ErrConnectFailed`, which includes the HTTP status
  when the server answered.
- `Call` fails at once with `ErrNotConnected` while the client is not
  connected; nothing is queued.
- `Close` is idempotent. It stops reconnecting, and pending calls fail with
  `ErrClosed`.

| Option | Default | Meaning |
|---|---|---|
| `WithVersions(v...)` | versions with a handler, newest first | Versions offered, in order of preference |
| `WithHandler(v, d)` | none: CALLs get `NotImplemented` | Dispatcher for the CSMS's CALLs on version `v` |
| `WithBasicAuth(user, pass)` | none | HTTP Basic credentials (profiles 1 and 2) |
| `WithTLS(cfg)` | system roots | Trust roots, client certificate (profile 3); MinVersion raised to TLS 1.2 |
| `WithHTTPClient(c)` | `http.DefaultClient` | Handshake HTTP client (proxies); not with `WithTLS` |
| `WithInsecureTransport()` | off | Allow `ws://` |
| `WithReconnect(Reconnect{MinDelay, MaxDelay})` | off | Redial with exponential backoff and jitter (1 s to 2 min) after a lost connection |
| `WithStateHandler(f)` | none | Called on every state change; must not block or call `Close` |
| `WithLogger(l)` | silent | `*slog.Logger` |
| `WithMetrics(m)` | none | `ocpp.Metrics` |
| `WithHandshakeTimeout(d)` | 30 s | Dial, TLS and WebSocket handshakes |
| `WithResponseTimeout(d)` | 30 s | Wait for a CALL's response |
| `WithMaxQueuedCalls(n)` | 64 | Calls waiting for the outgoing slot before `ErrBusy` |
| `WithMaxMessageSize(n)` | 64 KiB | Largest message accepted |
| `WithCallInterceptor(i)`, `WithHandlerInterceptor(i)` | none | Wrap outgoing and incoming calls |
| `WithFrameObserver(f)` | none | See every frame |

## Package server

The CSMS role.

```go
func New(opts ...Option) (*Server, error)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) // mount anywhere; identity = last path segment
func (s *Server) ListenAndServe(addr string) error                // returns ocpp.ErrClosed after Shutdown/Close
func (s *Server) Serve(ln net.Listener) error
func (s *Server) Shutdown(ctx context.Context) error
func (s *Server) Close() error
func (s *Server) Station(id string) (*Session, bool)
func (s *Server) Stations() []*Session
func (s *Server) Versions() []ocpp.Version

func (s *Session) ID() string
func (s *Session) Version() ocpp.Version
func (s *Session) Call(ctx context.Context, action string, req, resp any) error
func (s *Session) Close() error
func (s *Session) Done() <-chan struct{}
func (s *Session) Err() error // ocpp.ErrClosed, server.ErrReplaced, or wraps ocpp.ErrConnectionLost
func (s *Session) SessionID() string
func (s *Session) RemoteAddr() string
func (s *Session) ConnectedAt() time.Time
```

### Authentication

```go
type Authenticator interface {
    Authenticate(ctx context.Context, r ConnectRequest) error
}
type AuthenticatorFunc func(ctx context.Context, r ConnectRequest) error

type ConnectRequest struct {
    StationID    string
    Username     string
    Password     Secret // prints as [redacted]; string(p) for the value
    HasBasicAuth bool
    TLS          *tls.ConnectionState
    RemoteAddr   string
    Request      *http.Request
}

func BasicAuth(lookup func(ctx context.Context, stationID string) (password string, ok bool)) Authenticator
```

`BasicAuth` requires the user name to equal the station identity, and
compares passwords in constant time.

| Option | Default | Meaning |
|---|---|---|
| `WithHandler(v, d)` | required | Dispatcher for stations' CALLs; the versions accepted are those with one |
| `WithAuthenticator(a)` or `WithoutAuthentication()` | required, exactly one | Decide before the upgrade |
| `WithSessionHandler(onConnect, onDisconnect)` | none | Session start and end |
| `WithMaxStations(n)` | 10 000 | Then HTTP 503 |
| `WithReplacePolicy(p)` | `ReplaceExisting` | Or `RejectNew` (HTTP 409) |
| `WithTLS(cfg)` | none | TLS for `ListenAndServe` and `Serve` |
| `WithLogger`, `WithMetrics`, `WithCallInterceptor`, `WithHandlerInterceptor`, `WithFrameObserver` | none | As for the client |
| `WithHandshakeTimeout(d)` | 10 s | Reading the request headers (own listener only) |
| `WithResponseTimeout(d)` | 30 s | Wait for a station's response |
| `WithMaxQueuedCalls(n)` | 64 | Per session |
| `WithMaxMessageSize(n)` | 64 KiB | Largest message accepted |

## Package ocppj

The frame codec, with no I/O.

```go
type MessageType uint8 // Call=2, CallResult=3, CallError=4, CallResultError=5 (2.1), Send=6 (2.1)
func (t MessageType) Allowed(v ocpp.Version) bool

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

func Decode(data []byte, v ocpp.Version, l Limits) (Frame, error) // errors are *DecodeError, wrapping ErrMalformed
func Encode(f Frame) ([]byte, error)

func ErrorCodes(v ocpp.Version) []ocpp.ErrorCode
func ValidErrorCode(v ocpp.Version, code ocpp.ErrorCode) bool
func FormatErrorCode(v ocpp.Version) ocpp.ErrorCode     // FormationViolation / FormatViolation
func OccurrenceErrorCode(v ocpp.Version) ocpp.ErrorCode // spelt per version
func RPCErrorCode(v ocpp.Version) ocpp.ErrorCode        // FormationViolation / RpcFrameworkError
```

## Concurrency

- `Client`, `Server` and `Session` are safe for concurrent use. Any number of
  goroutines may call `Call` at the same time. Their CALLs are sent one at a
  time, because OCPP allows one outstanding CALL per direction.
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
