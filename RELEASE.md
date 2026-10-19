# go-ocpp Releases

Releases are cut with the `Release` workflow (semantic versioning, annotated
tag, GitHub release); the section of the version below is the release's
notes. Before v1.0.0 a minor release may change the API: every change is
listed here, and COMPATIBILITY.md has the stability rules.

## v0.1.2

**Date:** 2026-10-19
**Previous release:** v0.1.1

## Summary

Two fixes found while building ocppctl on go-ocpp: a graceful shutdown no
longer loses a frame that is being written, and integers follow the JSON
Schema draft of each OCA schema, so `20000.0` is an integer in OCPP 1.6
Security, 2.0.1 and 2.1 messages (EVerest libocpp writes integers so).
Interop moves to ocpp-interop v0.4.0, with CitrineOS as a fifth reference
and the second independent OCPP 2.1 CSMS.

**Upgrade notes:** `Server.Shutdown` now drains the sessions before it
closes them: CALLs and SENDs to stations made after it starts fail with
`ErrClosed`, and it waits (bounded by its context) for what is in flight;
use `Close` to close at once. One addition to the API: `Session.Shutdown`.

## Changes

### Fixed

- **A frame being written was lost on `Client.Shutdown`.** The endpoint
  counted itself idle once its writer had taken a frame off the queue, before
  the write completed, and did not count queued answers at all; `Close` then
  cancelled the write. A SEND queued just before `Shutdown`, or the answer of
  a handler followed by a `Shutdown` from `ocpp.AfterResponse` (a station
  that accepts a Reset and restarts), could be lost. The endpoint now counts
  every frame until it is written, and every incoming CALL until its answer
  is queued. `Shutdown` drains: new outgoing CALLs and SENDs fail with
  `ErrClosed`, incoming CALLs that have not started are no longer
  dispatched, and it waits until what is in flight is done, so it also ends
  on a connection that is busy in both directions.
- **Integral numbers refused for integers.** A number with a zero fractional
  part (`20000.0`, `2e4`) was refused with TypeConstraintViolation for an
  integer field. The OCPP 1.6 Security Whitepaper, 2.0.1 and 2.1 schemas are
  JSON Schema draft-06, in which such a number is an integer; it is now
  accepted in their messages (and decoded to the Go `int`). A number with a
  fraction is still refused. The OCPP 1.6 core schemas are draft-04, in which
  an integer has no fraction or exponent: their messages still refuse
  `20000.0`. Found with EVerest libocpp's NotifyEVChargingNeeds (2.0.1).

### Added

- `Session.Shutdown(ctx)`: the graceful close of one session, as
  `Client.Shutdown` is for a station.

### Changed

- `Server.Shutdown(ctx)` drains the sessions (as `Client.Shutdown` does)
  before it closes them with 1001.
- `Close` (client, session) is documented as dropping frames still queued or
  being written; `Shutdown` is the graceful close.
- ARCHITECTURE.md: the integer rule per schema draft, as an implementation
  choice.
- **Interop moved to otfabric/ocpp-interop v0.4.0** (pinned by digest in
  `interop/harness.go`, the Makefile and `interop.yml`), which adds a fifth
  reference, `citrineos` (CitrineOS core v2.0.1, TypeScript): a CSMS for
  1.6J, 2.0.1 and 2.1, the second independent OCPP 2.1 CSMS. go-ocpp as an
  OCPP 2.1 station is now tested against two independent CSMSs (The Mobility
  House, CitrineOS), and 2.1 against three implementations in all.
- The interop harness reads the capability feature `fixtureAnswers`
  (contract 0.5; true when not declared). Against a CSMS that answers by
  itself (`fixtureAnswers: false`), a CALL from go-ocpp with an action the
  CSMS does not declare in `handles` is not compared with the fixture: a
  CALLRESULT must pass go-ocpp's validation of the version, a CALLERROR must
  carry a defined error code and is skipped as unsupported with its code and
  reason, never passed (as ocpp-interop's `scripts/interop.sh` does).
- `TestStationTLS` connects to each reference CSMS by address and by host
  name (`-sni`): CitrineOS serves TLS only to a client that sends SNI, a
  known upstream defect recorded with its evidence. The compression test
  connects again only after the reference has reported the probe
  connection closed. The readiness timeout of a reference container is
  180 s (CitrineOS starts PostgreSQL and RabbitMQ first).

## Verification

- New regression tests, each failing on v0.1.1: `TestClientShutdownWritesQueuedSend`,
  `TestClientShutdownAfterResponse`, `TestSessionShutdownWritesQueuedSend`,
  `TestServerShutdownAnswersCallInHandler` (e2e, 100 rounds each),
  `TestIdleWhileHandling` (engine), `TestIntegralNumbers` and
  `TestUnmarshalIntegralFloats` (wire), `TestIntegersFollowTheSchemaDraft`
  (1.6) and `TestIntegralFloatsAreIntegers` (2.0.1, 2.1).
- `make interop` against the five ocpp-interop v0.4.0 images (2026-10-19,
  linux/arm64): 1426 scenarios pass, 0 fail, 195 skipped with their reasons
  (1.6J 390 / 43, 2.0.1 620 / 46, 2.1 415 / 105; leaf tests of
  `go test -json`). With CitrineOS as the CSMS: 237 pass, 87 skipped (no
  station role, CSMS actions its REST API cannot send, CALLERRORs of its own
  for samples it does not process, TLS without SNI); go-ocpp's CALLs it
  answers pass as 12 / 27 / 29 (1.6J / 2.0.1 / 2.1), its CALLs to go-ocpp as
  18 / 38 / 39, with Basic authentication, TLS profiles 2 and 3 and
  compression in every version. No known issue was removed: none existed
  only because of integral numbers. `make compat` regenerated
  docs/compatibility with the citrineos columns.
- `make check`.

---

## v0.1.1

**Date:** 2026-10-18
**Previous release:** v0.1.0

## Summary

Interoperability qualified against **otfabric/ocpp-interop v0.3.1**: four
independent implementations, now with TLS (security profiles 2 and 3),
compression, every OCPP action, and OCPP 2.1 against two independent
implementations (The Mobility House, and EVerest libocpp as a station). No
change to the library or its API.

**Upgrade notes:** none.

## Changes

### Added

- **Interop against EVerest libocpp** (everest-core 2026.02.1, a station for
  1.6J, 2.0.1 and 2.1), the second independent OCPP 2.1 implementation.
- **TLS interop** (`TestStationTLS`, `TestReferenceStationTLS`): security
  profiles 2 and 3 in both roles with a CA generated per run, and the
  refusals of a wrong password, another station's certificate and an
  untrusted server.
- **Compression interop** (`TestStationCompression`,
  `TestReferenceStationCompression`), with the negotiation checked on the
  wire.
- **Every action against the references**: the ocpp-interop fixtures have a
  sample of every action, so 39/39 (1.6J), 64/64 (2.0.1) and 90/91 (2.1)
  are exchanged with an independent stack (docs/compatibility; the 2.1
  exception is NotifyPeriodicEventStream, a SEND no reference supports).
- **e2e**: `TestMatrix` exchanges every action of every version over plain
  WebSocket, compression and security profiles 1, 2 and 3;
  `TestCompressionNegotiated` checks that compression is negotiated.

### Changed

- The interop harness runs a CALL whose action a reference does not declare
  handling and counts it as passed when it is answered as the fixture says;
  otherwise it is skipped. Known upstream defects added: ocpp-go's
  GetCompositeSchedule and SetNetworkProfile, ChargeTimeEU's 2.0.1
  compression, libocpp's ReportDERControl.
- Pinned images: ocpp-interop v0.3.1 (was v0.2.0), four references.

## Verification

- Interop against the pinned v0.3.1 images (linux/arm64): 1237 scenarios
  pass, 0 fail, 98 skipped with their reasons (1.6J 289/5, 2.0.1 456/20, 2.1
  261/24). TLS profiles 2 and 3 pass with every reference in both roles;
  compression with The Mobility House in both roles and all versions.
- `make check`, as for v0.1.0.

---

## v0.1.0

**Date:** 2026-10-18
**Previous release:** none (first release)

## Summary

The first release of go-ocpp: a pure Go library for the Open Charge Point
Protocol, in both roles, the Charging Station (`client`) and the CSMS
(`server`), for **OCPP 1.6J** (with the Security Whitepaper Edition 4
extensions), **OCPP 2.0.1** (Edition 4) and **OCPP 2.1** (Edition 2),
including 2.1's SEND and CALLRESULTERROR message types.

Every action of every version — 39, 64 and 91 — is a Go type generated from
the OCA JSON schemas, validated in both directions before a handler sees it
or the peer receives it, and dispatchable in both roles. One RPC engine
serves both roles and all three versions; one server can serve all three
versions on one endpoint. The library has a single dependency
(coder/websocket).

It has been tested against three independent implementations — The
Mobility House ocpp, ChargeTimeEU Java-OCA-OCPP and lorenzodonini/ocpp-go —
in both roles, against itself for every action of every version, and
under fuzzing, chaos, soak and resource-exhaustion tests with the race
detector.

**Upgrade notes:** none; this is the first release.

## Changes

### Added

**Protocol**

- **OCPP 1.6J, 2.0.1 and 2.1** — packages `ocpp16` (39 actions: core and
  Security Whitepaper Ed. 4), `ocpp201` (64) and `ocpp21` (91), generated
  from the unmodified OCA schemas (`schemas/`, CC BY-ND 4.0, with SHA-256
  sums) by `internal/gen`: a type for every request, response, object and
  enumeration; strict decoding (unknown properties, `null`, missing
  required fields); validation; call functions that check version,
  direction and request; handler interfaces with `Unimplemented…` embeds;
  dispatchers for both roles; `Actions`, `ValidateRequest`,
  `ValidateResponse`; `coverage.json` per version.
- **OCPP-J framing** — package `ocppj`: the frame codec of every version
  (CALL, CALLRESULT, CALLERROR; CALLRESULTERROR and SEND in 2.1), strict
  decoding, per-version error codes, size limits.
- **SEND (2.1)** — `ocpp.Sender` (`Client.Send`, `Session.Send`) and
  `ocpp.SendDispatcher`; `ocpp21.NotifyPeriodicEventStream` returns only an
  error and is never answered; a SEND does not wait for the outgoing CALL.
- **CALLRESULTERROR (2.1)** — sent when a response fails decoding or
  validation; one received is logged and counted.
- **The rules of the OCA text** (SPECIFICATIONS.md, T1–T11), checked
  against Edition 4 / Edition 2 and the 2026 errata: subprotocol
  negotiation (no common subprotocol: handshake without one, then close);
  one outstanding CALL per direction; message IDs; a corrupt frame answered
  with ID "-1" on 2.x; unknown message types ignored; descriptions cut to
  255 characters on 2.x; 2.x station identities checked; the 2.x reconnect
  back-off; compression required of a CSMS.

**Roles**

- **`client`** — the Charging Station: version negotiation, security
  profiles 1–3 (`WithBasicAuth`, `WithTLS`), compression, reconnection with
  the 2.x back-off (`WithReconnect`, `DefaultReconnect()`,
  `SetReconnect`), keepalive (`WithPingInterval`), custom handshake
  headers (`WithHeader`), graceful `Shutdown`, limits and timeouts, state
  handler; a refused handshake's HTTP status as `*HandshakeError`.
- **`server`** — the CSMS: an `http.Handler` to mount anywhere, or
  `ListenAndServe`/`Serve` with TLS; authentication before the upgrade
  (`WithAuthenticator`, `BasicAuth`, `CertificateAuth`, `AnyOf`,
  `ErrUnknownStation` → 404, `*StatusError` for any other status);
  sessions per station with `Station`, `Stations`, session hooks and a
  replacement policy (`ErrReplaced`); several versions on one endpoint;
  limits on stations, message size, queues, malformed messages and incoming
  rate; Origin check; keepalive; graceful `Shutdown`; `Session.TLS()`.
- **Shared** — `ocpp.Peer`, `Caller`, `Dispatcher`, `CallError` and the
  error codes of each version, `ValidationError`, `CloseError` (how the
  peer closed: `Normal()`), `Ptr`, sentinel errors (ERRORS.md);
  `ocpp.AfterResponse` for what a handler sends after its response.

**Observability and testing**

- `log/slog` logging with `component`, station, version and session
  attributes, never credentials; `ocpp.Metrics` callbacks; call and handler
  interceptors for tracing; a frame observer; `ErrHandlerPanic` with the
  stack logged.
- **`ocpptest`** — a test kit for applications: a CSMS and a station on
  loopback, sessions, and a `Recorder` dispatcher with canned answers.
- **`ocppcert`** — CertificateHashData of a certificate, for the
  certificate management messages.

**Examples** (examples/README.md)

- Quick starts: `ocpp16`, `ocpp201`, `ocpp21`, `observability`.
- Use cases: `csms` (one CSMS for 1.6, 2.0.1 and 2.1 with authentication,
  transactions and remote start/stop), `station-simulator` (a station to
  test a CSMS with, for every version), `smart-charging`,
  `security-profiles` (profiles 1, 2 and 3, certificate rotation),
  `device-model`, `remote-management` (reset, trigger, firmware update,
  logs) and `vendor-extensions` (DataTransfer, customData, raw calls).

**Documentation**

- API reference split by role: API.md (shared packages), API_CLIENT.md,
  API_SERVER.md; ERRORS.md with the CALLERROR codes of each version, HTTP
  refusals and close codes; OBSERVABILITY.md; SECURITY.md; ARCHITECTURE.md
  with every implementation choice and its reason; SPECIFICATIONS.md;
  INTEROPERABILITY.md.
- **One action matrix per version** against the reference stacks
  (docs/compatibility, generated by `make compat`), and FEATURES.md: a
  comparison with nine other implementations and every gap with its status.

### Fixed

Found by the hardening tests before release:

- A CALL that became pending just as its connection closed waited for its
  response timeout instead of failing at once.
- After a missed pong, the connection waited for a closing handshake the
  dead peer could not answer, delaying the detection by about 5 s.
- Under `RejectNew`, connections of one identity that arrived together
  could all be admitted, the later ones replacing the earlier; one is now
  admitted and the others refused with 409.

## Verification

- **Every action, both roles, every version**: a go-ocpp station and CSMS
  exchange every action of 1.6J, 2.0.1 and 2.1 over a real WebSocket, in
  the direction the specification gives it, with payloads carrying every
  property and with the required ones only (e2e `TestMatrix`).
- **Schema conformance**: every request and response of every version,
  valid and mutated, against the OCA schemas and an independent JSON Schema
  validator.
- **Interoperability** (INTEROPERABILITY.md) against otfabric/ocpp-interop
  v0.2.0, pinned by digest: OCPP 1.6J and 2.0.1 in both roles against The
  Mobility House ocpp 2.1.0, ChargeTimeEU Java-OCA-OCPP v2.0 and
  lorenzodonini/ocpp-go v0.19.0, and OCPP 2.1 in both roles against The
  Mobility House — every fixture scenario in both directions, unknown
  actions, close, security profile 1: 465 scenarios pass, 0 fail. Skipped
  scenarios (upstream defects documented with evidence, features a
  reference does not support) are never counted as passed.
- **Hardening**: a seeded chaos proxy that fragments, delays and cuts
  connections while stations of every version call and SEND (minutes of
  chaos with thousands of cuts and no wrong answer, error or leak);
  parallel tests (T6 under load, close and shutdown under load, replacement
  storms); resource exhaustion (slow handshakes, station floods, oversized
  messages, a 16 MiB compression bomb, call floods, queue saturation, slow
  readers, half-open links); TLS and authentication refusals; all with the
  race detector and a goroutine-leak check. Fuzzing of the frame decoder,
  the engine, the server handshake and every dispatcher, 10 s per target in
  CI and 10 minutes nightly.
- **API**: `TestAPI` records the exported API; `TestDocsCoverAPI` checks
  that every exported identifier is documented and every error described.
- CI: Go 1.26 and 1.27; Linux amd64 and arm64, macOS, Windows; cross-builds
  without cgo; staticcheck, golangci-lint, govulncheck; 97% coverage
  required.

## Known limitations

- **OCPP 2.1 against one independent implementation**: of the references,
  only The Mobility House supports 2.1 (ChargeTimeEU has it on master
  only).
- **SEND and CALLRESULTERROR** are verified against go-ocpp only: no
  released reference implements them.
- **Actions without a reference scenario**: 30 of 39 (1.6J), 27 of 64
  (2.0.1) and 43 of 91 (2.1) actions are exchanged with an independent
  stack; the others are verified against go-ocpp and the schemas
  (docs/compatibility).
- **Not in the library** (FEATURES.md, with status): an offline queue for
  transaction messages, switching network profiles at runtime, a Local
  Controller, typed configuration-key constants, OpenTelemetry/Prometheus
  adapters. Charging logic, persistence, OCPP 1.6 SOAP and older versions
  are out of scope.

---
