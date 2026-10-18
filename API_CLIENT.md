# API Reference: package client (Charging Station)

`github.com/otfabric/go-ocpp/client` is the Charging Station role (OCPP 1.6:
Charge Point): it connects to a CSMS, sends CALLs, and answers the CSMS's
CALLs through a dispatcher. Shared types are in [API.md](API.md), the CSMS
role in [API_SERVER.md](API_SERVER.md), errors in [ERRORS.md](ERRORS.md).

## Contents

- [Quick start](#quick-start)
- [Constructor and URL](#constructor-and-url)
- [Methods](#methods)
- [Options](#options)
- [States and reconnection](#states-and-reconnection)
- [Security profiles](#security-profiles)
- [Errors by method](#errors-by-method)

## Quick start

```go
c, err := client.New("wss://csms.example.com/ocpp/CS001",
    client.WithHandler(ocpp.V201, ocpp201.ChargingStation(myHandler)), // answers the CSMS
    client.WithBasicAuth("CS001", password),                           // security profile 2
    client.WithCompression(),
    client.WithReconnect(client.DefaultReconnect()),
    client.WithPingInterval(30*time.Second),
)
if err != nil { ... }
if err := c.Connect(ctx); err != nil { ... }
defer c.Close()

boot, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
    Reason:          ocpp201.BootReasonPowerUp,
    ChargingStation: ocpp201.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
})
```

The handler embeds `ocpp201.UnimplementedChargingStationHandler` and
implements the actions it supports; the others are answered `NotSupported`.
With handlers for several versions, the client offers all of them and the
CSMS picks one (`c.Version()` after `Connect`). Runnable programs are in
[examples](examples/README.md).

## Constructor and URL

```go
func New(rawURL string, opts ...Option) (*Client, error)
```

- `rawURL` is the CSMS's OCPP-J endpoint followed by `/` and the station
  identity, percent-encoded (`wss://host/ocpp/RDAM%7C123`). The identity
  is the last path segment.
- `wss://` is required; `ws://` needs `WithInsecureTransport` (security
  profile 1 on closed networks and tests).
- Credentials in the URL are refused; use `WithBasicAuth`.
- At least one version: `WithVersions` or `WithHandler`.
- Every refusal wraps `ocpp.ErrInvalidOption`. New does no I/O.

## Methods

```go
func (c *Client) Connect(ctx context.Context) error
func (c *Client) Call(ctx context.Context, action string, req, resp any) error
func (c *Client) Send(ctx context.Context, action string, req any) error // OCPP 2.1
func (c *Client) Close() error
func (c *Client) Shutdown(ctx context.Context) error
func (c *Client) SetReconnect(r *Reconnect) error
func (c *Client) ID() string            // the identity from the URL
func (c *Client) Role() ocpp.Role       // ocpp.ChargingStation
func (c *Client) Version() ocpp.Version // negotiated; zero before the first connection
func (c *Client) State() ocpp.State

func DefaultReconnect() Reconnect // 5 s + up to 10 s, doubled up to 5 times
type Reconnect struct{ WaitMinimum, RandomRange time.Duration; RepeatTimes int }

const (
    DefaultHandshakeTimeout = 30 * time.Second
    DefaultResponseTimeout  = 30 * time.Second
    DefaultMaxQueuedCalls   = 64
    DefaultMaxMessageSize   = 64 << 10
)
```

| Method | Behaviour |
|---|---|
| `Connect` | Dials once: TCP, TLS, the WebSocket handshake with the offered subprotocols. Returns nil when the connection is open. It does not retry; `WithReconnect` redials only after an open connection is lost. On a connected client it does nothing. |
| `Call` | Sends a CALL and waits for its result. Calls from many goroutines are sent one at a time (one outstanding CALL per direction); up to `WithMaxQueuedCalls` wait, more get `ErrBusy`. Fails at once with `ErrNotConnected` while disconnected: nothing is queued. The typed call functions (`ocpp201.BootNotification(ctx, c, …)`) use it. |
| `Send` | OCPP 2.1 SEND: queued and returned at once, never answered, does not wait for the CALL slot. `ErrUnsupportedVersion` on other versions. |
| `Close` | Closes now: pending calls fail with `ErrClosed`, reconnection stops. Idempotent and final: a closed client does not connect again (make a new one, as a restarting station would); from a handler it does not wait for that handler. |
| `Shutdown` | Graceful close: new calls fail with `ErrClosed`, calls already made get their answers and queued SENDs are written, then `Close`. Returns `ctx.Err()` if ctx ends first (closed anyway). |
| `SetReconnect` | Changes the back-off from the next attempt on, for a CSMS that sets the RetryBackOff variables (2.x OCPPCommCtrlr); nil stops reconnecting after the next loss. |

## Options

| Option | Default | Meaning |
|---|---|---|
| `WithVersions(v...)` | versions with a handler, newest first | Versions offered, in order of preference |
| `WithHandler(v, d)` | none: the CSMS's CALLs get `NotImplemented` | Dispatcher for the CSMS's CALLs on version `v`; for 2.1 SENDs it also implements `ocpp.SendDispatcher` (the ocpp21 dispatchers do) |
| `WithBasicAuth(user, pass)` | none | HTTP Basic credentials (security profiles 1 and 2); the user name must be the identity in the URL (2.x Part 2 A00), or `New` refuses it |
| `WithTLS(cfg)` | system roots | Trust roots and client certificate (profile 3); MinVersion raised to TLS 1.2; rotation via `GetClientCertificate` |
| `WithHTTPClient(c)` | `http.DefaultClient` | Handshake HTTP client (proxies); not with `WithTLS` |
| `WithHeader(name, value)` | none | Extra handshake header (an API key for a proxy, a tenant); Authorization, Host, Connection, Upgrade and Sec-WebSocket-* are refused |
| `WithInsecureTransport()` | off | Allow `ws://` |
| `WithCompression()` | off | Offer RFC 7692 compression (2.x CSMSs must support it) |
| `WithReconnect(Reconnect{…})` | off | Redial after a lost connection with the back-off of 2.x Part 4 §5.4; see `DefaultReconnect()` |
| `WithStateHandler(f)` | none | Called on every state change with its cause; must not block or call `Close` |
| `WithHandshakeTimeout(d)` | 30 s | Dial, TLS and WebSocket handshakes |
| `WithResponseTimeout(d)` | 30 s | Wait for a CALL's response (2.x: MessageTimeout of OCPPCommCtrlr) |
| `WithWriteTimeout(d)` | 10 s | Writing one message; then the connection is lost |
| `WithPingInterval(d)` | off | WebSocket ping every `d`; no pong within `d` loses the connection (WebSocketPingInterval) |
| `WithMaxQueuedCalls(n)` | 64 | Calls waiting for the outgoing slot before `ErrBusy` |
| `WithMaxMessageSize(n)` | 64 KiB | Largest message accepted, after decompression; larger closes with 1009 |
| `WithMaxBadMessages(n)` | off | Close with 1008 after `n` malformed frames |
| `WithIncomingRateLimit(perSecond, burst)` | off | Dispatch at most this many of the CSMS's CALLs a second; the rest wait |
| `WithLogger(l)` | silent | `*slog.Logger`; entries carry `component=ocpp.client` |
| `WithMetrics(m)` | none | `ocpp.Metrics` |
| `WithCallInterceptor(i)` | none | Wraps every outgoing CALL and SEND (tracing) |
| `WithHandlerInterceptor(i)` | none | Wraps every incoming CALL and SEND |
| `WithFrameObserver(f)` | none | Sees every frame, raw |

## States and reconnection

```
Disconnected ──Connect──▶ Connecting ──▶ Connected ──lost──▶ Disconnected
                                   ▲                              │
                                   └──── WithReconnect: back-off ─┘
any ──Close / Shutdown──▶ Closed
```

- On a lost connection, pending and queued calls fail with
  `ErrConnectionLost`; when the CSMS sent a close frame, the error also
  wraps an `*ocpp.CloseError` with its status (`Normal()` for 1000 and
  1001). Nothing is resent: the application decides what to
  send again (for transactions, 1.6 §4.3 / 2.x E04: the station's queue).
- The back-off before attempt n is
  `WaitMinimum · 2^min(n-1, RepeatTimes) + rand[0, RandomRange]`
  (2.x Part 4 §5.4).
- While reconnecting, `Call` fails with `ErrNotConnected`; the state
  handler and `Metrics.OnReconnectAttempt` report each attempt.

## Security profiles

| Profile | Options |
|---|---|
| 1: Basic over `ws://` | `WithInsecureTransport()`, `WithBasicAuth(id, password)` |
| 2: TLS + Basic | `WithBasicAuth(id, password)`, `WithTLS(&tls.Config{RootCAs: …})` when the CSMS's CA is not in the system roots |
| 3: mutual TLS | `WithTLS(&tls.Config{RootCAs: …, Certificates: […]})` or `GetClientCertificate` |

TLS verification cannot be turned off. See
[examples/security-profiles](examples/security-profiles) and
[SECURITY.md](SECURITY.md).

## Errors by method

```go
// The HTTP status of a refused handshake, wrapped next to ocpp.ErrConnectFailed.
type HandshakeError struct{ StatusCode int }
func (e *HandshakeError) Error() string // "HTTP 401 Unauthorized"
```

| Method | Errors (test with `errors.Is` / `errors.As`) |
|---|---|
| `New` | `ocpp.ErrInvalidOption` |
| `Connect` | `ocpp.ErrConnectFailed`, with a `*HandshakeError` (`StatusCode`) when the CSMS refused the handshake, and `ocpp.ErrUnsupportedVersion` when no subprotocol was agreed; `ocpp.ErrClosed` |
| `Call` | `*ocpp.CallError` (the CSMS answered with a CALLERROR), `ocpp.ErrTimeout`, `ocpp.ErrNotConnected`, `ocpp.ErrConnectionLost`, `ocpp.ErrClosed`, `ocpp.ErrBusy`, `ctx.Err()`, `ocpp.ErrProtocol` (a response that does not decode) |
| typed call functions | as `Call`, and `*ocpp.ValidationError` (wraps `ocpp.ErrInvalidMessage`; a response failing validation also wraps `ocpp.ErrProtocol`), `ocpp.ErrWrongDirection`, `ocpp.ErrUnsupportedVersion` |
| `Send` | `ocpp.ErrUnsupportedVersion` (not 2.1), `ocpp.ErrNotConnected`, `ocpp.ErrClosed`, `ctx.Err()` |
| `Shutdown` | `ctx.Err()` |
| `SetReconnect` | `ocpp.ErrInvalidOption` |
