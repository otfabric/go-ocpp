# Requirements

What go-ocpp must be, stated so that each requirement can be checked. The
design that meets them is in [ARCHITECTURE.md](ARCHITECTURE.md); the order in
which they are met is in [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

## Purpose

go-ocpp is a pure Go implementation of the Open Charge Point Protocol, for
both roles: the Charging Station (OCPP 1.6: Charge Point) and the CSMS
(OCPP 1.6: Central System). It is a protocol library. It transports,
validates and dispatches OCPP messages. Decisions such as whether to
authorize a token, start charging or install firmware belong to the
application, which makes them in its handlers.

It is meant for charging management systems and also for energy management
systems, building and industrial controllers, and station simulators.
Billing, persistence, fleet management and charging optimisation are not part
of it.

## R1 - Scope

| Version | Priority | Scope |
|---|---|---|
| OCPP 1.6J, with the security extensions of the 1.6 Security Whitepaper | P0 | Every action, both roles |
| OCPP 2.0.1 | P0 | Every action, both roles |
| OCPP 2.1 | P1 | Every action, both roles, including the message types CALLRESULTERROR and SEND |
| OCPP 1.6 SOAP, OCPP 1.5 and older | Excluded | — |

The baseline editions and errata are listed in
[SPECIFICATIONS.md](SPECIFICATIONS.md).

## R2 - Engineering constraints

1. Pure Go: builds with `CGO_ENABLED=0` on Linux, macOS and Windows, for
   amd64 and arm64.
2. No global mutable state. Everything is configured per client or server.
3. Every blocking call takes a `context.Context` and returns when it is
   cancelled.
4. Safe for concurrent use, and documented as such per type.
5. No database, message broker, metrics backend or tracing SDK is required.
6. Dependencies outside the standard library need a justification in an ADR.
   The production library must not import another OCPP implementation.
7. The minimum Go version is the oldest Go release still supported by the Go
   team when a version of go-ocpp is released.

## R3 - One engine, two roles

1. The Charging Station and CSMS roles share one RPC engine that handles
   framing, correlation, timeouts, dispatch and session lifecycle.
2. Both roles can send requests and handle requests. The CSMS is not a
   passive receiver.
3. The engine contains no charging logic.
4. A server session keeps the version negotiated for that connection.
   Stations using different versions can be connected to one server at the
   same time.

## R4 - RPC framing

1. The OCPP-J codec can be used and tested without a network connection.
2. It implements CALL, CALLRESULT and CALLERROR for every version, and
   CALLRESULTERROR and SEND for 2.1 only. A type 5 or 6 frame on a 1.6 or
   2.0.1 connection is a protocol error.
3. Malformed input never causes a panic. Every decoder entry point has a fuzz
   target with a committed regression corpus.
4. Frames larger than a configured limit are rejected before they are parsed.
5. Late responses (after a timeout), duplicate responses and responses with
   unknown IDs are dropped and reported through logging and metrics. They are
   never delivered to a caller they were not meant for.

## R5 - Request scheduling

1. Outstanding CALLs per direction are limited as the specification requires
   (SPECIFICATIONS.md T6). Callers waiting to send are held in a bounded
   queue. A full queue fails immediately with `ErrBusy`.
2. Every outgoing CALL has a timeout. The default is documented, and a
   context deadline overrides it.
3. Non-idempotent requests are never replayed automatically, including after
   a reconnect.

## R6 - Message coverage

1. Message types are generated from the OCA JSON schemas. Required and
   optional fields, enums, numeric and string constraints, arrays, nested
   objects and CustomData are preserved.
2. Each version has a machine-readable coverage manifest with one entry per
   official action. Each entry records the direction, the request and
   response type, whether the action is schema-validated and dispatchable,
   and its round-trip and interop test status.
3. A test fails if an action in the schema set is missing from the manifest,
   or if the manifest claims a status that no test supports.
4. Validation is strict by default, in both directions. Any relaxation is
   explicit, scoped to named actions or fields, and documented.

## R7 - Charging Station role (client)

1. Connects over WebSocket with subprotocol negotiation, a station identity,
   TLS, HTTP Basic authentication and client certificates.
2. Reconnects automatically with exponential backoff and jitter between
   configurable bounds. Reconnecting stops when the client is closed.
3. Reports connection state changes to the application, so that it can
   implement offline queues and send BootNotification again after a
   reconnect. The library does not persist anything.

## R8 - CSMS role (server)

1. The server is an `http.Handler`, so it can be mounted in an existing
   `http.Server` or mux. A convenience listener is provided as well.
2. Authentication runs before the WebSocket upgrade. The identity in the URL
   is checked against the credentials or certificate presented.
3. An active session can be looked up by station identity, and requests can
   be sent to it.
4. When a station connects with an identity that already has a session, a
   configurable policy decides what happens. The default is documented and
   never leaves two sessions controlling one station.
5. The number of stations, the message size and the handshake time are
   limited, with documented defaults.

## R9 - Security

1. Security profiles 1, 2 and 3 are supported where the version defines
   them.
2. Plain `ws://` and Basic authentication without TLS (profile 1) require an
   option whose name says it is insecure. No example uses them except one
   that is labelled as insecure.
3. TLS certificate verification cannot be disabled by any option of this
   library. An application that wants that can supply its own `tls.Config`.
4. Credentials and authorization tokens never appear in logs, errors or
   metric labels produced by the library.

## R10 - Observability

1. Logging uses `log/slog`. It is silent unless a logger is supplied.
2. Metrics are reported through a callback interface. No metrics library is
   imported.
3. Request interceptors on both directions give OpenTelemetry or similar
   tracing a place to hook in, without a dependency on it.
4. No label carries the station identity unless the application chooses to
   add it.

## R11 - Testing

1. Unit tests for the codec, the engine, the client and the server, run with
   `-race`.
2. Round-trip tests (decode, encode, compare) for every generated message,
   with valid and invalid payloads.
3. End-to-end tests run this module's client against this module's server
   for every version, with requests in both directions.
4. Interop tests run this module against the reference implementations
   published by `otfabric/ocpp-interop`, in both roles. They consume
   published images pinned by digest and nothing else from that repository.
5. Goroutine leaks are checked in the engine, client and server tests.

## R12 - Claims

1. Documentation separates three levels of assurance: schema conformant,
   interoperability tested, and OCA certified. go-ocpp does not claim
   certification.
2. An unsupported or untested feature is reported as such and never as
   passed.
3. Comparisons with other implementations state only what their
   documentation or our tests establish.

## R13 - Future CLI

The public API must be sufficient for `otfabric/ocppctl` without a second
implementation:

- sending an arbitrary action with a raw JSON payload;
- validating a payload for a version and action;
- listing the actions of a version;
- encoding and decoding frames;
- observing every frame sent and received.
