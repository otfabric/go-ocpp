# ADR 0001: Package layout and version package names

Status: Accepted (2026-10-18)

## Context

The implementation brief proposes `v16/`, `v201/` and `v21/`, each with
`messages/` and `types/` subpackages. Go reserves import paths ending in
`/vN` for module major versions: `github.com/otfabric/go-ocpp/v16` is also
the path of a hypothetical major version 16 of this module, so `go get`
first looks for that module. When this module reaches v2, the path becomes
`github.com/otfabric/go-ocpp/v2/v16`, which is hard to read.

Every OCPP message refers to the shared types of its version, so a
`messages` package always imports `types`. Splitting them gives callers two
imports and no benefit.

## Decision

- Version packages are `ocpp16`, `ocpp201` and `ocpp21`, one package each.
  Generated code and hand-written code live side by side, in files named
  `zz_generated_*.go` and ordinary names respectively.
- `client` and `server` are version-neutral and never import a version
  package. Versions are plugged in through `ocpp.Dispatcher`.
- `ocppj` is public, because the CLI and test tools need the frame codec.
- `ocpp21` has its own types even where they equal those of `ocpp201`.
  Type aliases across versions would tie the two packages' APIs and
  generated code together, and 2.1 changes many shared types in small ways.
  Sharing happens in the generator and the engine, not in the public types.

## Consequences

- Imports read `ocpp16.BootNotificationRequest`, which names the version at
  each use.
- Code that serves 2.0.1 and 2.1 stations with the same logic needs
  adapter code in the application, or a later helper package. That is the
  cost of keeping each version's API exact.
