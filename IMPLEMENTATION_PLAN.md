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
| 3 | OCPP 1.6J | Open |
| 4 | Initial interoperability (1.6J) | Open |
| 5 | OCPP 2.0.1 | Open |
| 6 | OCPP 2.1 | Open |
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

- [ ] Settle S2 and S3. Vendor the schemas under `schemas/` with their
      LICENSE (ADR 0007) and record their checksums
- [ ] `internal/gen` for draft-04: types, `Validate`, call functions,
      handler interfaces, dispatchers, `coverage.json`
- [ ] All 28 core and 11 security-extension actions
- [ ] Hand-written rules that the schemas cannot express, each citing its
      section
- [ ] Round-trip and invalid-payload tests per message. Cross-check of
      `Validate` against a reference validator
- [ ] Security profiles 1–3 in client and server
- [ ] Examples `station16` and `csms16`, run by `make examples`
- [ ] e2e for every 1.6 action in its own direction

Gate: every official 1.6J action is represented, validated and
dispatchable. The behaviour an application must decide is left to its
handlers.

## Phase 4 - Initial interoperability

In ocpp-interop:

- [ ] Contract 1.0 (from draft 0.1), and fixtures for 1.6
- [ ] Adapters `mobilityhouse` (Python) and `chargetime` (Java), both roles,
      1.6J
- [ ] Container images for amd64 and arm64, pinned bases, non-root,
      licences
- [ ] Smoke tests and the cross-adapter matrix; reports in the result
      schema
- [ ] Release v0.1.0, published to GHCR with digests

In go-ocpp:

- [ ] `interop/` harness (build tag), pinned digests in three places,
      `interop.yml`
- [ ] go-ocpp client against both reference servers, and both reference
      clients against the go-ocpp server
- [ ] INTEROPERABILITY.md and COMPATIBILITY.md with real results
- [ ] Interop badge in README.md, added once `interop.yml` exists

Gate: 1.6J interoperates in both roles with two independent
implementations.

## Phase 5 - OCPP 2.0.1

- [ ] S4 for T8 and T9. Generator for draft-06
- [ ] All 64 actions. Device model types (component, variable, attributes)
      without reducing them to strings
- [ ] TransactionEvent semantics kept as in 2.0.1, with no mapping to 1.6
- [ ] permessage-deflate on the server; decompressed-size limit
- [ ] Certificate and security messages; security events
- [ ] Examples `station201` and `csms201`
- [ ] ocpp-interop: `lorenzodonini` adapter (Go, ocpp-go). Its documented
      2.0.1 limitations are respected and recorded as capabilities
- [ ] Interop matrix extended to 2.0.1 in both roles

Gate: 2.0.1 message coverage is complete, and interop passes in both
directions for the features the adapters support.

## Phase 6 - OCPP 2.1

- [ ] S5: diff the schemas against Edition 2 and the 2026-09 errata
- [ ] CALLRESULTERROR and SEND in `ocppj` and the engine (T4, T5)
- [ ] All 91 actions; periodic event streams, DER, tariffs, battery swap,
      bidirectional charging
- [ ] Interop against the 2.1-capable adapters (ChargeTimeEU and, after
      verification, Mobility House 2.1.0). A feature with only one
      independent implementation is reported as such
- [ ] Examples `station21` and `csms21`

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
