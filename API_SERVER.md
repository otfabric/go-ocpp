# API Reference: package server (CSMS)

`github.com/otfabric/go-ocpp/server` is the CSMS role (OCPP 1.6: Central
System): it accepts station connections, authenticates them before the
WebSocket upgrade, answers their CALLs through a dispatcher per version, and
gives the application a `Session` per station to call it. Shared types are
in [API.md](API.md), the station role in [API_CLIENT.md](API_CLIENT.md),
errors in [ERRORS.md](ERRORS.md).

## Contents

- [Quick start](#quick-start)
- [Server](#server)
- [Sessions](#sessions)
- [The connection handshake](#the-connection-handshake)
- [Authentication](#authentication)
- [Options](#options)
- [Shutdown](#shutdown)
- [Scaling out](#scaling-out)
- [Errors by method](#errors-by-method)

## Quick start

```go
srv, err := server.New(
    server.WithHandler(ocpp.V16, ocpp16.CentralSystem(my16)),   // one endpoint,
    server.WithHandler(ocpp.V201, ocpp201.CSMS(my201)),         // three versions
    server.WithHandler(ocpp.V21, ocpp21.CSMS(my21)),
    server.WithAuthenticator(server.BasicAuth(passwordOf)),      // profile 1/2
    server.WithTLS(tlsConfig),
    server.WithSessionHandler(onConnect, onDisconnect),
)
if err != nil { ... }
go srv.ListenAndServe(":8443") // or mount srv (an http.Handler) in your own server

// Later, from anywhere: call a station.
if s, ok := srv.Station("CS001"); ok {
    resp, err := ocpp201.GetVariables(ctx, s, &ocpp201.GetVariablesRequest{...})
}
```

Handlers embed `ocpp201.UnimplementedCSMSHandler` (1.6:
`UnimplementedCentralSystemHandler`) and implement what they support.
A handler may call the station back through its `p ocpp.Peer` while it
handles a CALL (GetConfiguration while handling BootNotification).
Runnable programs: [examples/csms](examples/csms) and the
[examples index](examples/README.md).

## Server

```go
func New(opts ...Option) (*Server, error)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) // mount anywhere; identity = last path segment
func (s *Server) ListenAndServe(addr string) error                // returns ocpp.ErrClosed after Shutdown/Close
func (s *Server) Serve(ln net.Listener) error
func (s *Server) Shutdown(ctx context.Context) error
func (s *Server) Close() error
func (s *Server) Station(id string) (*Session, bool) // registered just after the handshake: see WithSessionHandler
func (s *Server) Stations() []*Session
func (s *Server) Versions() []ocpp.Version

type ReplacePolicy uint8 // ReplaceExisting (default), RejectNew

const (
    DefaultHandshakeTimeout = 10 * time.Second
    DefaultResponseTimeout  = 30 * time.Second
    DefaultMaxQueuedCalls   = 64
    DefaultMaxMessageSize   = 64 << 10
    DefaultMaxStations      = 10000
)
```

- A `Server` is an `http.Handler`: mount it under any path (`/ocpp/`) in an
  application's `http.Server`, behind its middleware. That server then does
  TLS and must set `ReadHeaderTimeout` against slow clients.
- `ListenAndServe` and `Serve` run their own `http.Server` with TLS
  (`WithTLS`, at least TLS 1.2) and the handshake timeout as
  `ReadHeaderTimeout`; they log `listening`. Several listeners may serve
  one `Server`.
- `New` needs at least one `WithHandler` and exactly one of
  `WithAuthenticator` and `WithoutAuthentication`; refusals wrap
  `ocpp.ErrInvalidOption`.

## Sessions

```go
func (s *Session) ID() string
func (s *Session) Role() ocpp.Role // ocpp.CSMS
func (s *Session) Version() ocpp.Version
func (s *Session) Call(ctx context.Context, action string, req, resp any) error
func (s *Session) Send(ctx context.Context, action string, req any) error // OCPP 2.1
func (s *Session) Close() error
func (s *Session) Shutdown(ctx context.Context) error // graceful Close
func (s *Session) Done() <-chan struct{}
func (s *Session) Err() error // ocpp.ErrClosed, server.ErrReplaced, or wraps ocpp.ErrConnectionLost
func (s *Session) SessionID() string     // unique per connection (a UUID), in logs as session_id
func (s *Session) RemoteAddr() string
func (s *Session) ConnectedAt() time.Time
func (s *Session) TLS() *tls.ConnectionState // nil without TLS; the client certificate for profile 3
```

- A `Session` is an `ocpp.Peer`: pass it to the typed call functions. It
  belongs to one connection; after a reconnect, `Station(id)` returns the
  new one.
- `Call` behaves as the client's: one outstanding CALL per station, up to
  `WithMaxQueuedCalls` waiting, `ErrBusy` beyond.
- `Done` is closed when the connection has ended and every goroutine of the
  session has returned; `Err` then says why.

## The connection handshake

For each upgrade request, in this order:

| Step | Refusal |
|---|---|
| Identity: the last path segment, percent-decoded | none or empty: HTTP 404 |
| Version: the first subprotocol of the station's offer that has a handler | (decided here, refused below) |
| 2.x identity rules: identifierString, at most 48 characters, no ":" (2.x Part 4 §3.1.1) | HTTP 400 |
| Authentication (`Authenticator`) | `ErrUnknownStation`: HTTP 404; a `*StatusError`: its code; any other error: HTTP 401 with `WWW-Authenticate` |
| No subprotocol in common | the handshake completes without a subprotocol and the connection is closed (OCPP-J 1.6 §3.1.2; 2.x Part 4 §3.2) |
| Shutting down | HTTP 503 |
| Same identity connected, `RejectNew` | HTTP 409 |
| `WithMaxStations` reached | HTTP 503 |
| Origin header of another host (browsers), unless `WithOriginPatterns` | HTTP 403 |
| WebSocket upgrade, compression accepted when offered | — |

Every refusal is logged at Warn and reported to
`Metrics.OnConnectFailed`. With `ReplaceExisting` (the default) a station
that connects again replaces its session: the new one is registered first,
then the old one closed with `ErrReplaced`.

## Authentication

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
    TLS          *tls.ConnectionState // client certificates: profile 3
    RemoteAddr   string
    Request      *http.Request // headers, e.g. from client.WithHeader
}
type Secret string // String and GoString return "[redacted]"
func (s Secret) String() string
func (s Secret) GoString() string

func BasicAuth(lookup func(ctx context.Context, stationID string) (password string, ok bool)) Authenticator

func CertificateAuth() Authenticator          // profile 3: a verified client certificate whose CommonName is the identity
func AnyOf(auths ...Authenticator) Authenticator // accepts when one accepts: AnyOf(CertificateAuth(), BasicAuth(lookup))

// Refuse with a status of the authenticator's choice (400–599; others: 401).
type StatusError struct{ Code int; Err error }
func (e *StatusError) Error() string
func (e *StatusError) Unwrap() error

var ErrUnknownStation error // an authenticator's error for an unknown identity: HTTP 404
var ErrUnauthorized error   // BasicAuth's error for refused credentials: HTTP 401
```

- `BasicAuth` requires the user name to equal the station identity and
  compares passwords in constant time; an identity `lookup` does not know
  gets 404 (OCPP-J §3.2), wrong credentials 401.
- For profile 3, `CertificateAuth` admits a station whose verified client
  certificate's CommonName is its identity (the TLS handshake only proves
  the certificate is trusted). For another mapping, check
  `r.TLS.PeerCertificates[0]` in an `AuthenticatorFunc`.
- `AnyOf` combines them for a CSMS with stations on several profiles. It
  answers 404 only when every authenticator says the station is unknown.
- Handlers see how a station connected with `p.(*server.Session).TLS()`.
- Authentication runs before the upgrade, so a refused station never gets a
  session and cannot replace one.

## Options

| Option | Default | Meaning |
|---|---|---|
| `WithHandler(v, d)` | required | Dispatcher for stations' CALLs on version `v`; the versions accepted are those with one. For 2.1 SENDs it also implements `ocpp.SendDispatcher` (the ocpp21 dispatchers do) |
| `WithAuthenticator(a)` | required, or `WithoutAuthentication()` | Decides before the upgrade |
| `WithoutAuthentication()` | — | Accepts every identity: closed networks and tests only |
| `WithTLS(cfg)` | none | TLS for `ListenAndServe` and `Serve` (profiles 2 and 3; `ClientAuth`, `ClientCAs` for 3) |
| `WithSessionHandler(onConnect, onDisconnect)` | none | Session start; session end with nil (local Close or Shutdown), `ErrReplaced`, or the cause |
| `WithReplacePolicy(p)` | `ReplaceExisting` | Or `RejectNew` (HTTP 409) |
| `WithMaxStations(n)` | 10 000 | Simultaneous sessions; then HTTP 503 |
| `WithOriginPatterns(p...)` | none | Hosts besides the server's own that a browser's Origin may name |
| `WithHandshakeTimeout(d)` | 10 s | Reading the request headers (own listener only) |
| `WithResponseTimeout(d)` | 30 s | Wait for a station's response |
| `WithWriteTimeout(d)` | 10 s | Writing one message to a station; then its connection is lost |
| `WithPingInterval(d)` | off | WebSocket ping to every station every `d`; no pong within `d` loses the connection |
| `WithMaxQueuedCalls(n)` | 64 | Calls per session waiting for the outgoing slot before `ErrBusy` |
| `WithMaxMessageSize(n)` | 64 KiB | Largest message accepted, after decompression; larger closes with 1009 |
| `WithMaxBadMessages(n)` | off | Close a station's connection with 1008 after `n` malformed frames |
| `WithIncomingRateLimit(perSecond, burst)` | off | Dispatch at most this many of a station's CALLs a second; the rest wait (none is refused) |
| `WithLogger(l)` | silent | `*slog.Logger`; entries carry `component=ocpp.server` |
| `WithMetrics(m)` | none | `ocpp.Metrics` |
| `WithCallInterceptor(i)`, `WithHandlerInterceptor(i)` | none | Wrap outgoing and incoming calls (tracing) |
| `WithFrameObserver(f)` | none | Sees every frame of every station, raw |

## Shutdown

- `Shutdown(ctx)` stops accepting connections (new upgrades get 503) and
  drains every session: new CALLs and SENDs to stations fail with
  `ocpp.ErrClosed`, CALLs from stations that have not started are no longer
  dispatched, and what is in flight (a CALL awaiting its answer, a handler
  that has started, a frame queued or being written) is finished or ctx
  ends. It then closes every session with status 1001 (going away) and
  waits until they have ended or ctx ends. `ListenAndServe` and `Serve` then return
  `ocpp.ErrClosed`.
- `Close` closes at once, without waiting: frames still queued or being
  written are dropped.
- `Session.Shutdown(ctx)` is the same for one session: it drains it, then
  closes it like `Session.Close`.
- Calls in flight on a closed session fail with `ocpp.ErrClosed`.

## Scaling out

A `Server` holds the sessions of its own process: `Station(id)` finds only
stations connected to it. To run several CSMS instances:

- Route each station to one instance at a time. A WebSocket stays on the
  instance that accepted it; a load balancer that balances connections
  (not requests) is enough, and stations reconnect elsewhere when an
  instance stops (`Shutdown` closes with 1001).
- Record where each station is connected in a shared store from the
  session handler (`WithSessionHandler`): the instance on connect, removed
  on disconnect unless the error is `ErrReplaced` (the station moved). Send
  a CALL to a station through the instance that holds it, for example over
  a message broker the application already runs.
- With `ReplaceExisting`, a station that reconnects to the same instance
  replaces its session there; one that reconnects to another instance
  leaves the old session to end by keepalive (`WithPingInterval`) or by its
  next failed write.

The library itself needs no shared state, so instances are independent.

## Errors by method

| Method | Errors |
|---|---|
| `New` | `ocpp.ErrInvalidOption` |
| `ListenAndServe`, `Serve` | `ocpp.ErrClosed` after `Shutdown` or `Close`; listener errors |
| `Shutdown` | `ctx.Err()` |
| `Session.Call` | `*ocpp.CallError`, `ocpp.ErrTimeout`, `ocpp.ErrConnectionLost`, `ocpp.ErrClosed` (also `server.ErrReplaced`), `ocpp.ErrBusy`, `ctx.Err()`, `ocpp.ErrProtocol` |
| typed call functions on a session | as `Session.Call`, and `*ocpp.ValidationError`, `ocpp.ErrWrongDirection`, `ocpp.ErrUnsupportedVersion` |
| `Session.Send` | `ocpp.ErrUnsupportedVersion`, `ocpp.ErrClosed`, `ocpp.ErrConnectionLost`, `ctx.Err()` |
| `Session.Err` | nil while connected; `ocpp.ErrClosed`, `server.ErrReplaced`, or an error wrapping `ocpp.ErrConnectionLost` (and an `*ocpp.CloseError` when the station sent a close frame: `Normal()` when it closed on purpose) |
| `Authenticator` results | `ErrUnknownStation` → 404, `*StatusError` → its code, any other error → 401 |
