# ADR 0006: Minimum Go version and CI

Status: Accepted (2026-10-18)

## Context

On 2026-10-18 the supported Go releases are 1.27 (1.27.2) and 1.26 (1.26.9).
go-iec104 and go-openadr declare `go 1.23`, which predates this project.
go-opcua declares `go 1.26.0` and tests 1.26 and 1.27. The brief asks for a
supported, not unnecessarily old, minimum.

The organisation's reusable workflows (`otfabric/.github`, `@v2`) provide
lint, coverage and a test matrix. They do not provide govulncheck or an
operating-system matrix.

## Decision

- `go 1.26.0` in `go.mod`. The CI matrix covers 1.26 and 1.27.
- `ci.yml` calls `otfabric/.github/.github/workflows/go-ci.yml@v2` and adds
  three jobs of its own:
  - a build matrix for Linux, macOS and Windows (amd64 and arm64, with
    `CGO_ENABLED=0`);
  - `govulncheck`;
  - a format check with the gofmt of the `go.mod` version.
- Releases use `go-package-release.yml@v2`, as the sibling libraries do.
- Actions are pinned to their current major versions: `actions/checkout@v7`,
  `actions/setup-go@v7`, `actions/upload-artifact@v7`.

## Consequences

- Raise the minimum when Go 1.26 leaves support (with Go 1.28's release).
