# Testing go-ocpp

How to run each tier. Conventions for tests are in [CONTRIBUTING.md](CONTRIBUTING.md); peers and results in [INTEROPERABILITY.md](INTEROPERABILITY.md).

## Tiers

| Tier | Command | Prerequisites | In CI |
|---|---|---|---|
| Unit, schema | `make test` | none | yes, Go 1.26 and 1.27, with `-race` |
| Oldest Go | `make test-min` | none | yes |
| End-to-end | `make e2e` | none | yes |
| Chaos | `make chaos` (`CHAOS_DURATION`, default 30s; `CHAOS_SEED`) | none | Hardening workflow, nightly |
| Soak | `make soak` (`SOAKTIME`, default 10m) | none | Hardening workflow, nightly |
| Fuzz | `make fuzz` (`FUZZTIME`, default 30s), `make fuzz-long` (10 min) | none | 10s per target on each pull request; long runs nightly |
| Fuzz target lists | `make fuzz-check` | none | yes: the lists in the Makefile and in `hardening.yml` against `go test -list '^Fuzz'` |
| 32-bit | `GOARCH=386 go test -count=1 ./...` | a host that runs 386 binaries (Linux amd64) | Hardening workflow, nightly (job `test-386`) |
| Benchmarks | `make bench` | none | no |
| Interop | `make interop` | Docker | Interop workflow |
| Compatibility matrices | `make compat` rewrites `docs/compatibility/` | Docker | no |
| Cross-compile | `make cross` | none | yes, `platforms` job, which also tests on Linux arm64, macOS and Windows |
| Generated code, schemas | `make gen-check`, `make schemas-check` | none | yes |
| Coverage package list | `make coverpkg-check` | none | yes: `coverpkg` in `ci.yml` against `COVER_PKGS` |

## Environment variables

| Variable | Effect |
|---|---|
| `OCPP_CHAOS_DURATION`, `OCPP_CHAOS_SEED` | Length of the chaos test; replays a logged seed |
| `OCPP_INTEROP_REQUIRE` | A missing image or Docker fails the interop suite instead of skipping it (`make interop` sets it) |
| `OCPP_INTEROP_ADAPTERS` | Runs the named stacks only |
| `OCPP_INTEROP_<STACK>_IMAGE` | Replaces the pinned image of one stack |
| `OCPP_WRITE_COMPAT` | `TestWriteCompatibility` writes the matrices (`make compat` sets it) |

## Coverage

`make coverage` measures `COVER_PKGS`: the library packages, whichever package's tests exercise them. Excluded: the examples, the interop suite, `e2e` and the code-generation and test helpers `internal/gen`, `internal/specsync`, `internal/schematest` and `internal/leakcheck` (see the `COVER_PKGS` line of the [Makefile](Makefile)). `make coverage-check` fails below `COVER_MIN` (98), the same minimum as `coverage-min` in `.github/workflows/ci.yml`, which measures the same packages: its `coverpkg` input is `COVER_PKGS`, the generated version packages and `internal/wire` included, and `make coverpkg-check` fails when the two lists differ. The total is 98.2 %. `make coverage-pkg-check` requires `COVER_PKG_MIN` (90) of each package of `COVER_PKGS`, in CI too; `ocpptest` is the lowest, at 93 %.

## Fuzz regressions

`make fuzz` runs the targets in `FUZZ_TARGETS`. A failing input is written to `testdata/fuzz/<target>/` of the package. Keep it with the fix ([CONTRIBUTING.md](CONTRIBUTING.md)): `go test` replays it from then on.

The ten targets are listed twice by hand, in `FUZZ_TARGETS` and in the matrix of `.github/workflows/hardening.yml`. `make fuzz-check` (in `make check` and in CI) lists the targets of the library packages with `go test -list '^Fuzz'` and fails when either list lacks one or names one that does not exist. Besides returning for every input, the targets assert what an accepted input means: `FuzzDecode` that the frame encodes and decodes to itself, and with the three `FuzzDispatch` targets that an accepted input is valid UTF-8 which `encoding/json` decodes without replacing text; `FuzzWellFormed` compares the text check with `encoding/json` for every valid JSON input.


## Lifecycle contract matrix

The contract tests of the long-lived types, by the rows of the organisation's
lifecycle standard (L1 to L16). The types are `client.Client`,
`server.Server` and `server.Session`, and underneath them
`internal/engine.Endpoint` (one per connection, which a client and a session
wrap) and `internal/transport.Conn`. A row applies to a type that has the
feature; where it does not, the reason is given, so that a row without a
test and a row that does not apply look different. Tests are in `e2e`
unless a package is named. Those of rows L1, L2, L8 and L10 wait for events
only, never for a fixed time.

| Row | Scenario | Applies to | Tests | Notes |
|---|---|---|---|---|
| L1 | Close twice | all five | `TestClientCloseTwiceAndConcurrently`, `TestClientCloseBeforeConnect`, `TestSessionCloseTwiceAndConcurrently`, `TestServerCloseTwiceAndConcurrently`; `internal/engine` `TestCloseTwice`; `internal/transport` `TestAcceptCloseBounded/Close_twice` | every Close returns nil; the connection is closed once, the state handler sees Closed once, the session handler is called once; `Done` is closed and `Err` is `ErrClosed` (client, session) |
| L2 | Close from several goroutines | all five | the three `…CloseTwiceAndConcurrently` tests (8 goroutines behind a barrier); `internal/engine` `TestCloseFromSeveralGoroutines`; `internal/transport` `TestAcceptCloseBounded/Close_twice` (a second Close while the first waits) | every call returns nil |
| L3 | Close during a blocked read | Client, Session, Endpoint, Conn | `TestCloseDuringCall`; `internal/engine` `TestCloseFailsPendingCall`, `TestCallRacingClose`; `internal/transport` `TestPipeContexts` | the pending CALL fails with `ErrClosed` |
| L4 | Close during a write | Client, Session, Endpoint | `TestSlowReader`, `TestCloseUnderLoad`; `internal/engine` `TestStuckWriter`, `TestWriteErrorLosesConnection` | |
| L5 | Close during the handshake | Client, Server, Conn | `TestCloseDuringConnect`, `TestHostileCloseDuringStalledConnect`, `TestSlowHandshake`; `client` `TestConnectWaitingClosed`; `internal/transport` `TestDialContextEnd` | n/a for Session and Endpoint: they exist once the handshake is over |
| L6 | Cancel a pending request | Client, Session, Endpoint | `internal/engine` `TestContextCancelKeepsSlot`, `TestUnsentCallNotWritten`, `TestWaitingForTheSlot`, `TestOnCallDoneOncePerCall` | recorded deviation: `Call` returns at once, but a CALL already written keeps the outgoing slot until its response or the response timeout (OCPP-J allows one outstanding CALL, T6), so the next request waits for that; a CALL not yet written frees the slot at once |
| L7 | Response after the request timed out | Client, Session, Endpoint | `TestHostileCallTimeouts`; `internal/engine` `TestTimeoutAndLateResponse` | dropped, counted once by `OnProtocolError`; the next CALL is not affected |
| L8 | Response after reconnect | Client | `TestReconnect`; `internal/engine` `TestResponseBelongsToItsConnection` | by construction: each connection has an Endpoint of its own with its own pending CALL; the engine test uses the same message ID on two connections. n/a for Server and Session: a session is one connection |
| L9 | A callback calls a public method | Client, Session, Endpoint | `TestHostileStateHandlerReadsClient`; `internal/engine` `TestCallFromHandler`, `TestAfterResponse` | |
| L10 | A callback calls Close | Client, Session, Endpoint | `TestClientCloseFromHandler`, `TestSessionCloseFromHandler`, `TestClientShutdownAfterResponse`; `internal/engine` `TestCloseFromHandler`, `TestCloseFromAfterResponse`, `TestDoneDoesNotWaitForAfterResponse` | Close from a handler returns without waiting for that handler; the state handler, the metrics callbacks and the frame observer must not call Close (documented, not tested) |
| L11 | Slow callback | Session, Client, Endpoint | `TestIncomingFlood`, `TestPingWhileIncomingQueueIsFull`; `internal/engine` `TestFloodWhileHandling`, `TestPingWhileReaderWaitsForRoom` | another station is served meanwhile; the slow connection's reader waits, and its pings are not counted in that time |
| L12 | Queue at capacity | Client, Session, Endpoint | `TestQueueSaturation`; `internal/engine` `TestBusy`, `TestFloodWhileHandling`, `TestOnCallDoneWithoutTheSlot` | outgoing: `ErrBusy`, reported to `OnCallDone`; incoming: the reader waits (backpressure) |
| L13 | Concurrent sessions | Server | `TestManyStations`, `TestParallelCalls`, `TestReplacementStorm`, `TestRejectNewRace` | n/a for the others: one connection each |
| L14 | Failure during construction | Client, Server, Conn | `TestHostileBrokenUpgrade`, `TestHostileAcceptThenDrop`; `internal/transport` `TestDialUnboundTransports`, `TestAcceptRefusalClosesConnection` | each with the goroutine check |
| L15 | `Shutdown(ctx)` | Client, Server, Session | `TestShutdown`, `TestClientShutdown`, `TestSessionShutdownTimeout`, `TestShutdownUnderLoad`, `TestShutdownWhileStationKeepsSending`, `TestServerShutdownAnswersCallInHandler` | `Server.Close` is `Shutdown` without waiting: it returns before the sessions have ended. n/a for Endpoint and Conn, which have no Shutdown (the roles drain an Endpoint with `Drain` and `Idle`) |
| L16 | Close racing reconnect | Client | `TestCloseDuringBackoff`, `TestCloseDuringRedial`, `TestReconnectStopsOnClose`, `TestHostileCloseInEveryState`, `TestHostileSetReconnectNilWhileRedialling` | n/a for the others: only the client reconnects |

`Done` and `Err` exist on `client.Client`, `server.Session` and the
Endpoint; `server.Server` reports its end through `Serve`'s return and the
session handler. For the client, `TestClientDoneAndErr` and
`TestClientDoneWhileReconnecting` check that `Done` stays open across a
lost connection, a failed `Connect` and a reconnect, and that `Err` is the
error of the last state change, also when the state handler reads it;
`TestClientCloseFromHandler` that `Done` is closed only once the handler
that called `Close` has returned.

## Ownership

Who owns a slice that crosses the API is stated where it crosses (the doc
comments of `ocpp.Caller`, `ocpp.Dispatcher`, `ocpp.FrameObserver`,
`ocppj.Frame` and the version packages), and tested by changing the memory
afterwards: `ocppj` `TestDecodeDoesNotAliasInput`, `TestEncodeDoesNotAliasFrame`;
`internal/engine` `TestDispatcherOwnsItsPayload` (a transport that reuses
its read buffer), `TestCallerMayReuseItsRequest`, `TestResponseIsACopy`;
`ocpp201` `TestMessagesAreNotShared`.

## Goroutines

`internal/leakcheck.Check(t)`, called first in a test, fails it for every
goroutine that started during the test and still runs after its cleanups,
with that goroutine's stack. It compares stacks, not counts, so goroutines
that end meanwhile hide nothing. `internal/engine`, `internal/transport` and
`e2e` use it (through `checkNoLeak` and `noLeak` in the first and the last);
its own tests are `TestCheckNamesTheGoroutine` and `TestCheckDoesNotCount`.

## When CI runs

| Event | Workflow | What runs |
|---|---|---|
| Pull request | `pr.yml` | `docs.yml` (docsync, otfabric-policy) always; `ci.yml`, `interop.yml` unless every changed file is documentation; then `OT Fabric Required Checks` |
| Push to a branch | none | nothing; dispatch `ci.yml`, `interop.yml` to test a branch without a pull request |
| Push to `main` | `docs.yml` | the light checks; the merged commit was tested as the pull request's merge commit |
| Schedule | `hardening.yml`, `spec-check.yml` | the scheduled runs, on `main`: long fuzzing, chaos and soak, the tests as `GOARCH=386`; the schema check |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`, and other than `API.md`, `API_CLIENT.md`, `API_SERVER.md`, `ERRORS.md`, which code reads. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
