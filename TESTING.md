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
| Fuzz | `make fuzz` (`FUZZTIME`, default 30s), `make fuzz-long` (10 min) | none | 10s per target on each push; long runs nightly |
| Benchmarks | `make bench` | none | no |
| Interop | `make interop` | Docker | Interop workflow |
| Compatibility matrices | `make compat` rewrites `docs/compatibility/` | Docker | no |
| Cross-compile | `make cross` | none | yes, `platforms` job, which also tests on Linux arm64, macOS and Windows |
| Generated code, schemas | `make gen-check`, `make schemas-check` | none | yes |

## Environment variables

| Variable | Effect |
|---|---|
| `OCPP_CHAOS_DURATION`, `OCPP_CHAOS_SEED` | Length of the chaos test; replays a logged seed |
| `OCPP_INTEROP_REQUIRE` | A missing image or Docker fails the interop suite instead of skipping it (`make interop` sets it) |
| `OCPP_INTEROP_ADAPTERS` | Runs the named stacks only |
| `OCPP_INTEROP_<STACK>_IMAGE` | Replaces the pinned image of one stack |
| `OCPP_WRITE_COMPAT` | `TestWriteCompatibility` writes the matrices (`make compat` sets it) |

## Coverage

`make coverage` measures `COVER_PKGS`: the library packages, whichever package's tests exercise them. Excluded: the examples, the interop suite, `e2e` and the code-generation and test helpers `internal/gen`, `internal/specsync` and `internal/schematest` (see the `COVER_PKGS` line of the [Makefile](Makefile)). `make coverage-check` fails below `COVER_MIN` (98), the same minimum as `coverage-min` in `.github/workflows/ci.yml`.

## Fuzz regressions

`make fuzz` runs the targets in `FUZZ_TARGETS`. A failing input is written to `testdata/fuzz/<target>/` of the package. Keep it with the fix ([CONTRIBUTING.md](CONTRIBUTING.md)): `go test` replays it from then on.
