# Contributing

## Getting Started

1. Fork the repository and clone your fork.
2. Create a branch: `git checkout -b feature/my-change`.

## Prerequisites

- Go 1.26 or later. Do not use language or library features newer than the
  `go` line in go.mod.
- staticcheck, golangci-lint v2, govulncheck
- Docker, for `make interop` (from Phase 4)

No charging hardware is needed.

## Build & Test

```bash
make check     # everything CI runs, except interop
make test      # tests with the race detector
make fuzz      # fuzz targets (FUZZTIME=30s each)
make interop   # against the otfabric/ocpp-interop images (from Phase 4)
```

## Code Style

- Every Go file starts with `// SPDX-License-Identifier: MIT`.
- Standard library only, except the dependencies ADRs allow (`make deps`).
- Follow the import rules in
  [ARCHITECTURE.md](ARCHITECTURE.md#packages-and-import-rules).
- Code that implements a normative rule cites it, e.g.
  `// OCPP 2.0.1 Part 4 §4.1.3`.

## Formatting

CI formats with the gofmt of the Go version in go.mod. Files must be stable
under that gofmt and under the local one: run `make fmt`, then
`make fmt-check`.

## Tests

- Test vectors come from the specification or from captures, never from
  another implementation's output alone.
- Run tests with `-race`. Do not depend on timing: use contexts and
  channels, not sleeps.
- A test that fails must not be skipped to make CI pass.
- Hardening: `make fuzz` (FUZZTIME, default 30 s per target), `make
  fuzz-long` (10 min), `make chaos` (CHAOS_DURATION, CHAOS_SEED to replay a
  logged seed) and `make soak` (SOAKTIME). A fuzz input that fails is
  written to the package's `testdata/fuzz`: commit it with the fix.
- An intended change of the exported API: `go test -run TestAPI -update`,
  and note it in RELEASE.md.
- Coverage is held from the start: Codecov requires 97% of the library and
  95% of every change (`codecov.yml`). `make coverage` measures what CI
  measures. A branch no test can reach is a reason to simplify the code,
  not to lower the target; the few that only a race can reach are named in
  the pull request.

## Generated code and specifications

`ocpp16/zz_generated_*.go` and `ocpp201/zz_generated_*.go` come from
`internal/gen` and the OCA schemas in `schemas/`; never edit them, change the
generator and run `make gen`. CI fails when they are out of date
(`make gen-check`) or when a schema differs from its recorded checksum
(`make schemas-check`). New OCA documents are picked up with
`make schemas-sync` (see [schemas/README.md](schemas/README.md#staying-current)).

## Documentation

Update the document that owns the topic, and add an entry to RELEASE.md
under "Unreleased". Record decisions that are hard to reverse as an ADR in
[docs/adr](docs/adr).

## Submitting

Run `make check`, then open a pull request against `main`.

## License

Contributions are accepted under the MIT licence of this repository.
