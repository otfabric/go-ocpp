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

### Known limitations

- No typed messages or schema validation yet (Phase 3). Requests and
  responses are raw JSON or any Go value.
- CALLRESULTERROR and SEND (OCPP 2.1) are dropped (Phase 6).
- No WebSocket compression (Phase 5).
- The transport rules have not yet been checked against the OCA text
  (SPECIFICATIONS.md S4).
