# Agent instructions

Instructions for AI coding agents (and humans) working in this repository.

## Organisation standards

This repository follows **otfabric/engineering policy 1.0**, profile
**protocol-library** (`.otfabric/policy.yaml`, mode `baseline`). Where this
file and the organisation standards disagree, this file wins here: say so in
your report.

## Read first

1. [REQUIREMENTS.md](REQUIREMENTS.md): what must hold.
2. [ARCHITECTURE.md](ARCHITECTURE.md) and [docs/adr](docs/adr): how it is
   built, and why.
3. [SPECIFICATIONS.md](SPECIFICATIONS.md): the normative baseline and the
   open verification items.
4. [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md): the current phase and
   its gate.

## Hard rules

- Never invent protocol behaviour. If the OCA text is unavailable or
  ambiguous, leave the decision to the application, or record it as an
  implementation choice in ARCHITECTURE.md, with the reason.
- Never mark an action or feature implemented because its types compile.
  Update the coverage manifest only with what tests establish.
- Never skip or weaken a failing test, and never report an unsupported
  interop scenario as passed.
- Keep transport, protocol machinery and application policy apart. No
  charging logic goes in the library.
- No global mutable state. No new dependency without an ADR.
- Do not import another OCPP implementation, and do not depend on
  ocpp-interop beyond its published images.
- Do not commit OCA PDFs. OCA schema files go under `schemas/` verbatim and
  are never edited (ADR 0007); generated code is MIT.
- Never log credentials or authorization tokens.

## Before finishing

```bash
make check
```

At the end of a phase, report what the template in IMPLEMENTATION_PLAN.md
asks for.
