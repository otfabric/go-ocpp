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
| Ping/pong keepalive | Y | n/a | Y | Y | Y | Y | Y | U | Y (Phase 7) |
| Reconnect with the 2.x back-off | Y | n/a | U | Y | n/a | Y | Y | Y | Y |
| Offline queue, TransactionMessageAttempts retry | P | N | P | Y (SQLite) | n/a | P | Y (SQLite) | Y | N |
| One outstanding CALL, response timeouts | Y | Y | U | Y | Y | Y | Y | Y | Y |
| Call and handler interceptors, frame observer | N | P | N | P | P | P | U | N | Y |
| Pluggable logger | Y | Y | Y | Y | Y | events | Y | Y | Y (slog) |
| Metrics hooks / OpenTelemetry | N | N | N | N | Y (OTel) | N | N | N | P (hooks, no exporter) |
| Raw calls and vendor actions | P | Y | P | P | Y | Y | U | P | Y |
| Authentication before the upgrade, fail-closed | Y | n/a | U | n/a | Y | Y | Y | n/a | Y |
| Session replacement policy | U | n/a | U | n/a | U | U | U | n/a | Y |
| Limits: stations, message size, queues, bad messages | P | N | P | U | U | P | U | U | Y |
| Graceful shutdown | Y | n/a | U | U | Y | Y | U | n/a | Y (server) |
| Public test kit / mocks | Y | P | P | P | P | N | U | Y | P (internal) |
| Device model / configuration key helpers | N | N | N | Y | Y | N | Y | Y | N |
| Certificate helpers (hash data, CSR, OCSP) | N | N | N | Y | Y | N | Y | P | N |
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

Library scope: belongs in go-ocpp.

| # | Feature | Priority | Status |
|---|---|---|---|
| 1 | WebSocket ping/pong keepalive and dead-peer detection | P0 | Done (Phase 7): `WithPingInterval`, abort without the closing handshake |
| 2 | Close a peer after repeated malformed frames | P0 | Done (Phase 7): `WithMaxBadMessages` |
| 3 | Configurable write timeout (a peer that stops reading) | P0 | Done (Phase 7): `WithWriteTimeout` |
| 4 | Per-station rate limit of incoming CALLs | P1 | Open. Possible today with a `HandlerInterceptor`; a built-in limiter would make it one option |
| 5 | Runtime reconfiguration of the client: back-off, URL, network profile fallback (NetworkConfigurationPriority), security profile upgrade | P1 | Open |
| 6 | Per-action validation exemption, logged, for interop with stations that send invalid messages | P1 | Open (ARCHITECTURE.md allows it "only per action and logged") |
| 7 | Public test kit (`ocpptest`): in-memory pipe, fake peer, recording dispatchers | P1 | Open |
| 8 | Custom handshake headers and an Origin check | P1 | Open (headers only through `WithHTTPClient`, which excludes `WithTLS`) |
| 9 | Client close that waits for pending calls | P2 | Open |
| 10 | Per-call response timeout | P2 | Open (the context deadline bounds the wait, not the slot) |
| 11 | Local Controller role (2.x), composing client and server | P2 | Open |
| 12 | Case-insensitive station identities: identifierString is case-insensitive (2.x Part 2), sessions are keyed by the exact string | P1 | Open question: settle against the OCA text before changing session lookup |

Application scope: out of scope per ARCHITECTURE.md; at most an optional
subpackage with no new dependency, each with an ADR.

| # | Feature | Priority | Status |
|---|---|---|---|
| 13 | Offline queue with TransactionMessageAttempts/RetryInterval (1.6 §4.3, 2.x E04/E10), behind a `Store` interface | P1 | Open; the largest gap against the union. Changes a non-goal: needs an ADR |
| 14 | Certificate helpers: CertificateHashData, CSR for SignCertificate, OCSP request data | P1 | Open |
| 15 | Heartbeat scheduler | P2 | Open |
| 16 | Typed constants for 1.6 configuration keys and 2.x components and variables (no storage) | P2 | Open |
| 17 | OpenTelemetry / Prometheus adapters on the hooks, as a separate module | P2 | Open; `examples/observability` shows where they plug in |
| 18 | Scaling documentation (several CSMS instances, a broker) | P2 | Open |

Will not do: smart charging calculation, signed meter values, firmware and
log upload, ISO 15118 contract logic, local authorization lists,
persistence (application logic); OCPP 1.6 SOAP, 1.5 and older.

## Interop gaps

- ChargeTimeEU 2.1 (SEND, CALLRESULTERROR) exists on its master branch only;
  the reference adapters are pinned to releases (ocpp-interop R2.2).
- No reference release sends SEND or CALLRESULTERROR: go-ocpp's are tested
  against go-ocpp only.
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
