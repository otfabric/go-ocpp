# Implementation plan

The phases, their gates and the checklist across the three repositories:
`otfabric/go-ocpp` (this one), `otfabric/ocpp-interop` and `otfabric/ocppctl`.
A phase is done when its gate is met. Writing the code is not enough.

Order: go-ocpp foundations → OCPP 1.6J → two interop adapters → OCPP 2.0.1
→ third adapter → OCPP 2.1 → hardening → ocppctl.

| Phase | Subject | Status |
|---|---|---|
| 0 | Research and architecture | **Done** (2026-10-18) |
| 1 | Repository foundations | **Done** (2026-10-18). CI has not run on GitHub yet |
| 2 | Shared OCPP-J engine | **Done**, except the check against the OCA text (S4) |
| 3 | OCPP 1.6J | **Done** |
| 4 | Initial interoperability (1.6J) | **Done** (ocpp-interop v0.0.1) |
| 5 | OCPP 2.0.1 | **Done** (ocpp-interop v0.1.0) |
| 6 | OCPP 2.1 | In progress |
| 7 | Production hardening | Open |
| 8 | ocppctl | Open |

## Phase 0 - Research and architecture

Delivered:

- go-ocpp: [ARCHITECTURE.md](ARCHITECTURE.md),
  [REQUIREMENTS.md](REQUIREMENTS.md), [SPECIFICATIONS.md](SPECIFICATIONS.md),
  this plan, and [ADRs 0001–0006](docs/adr/README.md).
- ocpp-interop: REQUIREMENTS.md, PLAN.md and
  docs/CONTAINER_CONTRACT.md (draft, version 0.1).

Gate: the architecture and the external interfaces are documented. These
are the public API sketch, the container contract, and the capabilities and
result documents. **Met.**

## Phase 1 - Repository foundations

- [x] go.mod (`go 1.26.0`, no requirements), MIT licence, SPDX headers
- [x] Makefile in the style of the sibling libraries
- [x] CI: reusable go-ci, OS/arch build matrix, govulncheck, gofmt check
- [x] Release workflow (go-package-release)
- [x] golangci-lint configuration, codecov.yml, .gitignore
- [x] Root package with `Version` and the sentinel errors, with tests
- [x] Documentation skeletons (README, API, ERRORS, OBSERVABILITY,
      INTEROPERABILITY, COMPATIBILITY, SECURITY, RELEASE, CONTRIBUTING,
      AGENTS)
- [x] ocpp-interop: layout, Makefile, versions.yaml with planned adapters,
      JSON schemas (capabilities, client result, report, fixture), baseline
      fixtures for 1.6 and 2.0.1, validation scripts, CI
- [ ] CI green on GitHub. This needs a push, which has not been done.

Gate: both repositories build cleanly and pass their initial CI.

## Phase 2 - Shared OCPP-J engine

- [ ] Confirm T1–T3, T6, T7 and T9 against the OCA text (S4). Not done:
      the OCA documents were not available. Must happen before Phase 3 is
      merged
- [x] `ocppj`: frames, per-version error codes, limits, fuzz target
      (`FuzzDecode`) with seeds
- [x] `internal/transport`: coder/websocket adapter and in-memory
      transport (ADR 0002)
- [x] `internal/engine`: correlation, a single outgoing slot with a bounded
      queue, timeouts, late and duplicate responses, sequential dispatch,
      lifecycle, goroutine-leak tests (ADR 0004)
- [x] Root contracts: `Peer`, `Caller`, `Dispatcher`, `CallError`,
      `Metrics`, interceptors, `State`
- [x] `client`: dial, subprotocol offer, Basic auth, TLS, reconnect with
      backoff and jitter, state handler
- [x] `server`: `http.Handler`, authenticator, version selection, session
      registry, replacement policy, limits, `Shutdown`
- [x] A test of the import rules in ARCHITECTURE.md
- [x] e2e with raw dispatchers on every version: requests in both
      directions, a handler calling back, 40 concurrent stations, close
      during a call, Basic auth, TLS profiles 2 and 3, replacement,
      reconnect, limits, observability, no credentials in logs

Gate: a Go client and a Go server exchange requests in both directions over
WebSockets, under `-race`, with no leaked goroutines.

## Phase 3 - OCPP 1.6J

- [x] Settle S2–S5 against the OCA archives; T1–T11 checked against the
      text (three Phase 2 behaviours corrected). Schemas vendored under
      `schemas/` with their LICENSE (ADR 0007) and SHA-256 sums
- [x] `internal/gen` for draft-04 and draft-06: types, strict decoding,
      `Validate`, call functions, handler interfaces, dispatchers,
      `coverage.json`
- [x] All 28 core and 11 security-extension actions
- [ ] Hand-written rules that the schemas cannot express. None so far: the
      rules found in the 1.6 text concern behaviour (what to answer, when to
      send), which belongs to the application's handlers
- [x] Round-trip and invalid-payload tests per message, cross-checked against
      a reference validator (`internal/schematest`)
- [x] Security profiles 1–3 in client and server (Phase 2)
- [x] Example `examples/ocpp16` (both roles in one process, TLS + Basic),
      run by `make examples`
- [x] Every action through its call function and both dispatchers (package
      tests); typed e2e over WebSocket in both directions

Gate: every official 1.6J action is represented, validated and
dispatchable. The behaviour an application must decide is left to its
handlers.

## Phase 4 - Initial interoperability

In ocpp-interop:

- [ ] Contract 1.0 (draft 0.2 so far; ChargeTimeEU cannot implement all of
      it), fixtures for 1.6 done
- [x] Adapters `mobilityhouse` (Python) and `chargetime` (Java), both roles,
      1.6J
- [x] Container images for amd64 and arm64, pinned bases, non-root,
      licences
- [x] Smoke tests and the cross-adapter matrix; reports in the result
      schema
- [x] Released as v0.0.1, published to GHCR with digests

In go-ocpp:

- [x] `interop/` harness (build tag), pinned digests in three places
      (ocpp-interop v0.0.1), `interop.yml` on amd64 and arm64
- [x] go-ocpp client against both reference servers, and both reference
      clients against the go-ocpp server (1.6J, raw payloads, profiles 0
      and 1)
- [x] INTEROPERABILITY.md and COMPATIBILITY.md with real results
- [x] Interop badge in README.md

Gate: 1.6J interoperates in both roles with two independent
implementations.

## Phase 5 - OCPP 2.0.1

- [x] T8 and T9 confirmed; generator for draft-06
- [x] All 64 actions. Device model types (`ComponentType`, `VariableType`,
      `VariableAttributeType`, `VariableCharacteristicsType`, …) as the
      schemas define them, not reduced to strings
- [x] TransactionEvent as defined by 2.0.1, with no mapping to 1.6
- [x] permessage-deflate on the server (always), optional on the client;
      size limit after decompression
- [x] Certificate and security messages, security events (as messages; key
      and certificate storage stay with the application)
- [x] Example `examples/ocpp201` (compression, device model, TransactionEvent)
- [x] ocpp-interop: `lorenzodonini` adapter (Go, ocpp-go) and 2.0.1 in all
      adapters (its Phase 5), released as ocpp-interop v0.1.0
- [x] Interop matrix extended to 2.0.1 in both roles, with the third
      reference, pinned to ocpp-interop v0.1.0 (INTEROPERABILITY.md)

Gate: 2.0.1 message coverage is complete, and interop passes in both
directions for the features the adapters support.

## Phase 6 - OCPP 2.1

- [x] S5: the schemas are the Edition 2 archive's; the 2026-09 errata change
      none (SPECIFICATIONS.md)
- [x] CALLRESULTERROR and SEND in `ocppj` and the engine (T4, T5);
      `ocpp.Sender`, `ocpp.SendDispatcher`, `Client.Send`, `Session.Send`
- [x] All 91 actions generated (`ocpp21`), with the 27 new directions taken
      from Part 2; NotifyPeriodicEventStream as a SEND-only action
- [x] e2e: typed 2.1 in both roles, a SEND while a CALL is outstanding, a
      CALLRESULTERROR for an invalid response
- [x] Example `examples/ocpp21` (version negotiation, periodic event stream)
- [ ] Interop against the 2.1-capable adapters (ocpp-interop v0.2.0:
      Mobility House 2.1.0 and ChargeTimeEU). A feature with only one
      independent implementation is reported as such

Gate: 2.1 coverage is documented, and every claimed feature has been
verified against an independent implementation.

## Phase 7 - Production hardening

- [ ] Long fuzzing runs; corpus committed
- [ ] Resource exhaustion: many stations, slow readers, oversized and
      compressed frames, handshake floods
- [ ] Reconnect storms and long-running soak tests
- [ ] TLS and authentication negative tests
- [ ] API review against R13 (ocppctl needs); documentation review;
      dependency audit
- [x] Settle S1 (licensing of generated code): ADR 0007
- [ ] v0.1.0 of go-ocpp

## Phase 8 - ocppctl

Starts after the go-ocpp API is stable enough to be published. It builds on
the published module, follows the OTFabric CLI conventions (see iec104ctl,
modbusctl), offers human-readable and JSON output with stable exit codes, and
contains no protocol code of its own.

## Phase report template

At the end of each phase, the report covers:

- Completed functionality
- Tests executed
- Interoperability results
- Remaining gaps
- Architectural decisions
- Known issues
- Recommended next phase
