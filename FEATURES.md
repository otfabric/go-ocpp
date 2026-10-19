# Features and gaps

go-ocpp aims to be a superset of the library features of the open-source
OCPP implementations, without their charging logic: what a library can do
for every application, done with strict typing, validation against the OCA
schemas, and tests against independent implementations. This file tracks
where it stands. It is updated when a feature lands or the comparison
changes; COMPATIBILITY.md holds the per-action coverage.

Research of 2026-10-18 (sources below). Cells: Y yes, N no, P partial,
U unverified, app = left to the application or product, n/a = delegated to
another library. A grep-based N is weaker than a documented one.

## Comparison

OG lorenzodonini/ocpp-go v0.19.0 (Go) · MH mobilityhouse/ocpp 2.1.0
(Python) · JC ChargeTimeEU/Java-OCA-OCPP v2.0 (Java) · LO EVerest libocpp
v0.31.1 (C++, now in the EVerest monorepo) · CO CitrineOS core v2.0.1
(TypeScript CSMS) · RPC mikuso/ocpp-rpc 2.2.1 (Node.js) · OO
c-jimenez/open-ocpp v2.0.0-alpha (C++) · MO MicroOcpp v1.2.0 (C/C++) ·
GO go-ocpp.

| Feature | OG | MH | JC | LO | CO | RPC | OO | MO | GO |
|---|---|---|---|---|---|---|---|---|---|
| OCPP 1.6J (+ Security Whitepaper) | Y | Y | Y (U) | Y | Y | Y (n/a) | Y | Y (P) | Y |
| OCPP 2.0.1 | Y | Y | Y | Y | Y | Y | Y | P | Y |
| OCPP 2.1 with SEND and CALLRESULTERROR | N | P (no SEND) | master only | Y | Y | Y | N | N | Y |
| Station and CSMS roles | Y | Y | Y | station | CSMS | Y | Y | station | Y |
| Several versions on one endpoint | U | P | Y | n/a | Y | Y | U | n/a | Y |
| Validation against the OCA schemas, both directions | P (struct tags) | Y | P (hand-written) | U | Y | opt-in | Y | U | Y (generated, always on) |
| Security profiles 1–3 | Y | n/a | P | Y | Y | Y | Y | P | Y |
| WebSocket compression (RFC 7692) | U | n/a | Y | N | Y | U | U | N | Y |
| Ping/pong keepalive | Y | n/a | Y | Y | Y | Y | Y | U | Y |
| Reconnect with the 2.x back-off | Y | n/a | U | Y | n/a | Y | Y | Y | Y |
| Offline queue, TransactionMessageAttempts retry | P | N | P | Y (SQLite) | n/a | P | Y (SQLite) | Y | N |
| One outstanding CALL, response timeouts | Y | Y | U | Y | Y | Y | Y | Y | Y |
| Call and handler interceptors, frame observer | N | P | N | P | P | P | U | N | Y |
| Pluggable logger | Y | Y | Y | Y | Y | events | Y | Y | Y (slog) |
| Metrics hooks / OpenTelemetry | N | N | N | N | Y (OTel) | N | N | N | P (hooks, no exporter) |
| Raw calls and vendor actions | P | Y | P | P | Y | Y | U | P | Y |
| Authentication before the upgrade, fail-closed | Y | n/a | U | n/a | Y | Y | Y | n/a | Y |
| Session replacement policy | U | n/a | U | n/a | U | U | U | n/a | Y |
| Limits: stations, message size, queues, bad messages, rate | P | N | P | U | U | P | U | U | Y |
| Graceful shutdown | Y | n/a | U | U | Y | Y | U | n/a | Y (server and client) |
| Public test kit / mocks | Y | P | P | P | P | N | U | Y | Y (`ocpptest`) |
| Device model / configuration key helpers | N | N | N | Y | Y | N | Y | Y | N |
| Certificate helpers (hash data, CSR, OCSP) | N | N | N | Y | Y | N | Y | P | P (`ocppcert` hash data) |
| Code generated from the OCA schemas | N | N | N | Y | Y | bundled | N | N | Y |
| Tested against independent implementations | U | U | U | OCA cert. | OCA cert. | U | U | U | Y (ocpp-interop) |

SteVe and OCPP.Core (CSMS products, GPL-3.0) and rust-ocpp (types only)
were looked at and left out: they are not libraries of this kind.

## Where go-ocpp leads

No other implementation combines: validation generated from the OCA
schemas, always on in both directions and cross-checked against an
independent validator; 1.6J, 2.0.1 and 2.1 including SEND and
CALLRESULTERROR; fail-closed authentication before the upgrade; bounded
queues and limits everywhere; interceptors and a frame observer; slog and
dependency-free metrics hooks; one production dependency; interop tests
against three independent stacks; chaos, soak and fuzz testing.

## Gaps

Every gap found has a status: **done** (in v0.1.0), **by design** (covered
another way, with the reason), or **planned** (with its priority). Nothing
is left without one.

Library scope: belongs in go-ocpp.

| # | Feature | Priority | Status |
|---|---|---|---|
| 1 | WebSocket ping/pong keepalive and dead-peer detection | P0 | **Done**: `WithPingInterval` (client, server); a dead peer is aborted without the closing handshake |
| 2 | Close a peer after repeated malformed frames | P0 | **Done**: `WithMaxBadMessages` |
| 3 | Write timeout for a peer that stops reading | P0 | **Done**: `WithWriteTimeout` |
| 4 | Per-station rate limit of incoming CALLs | P1 | **Done**: `WithIncomingRateLimit` (client, server); messages over the rate wait, none is refused, as OCPP has no error for it |
| 5 | Runtime reconfiguration of the client | P1 | **Done** for the back-off: `Client.SetReconnect` (RetryBackOff* set by the CSMS). **Planned (P1)**: switching URL and security profile after SetNetworkProfile, and fallback along NetworkConfigurationPriority; until then the application creates a new `Client` with the new profile |
| 6 | Accepting messages of stations that do not conform | P1 | **By design**: typed validation stays always on. An application that must accept a broken station handles that action with a raw `ocpp.DispatcherFunc` (or calls with `json.RawMessage`) and decodes leniently itself, for that action only (API.md) |
| 7 | Public test kit | P1 | **Done**: package `ocpptest` (`CSMS`, `Station`, `Session`, `Recorder`) |
| 8 | Custom handshake headers, Origin check | P1 | **Done**: `client.WithHeader`; the server refuses foreign browser origins by default, `server.WithOriginPatterns` allows some |
| 9 | Client close that waits for pending calls | P2 | **Done**: `Client.Shutdown(ctx)` |
| 10 | Per-call response timeout | P2 | **By design**: the caller's context deadline bounds each call; the response timeout bounds the slot, which T6 makes a per-connection matter |
| 11 | Local Controller role (2.x) | P2 | **Planned (P2)**: a package composing client and server with raw dispatch; no reference stack implements it to test against |
| 12 | Case-insensitive station identities | P1 | **By design**: identities are matched exactly. identifierString is case-insensitive as a data type, but the identity is also a URL path segment and the Basic authentication user name, both case-sensitive; matching exactly never merges two stations. Recorded in ARCHITECTURE.md |

Application scope: out of scope per ARCHITECTURE.md; at most an optional
subpackage with no new dependency.

| # | Feature | Priority | Status |
|---|---|---|---|
| 13 | Offline queue with TransactionMessageAttempts/RetryInterval (1.6 §4.3, 2.x E04/E10), behind a `Store` interface | P1 | **Planned (P1)**, after v0.1.0: changes an ARCHITECTURE non-goal and needs an ADR. Until then the application queues: `Call` fails at once with `ErrNotConnected` or `ErrConnectionLost`, never silently |
| 14 | Certificate helpers | P1 | **Done** for CertificateHashData: package `ocppcert`. **By design** for CSRs and OCSP requests: `crypto/x509.CreateCertificateRequest` and the application's OCSP client build them; there is nothing OCPP-specific to add |
| 15 | Heartbeat scheduler | P2 | **By design**: a ticker calling `Heartbeat` with the interval of the boot response; [examples/station-simulator](examples/station-simulator) shows it |
| 16 | Typed constants for 1.6 configuration keys and 2.x components and variables | P2 | **Planned (P2)**: generated from the OCA appendices |
| 17 | OpenTelemetry / Prometheus adapters | P2 | **Planned (P2)** as a separate module, so the library keeps one dependency; [examples/observability](examples/observability) shows where they plug in |
| 18 | Running several CSMS instances | P2 | **Done** (documentation): API_SERVER.md, "Scaling out" |

Will not do: smart charging calculation, signed meter values, firmware and
log upload, ISO 15118 contract logic, local authorization lists,
persistence (application logic; the examples show how an application
does them); OCPP 1.6 SOAP, 1.5 and older.

## API ergonomics

Found by writing the use-case examples against the public API.

| # | Friction | Status |
|---|---|---|
| 19 | A handler cannot act after its response is sent (NotifyReport after GetBaseReport, status notifications after UpdateFirmware, a reboot after Reset) | **Done**: `ocpp.AfterResponse`; the examples use it |
| 20 | A station's deliberate close looks like a lost connection | **Done**: `*ocpp.CloseError` with `Normal()`, wrapped next to `ErrConnectionLost` |
| 21 | `Connect` reported the HTTP status only in the text | **Done**: `*client.HandshakeError{StatusCode}` |
| 22 | An authenticator could only refuse with 401 or 404 | **Done**: `*server.StatusError` (any 4xx or 5xx) |
| 23 | `BasicAuth` does not compose with certificate authentication | **Done**: `server.CertificateAuth`, `server.AnyOf` |
| 24 | `WithBasicAuth` repeats the identity, which may mismatch | **Done**: `client.New` refuses a user name that is not the identity |
| 25 | Handlers cannot see how a station connected | **Done**: `Session.TLS()` |
| 26 | 2.0.1 and 2.1 have distinct types, so multi-version 2.x code is written twice; `IdTokenType.type` is an enumeration in 2.0.1 and a string in 2.1 | **By design**: the types follow each version's schemas (ADR 0001), which do differ (2.1 opened the id token types); sharing them would hide such differences |
| 27 | Wrapping a generated dispatcher in `ocpp.DispatcherFunc` drops its `DispatchSend` | **By design**, documented in API.md: a wrapper that must keep SENDs implements `ocpp.SendDispatcher` too and forwards to the wrapped dispatcher |
| 28 | A closed `Client` cannot connect again | **By design**, documented in API_CLIENT.md: `Close` is final, as for `net/http`'s servers; a restarting station makes a new `Client` (examples/remote-management) |

## Interop gaps

Against otfabric/ocpp-interop v0.4.0 (INTEROPERABILITY.md):

- **Done**: every action of 1.6J (39/39) and 2.0.1 (64/64), and 90 of 91 of
  2.1, is exchanged with an independent stack
  ([docs/compatibility](docs/compatibility)); TLS (security profiles 2 and
  3) and compression are tested against the references in both roles; OCPP
  2.1 is tested against three independent implementations: two CSMSs (The
  Mobility House, CitrineOS core v2.0.1) with go-ocpp as the station, and
  two stations (The Mobility House, EVerest libocpp) with go-ocpp as the
  CSMS.
- CitrineOS answers by itself, not from the fixture: go-ocpp's CALLs to it
  outside the actions it declares are checked against the OCA schema only;
  it refuses nine 2.1 station messages with FormatViolation and the 1.6
  Security Whitepaper messages with NotImplemented (skipped, not passed),
  and serves TLS only to a station that sends SNI.
- **Open, blocked upstream**: SEND and CALLRESULTERROR against an
  independent implementation (no released reference implements them; the
  2.1 action NotifyPeriodicEventStream is a SEND); compression with go-ocpp
  as the CSMS against a second station (ChargeTimeEU's is defective in
  2.0.1, ocpp-go and EVerest cannot, CitrineOS has no station).
- Mobility House 2.1.0 does not answer 2.1 CALLs it has no handler for
  (fixed on master, not released); BatterySwap cannot be sent by it.

## Sources

- https://github.com/lorenzodonini/ocpp-go (ws/websocket.go, ocppj/ocppj.go, ocppj/dispatcher.go, docs/testing.md)
- https://github.com/mobilityhouse/ocpp (ocpp/charge_point.py, routing.py, ocpp/experimental)
- https://github.com/ChargeTimeEU/Java-OCA-OCPP (JSONConfiguration.java, Communicator.java, MultiProtocolJSONServer.java)
- https://github.com/EVerest/libocpp (include/ocpp: message_queue.hpp, evse_security.hpp, ocsp_updater.hpp)
- https://github.com/citrineos/citrineos-core (packages/types/src/config/types.ts)
- https://github.com/mikuso/ocpp-rpc (lib/client.js)
- https://github.com/c-jimenez/open-ocpp (README_ocpp16.md)
- https://github.com/matth-x/MicroOcpp
- https://github.com/steve-community/steve, https://github.com/dallmann-consulting/OCPP.Core, https://github.com/codelabsab/rust-ocpp
