# ADR 0007: Licensing of generated code and vendored schemas

Status: Accepted (2026-10-18). Settles open item S1 of SPECIFICATIONS.md and
the open point in the consequences of ADR 0003.

## Context

The OCA publishes the OCPP specifications and JSON schemas under CC BY-ND
4.0. ADR 0003 generates go-ocpp's message types and validation from those
schemas. It left two questions open: the licence of the generated code, and
whether to commit the schemas themselves.

## Decision

- The maintainers consider code generated from the OCA schemas, or written
  from them, to be no licensing issue. That code is part of go-ocpp under
  MIT, like the rest of the module. Generated files name their source schema
  and its SHA-256 in a header.
- The schema files are committed verbatim under `schemas/ocpp16/`,
  `schemas/ocpp201/` and `schemas/ocpp21/`. CC BY-ND 4.0 permits
  redistributing unmodified material with attribution. `schemas/LICENSE`
  names the OCA, the editions and the licence, and states that the MIT
  licence of the repository does not cover these files. They are never
  edited. An edition or erratum replaces them whole.
- The generator reads `schemas/` by default, and CI regenerates and fails on
  drift on every run.
- OCA PDFs are still not committed.

## Consequences

- Builds and the drift check are reproducible from the repository alone.
- README.md and SPECIFICATIONS.md keep stating that the schemas are © OCA,
  CC BY-ND 4.0.
