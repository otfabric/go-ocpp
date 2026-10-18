# ADR 0003: Message types and validation generated from the OCA schemas

Status: Accepted (2026-10-18). The open point on committing the schemas is settled by ADR 0007.

## Context

The three versions have 39, 64 and 91 actions and several hundred nested
types. Writing them by hand is a source of silent drift. A runtime JSON
Schema validator would need either a dependency or an in-house engine for
draft-04 and draft-06. It also reports errors in schema terms rather than
Go terms, and validates through reflection on every message.

The schemas are licensed CC BY-ND 4.0 (SPECIFICATIONS.md, Licensing).

## Decision

- `internal/gen` reads a directory of OCA schemas and writes, for each
  version package:
  - the types and enums;
  - a `Validate() error` method per type, checking required fields, enums,
    string length, numeric bounds, array sizes and formats;
  - the call functions, handler interfaces and dispatchers;
  - the action table and `coverage.json`.
- Generated files are committed and carry a header naming the schema file
  and its SHA-256. CI regenerates them and fails on drift when the schemas
  are available to it.
- Validation is generated code, so there is no reflection and no runtime
  schema engine. A test generates random valid and invalid payloads from
  the schemas and checks that `Validate` agrees with a reference JSON Schema
  validator. That validator is a test-only dependency, so it never reaches
  users of the module.
- The output is plain `encoding/json` structs that read like hand-written
  code. Optional fields are pointers only where the zero value is valid and
  must be distinguishable from absence. Otherwise they use `omitempty`.
- Where the schemas cannot express a rule the specification states, the
  rule is written by hand in a separate file and cites its section.

## Consequences

- Whether the schemas are committed is decided separately (S1). Until then
  the generator takes their location as an argument, and the drift check
  runs where the schemas are present.
- A new edition or errata becomes a regeneration plus a reviewed diff.
