# go-ocpp Releases

No release yet. Releases are cut with the `Release` workflow (semantic
versioning, annotated tag, GitHub release).

## Unreleased

### Summary

Phase 0 (research and architecture) and Phase 1 (repository foundations).

### Added

- ARCHITECTURE.md, REQUIREMENTS.md, SPECIFICATIONS.md,
  IMPLEMENTATION_PLAN.md and ADRs 0001–0006
- Package `ocpp`: `Version`, with its subprotocols and parsers; sentinel
  errors
- Makefile, golangci-lint configuration, CI (reusable go-ci; format,
  dependency, staticcheck and govulncheck checks; build and test on Linux
  amd64 and arm64, macOS and Windows), release workflow

Phase 2 (shared OCPP-J engine):

- Package `ocppj`: OCPP-J frame codec for every version, with per-version
  message types and error codes, size limits, and a fuzz target
- Packages `client` and `server`: the Charging Station and CSMS roles over
  WebSocket (github.com/coder/websocket, ADR 0002), with subprotocol
  negotiation, HTTP Basic authentication, TLS and mutual TLS, reconnection
  with backoff and jitter, session lookup and replacement, limits and
  graceful shutdown
- One RPC engine for both roles: one outstanding CALL per direction, a
  bounded queue, response timeouts, late-response handling, sequential
  dispatch, and handlers that may call the peer
- Root package: `Peer`, `Caller`, `Dispatcher`, `CallError` and error codes,
  `Metrics`, interceptors, frame observer, `State`
- End-to-end tests over real WebSockets for every version, and an
  import-rule test

Phase 4 (go-ocpp side):

- Interop suite (`interop/`, build tag `interop`, `make interop`, workflow
  `interop.yml` on amd64 and arm64) against otfabric/ocpp-interop v0.0.1,
  pinned by digest: OCPP 1.6J in both roles against The Mobility House ocpp
  2.1.0 and ChargeTimeEU Java-OCA-OCPP v2.0, with every fixture sample in
  both directions, security profile 1, unknown actions and close

Phases 3 and 5 (OCPP 1.6J and 2.0.1):

- Packages `ocpp16` (39 actions: core and Security Whitepaper Ed. 4) and
  `ocpp201` (64 actions), generated from the OCA JSON schemas by
  `internal/gen`: types, strict decoding and validation, call functions with
  version and direction checks, handler interfaces and dispatchers for both
  roles, `Actions`, `ValidateRequest`, `ValidateResponse`, `coverage.json`
- `schemas/`: the OCA schemas, unmodified (CC BY-ND 4.0), with SHA-256 sums
- `ocpp.ValidationError`, `ocpp.ErrInvalidMessage`, `ocpp.Ptr`
- Conformance suite against the schemas and an independent JSON Schema
  validator; dispatcher fuzz targets
- WebSocket compression (RFC 7692): always supported by the server,
  `client.WithCompression`
- Examples `ocpp16` and `ocpp201`
- Incoming payloads with a `null` property are refused
  (TypeConstraintViolation): no OCPP schema allows null
- Interop: per OCPP version, typed validation of every reference payload,
  and the third reference (lorenzodonini/ocpp-go) when its image is given
- `internal/specsync` and the weekly `spec-check` workflow: the vendored
  OCA documents are compared with the newest published editions, errata and
  appendices, and a `spec-update` issue is opened when they differ

Changed after checking the OCA text (SPECIFICATIONS.md, T1–T11):

- A station offering no supported subprotocol gets a completed handshake
  and an immediate close, not HTTP 400
- `server.ErrUnknownStation` (HTTP 404); `server.BasicAuth` returns it for
  an unknown identity
- On 2.x, a corrupt frame whose message ID cannot be read is answered with
  a CALLERROR with ID "-1"
- `client.Reconnect` is now `{WaitMinimum, RandomRange, RepeatTimes}`, the
  back-off of 2.x Part 4 §5.4, with `client.DefaultReconnect`
- `client.Client.Role` and `server.Session.Role`

### Known limitations

- CALLRESULTERROR and SEND (OCPP 2.1) are dropped (Phase 6).
- OCPP 2.1 messages (Phase 6).
