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

### Known limitations

- No protocol functionality yet.
