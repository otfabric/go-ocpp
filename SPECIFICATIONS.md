# Specifications

This file records which Open Charge Alliance (OCA) documents go-ocpp is built
against, where they come from, and what is still unverified. It is the
normative baseline: when the code and this file disagree, one of them is a
bug.

Last reviewed: 2026-10-18.

## Contents

- [Baseline](#baseline)
- [Source rules](#source-rules)
- [JSON schemas](#json-schemas)
- [Licensing](#licensing)
- [Transport facts the engine depends on](#transport-facts-the-engine-depends-on)
- [Open verification items](#open-verification-items)

## Baseline

All documents are published by the OCA at
<https://openchargealliance.org/my-oca/ocpp/>; superseded editions are in the
archive at <https://openchargealliance.org/protocols/archive/>.

| Version | Documents | Errata | Status in go-ocpp |
|---|---|---|---|
| OCPP 1.6 | OCPP 1.6 specification and OCPP-J 1.6 (JSON over WebSocket), "OCPP 1.6 (all files & errata)" bundle | The errata sheets inside that bundle (OCA added sheets on 2025-04-30) | P0 |
| OCPP 1.6 security | Improved security for OCPP 1.6-J, Security Whitepaper **Edition 4** (2026-02-05) | — | P0 |
| OCPP 2.0.1 | OCPP 2.0.1 **Edition 4**, Parts 0–6, Part 2 appendices v1.5 | **Edition 4 Errata 2026-09** | P0 |
| OCPP 2.1 | OCPP 2.1 **Edition 2**, Parts 0–6, Part 2 appendices v2.1 | **Edition 2 Errata 2026-09** | P1 |

Excluded: OCPP 1.6 SOAP, OCPP 1.5 and older.

OCPP 2.0.1 Edition 3 is also published as IEC 63584. go-ocpp follows the OCA
edition, which is newer.

When a document is downloaded, record its file name and SHA-256 in the table
under [JSON schemas](#json-schemas), so that a build can show what it was
generated from.

## Source rules

1. OCA specifications take precedence over any third-party implementation,
   including the reference implementations in `otfabric/ocpp-interop`.
2. Errata take precedence over the text they correct.
3. Interoperability with another implementation does not establish
   conformance; see [INTEROPERABILITY.md](INTEROPERABILITY.md) for the three
   levels of assurance.
4. Behaviour the specification does not define is not invented. It is either
   left to the application (a handler, an option) or documented as an
   implementation choice in [ARCHITECTURE.md](ARCHITECTURE.md) with the
   reason.
5. Code that implements a normative rule cites it in a comment, e.g.
   `// OCPP 2.0.1 Part 4 §4.1.3`. Important state transitions are listed with
   their source in ARCHITECTURE.md.
6. Requirements are classified as transport (WebSocket, TLS, HTTP),
   protocol (RPC framing, message semantics) or application policy
   (authorization decisions, charging behaviour). Only the first two are
   implemented by the library.

## JSON schemas

go-ocpp generates its message types and validators from the OCA JSON schemas
(see [ADR 0003](docs/adr/0003-schema-generation.md)).

| Version | Schema draft | Actions | Schema files | Source archive | SHA-256 |
|---|---|---|---|---|---|
| 1.6J core | draft-04 | 28 | 56 | OCPP 1.6 bundle | *to record* |
| 1.6J security extensions | draft-04 | 11 | 22 | Security Whitepaper Ed. 4 bundle | *to record* |
| 2.0.1 | draft-06 | 64 | 128 | 2.0.1 Edition 4 bundle | *to record* |
| 2.1 | draft-06 | 91 | 181 | 2.1 Edition 2 bundle | *to record* |

The counts come from the schema sets as distributed in 2026 and must be
re-checked against the downloaded archives:

- 1.6J security extensions: SignCertificate, CertificateSigned,
  InstallCertificate, GetInstalledCertificateIds, DeleteCertificate, GetLog,
  LogStatusNotification, SecurityEventNotification, SignedUpdateFirmware,
  SignedFirmwareStatusNotification, ExtendedTriggerMessage.
- 2.1 keeps the 64 actions of 2.0.1 and adds 27: AFRRSignal,
  AdjustPeriodicEventStream, BatterySwap, ChangeTransactionTariff,
  ClearDERControl, ClearTariffs, ClosePeriodicEventStream,
  GetCertificateChainStatus, GetDERControl, GetPeriodicEventStream,
  GetTariffs, NotifyAllowedEnergyTransfer, NotifyDERAlarm, NotifyDERStartStop,
  NotifyPeriodicEventStream, NotifyPriorityCharging, NotifySettlement,
  NotifyWebPaymentStarted, OpenPeriodicEventStream, PullDynamicScheduleUpdate,
  ReportDERControl, RequestBatterySwap, SetDERControl, SetDefaultTariff,
  UpdateDynamicSchedule, UsePriorityCharging, VatNumberValidation.
- NotifyPeriodicEventStream is sent as a SEND (message type 6) and has a
  request schema only, which is why 2.1 has 181 files rather than 182.

The authoritative list of actions per version is the coverage manifest
(`ocpp16/coverage.json`, `ocpp201/coverage.json`, `ocpp21/coverage.json`),
which the generator writes and a test checks against the schema directory.
See [REQUIREMENTS.md](REQUIREMENTS.md) R6.

## Licensing

The go-ocpp source code is MIT licensed (see [LICENSE](LICENSE)). The OCA
documents and schemas are not.

- The OCA publishes OCPP under the **Creative Commons
  Attribution-NoDerivatives 4.0 International Public License**
  (CC BY-ND 4.0). The OCPP 2.1 schemas say so in their `comment` field
  (`"OCPP 2.1 Edition 1 (c) OCA, Creative Commons
  Attribution-NoDerivatives 4.0 International Public License"`). The
  OCPP-J 1.6 document carries the same statement. The 2.0.1 schemas carry
  only `"comment": "OCPP 2.0.1 FINAL"`, and the 1.6 schemas carry no comment.
- CC BY-ND 4.0 permits redistributing the material **unmodified** with
  attribution, and forbids sharing adapted material.

What this repository does, as decided by the maintainers on 2026-10-18
([ADR 0007](docs/adr/0007-schema-licensing.md)):

- Code generated from the OCA schemas, or otherwise written from them, is
  part of go-ocpp and is MIT licensed. This includes types, validation, call
  functions, handler interfaces and coverage manifests. Each generated file
  carries a header naming the schema file and its SHA-256, for attribution
  and traceability. The same practice is followed by mobilityhouse/ocpp,
  ChargeTimeEU/Java-OCA-OCPP and lorenzodonini/ocpp-go, all MIT licensed.
- The OCA schema files are committed **verbatim** under
  `schemas/<version>/`, as CC BY-ND 4.0 permits. They are kept with a
  `LICENSE` file naming the OCA as the source and the licence, and they are
  outside the MIT grant. They are never edited. A new edition or erratum
  replaces the files whole.
- OCA PDFs are not committed. Documentation describes behaviour in its own
  words, cites sections, and does not reproduce specification text.

## Transport facts the engine depends on

These are the rules the shared engine is designed around. Each is marked with
how it has been established so far. "Secondary" means it is taken from
another implementation's documentation and still has to be checked against
the OCA text before the code that depends on it is merged.

| # | Rule | Versions | Source | Verified |
|---|---|---|---|---|
| T1 | WebSocket subprotocols `ocpp1.6`, `ocpp2.0.1`, `ocpp2.1`, negotiated with `Sec-WebSocket-Protocol`. The station offers in order of preference, the CSMS selects one | all | OCPP-J 1.6 §3; 2.x Part 4 §3 | Secondary |
| T2 | The station identity is the last path segment of the connection URL | all | OCPP-J 1.6 §3.1; 2.x Part 4 §3.1 | Secondary |
| T3 | Frames: CALL `[2, id, action, payload]`, CALLRESULT `[3, id, payload]`, CALLERROR `[4, id, code, description, details]` | all | OCPP-J 1.6 §4; 2.x Part 4 §4 | Secondary |
| T4 | CALLRESULTERROR `[5, id, code, description, details]`: the receiver of a CALLRESULT reports that it cannot process it | 2.1 only | 2.1 Part 4 §4 | Secondary |
| T5 | SEND `[6, id, action, payload]`: one way, no response of any kind, exempt from T6 | 2.1 only | 2.1 Part 4 §4.2.4 | Secondary |
| T6 | At most one outstanding CALL per direction; the next CALL waits for the response or a timeout | all | OCPP-J 1.6 §4.1.1; 2.x Part 4 §4.1.1 | Secondary |
| T7 | Message IDs are strings of at most 36 characters, unique per sender within a connection | all | OCPP-J 1.6 §4.1.3; 2.x Part 4 §4.1.4 | Secondary |
| T8 | `permessage-deflate` (RFC 7692) is required for a CSMS and a Local Controller, optional for a Charging Station | 2.0.1, 2.1 | 2.x Part 4 §3.4 | Secondary |
| T9 | An unknown message type ID is ignored rather than answered with `MessageTypeNotSupported` (errata reported as 2026-04, entries 1161 and 1163) | 2.0.1, 2.1 | Errata | **Unconfirmed** |
| T10 | Security profiles 1–3 (Basic auth over plain WebSocket; TLS with Basic auth; TLS with client certificates) | 1.6 (whitepaper), 2.x | 1.6 Security Whitepaper; 2.x Part 2 §A | Secondary |

## Open verification items

| ID | Item | Blocks |
|---|---|---|
| ~~S1~~ | ~~Licensing status of generated Go types, and whether to vendor the schemas verbatim~~ Settled 2026-10-18: generated code is MIT; schemas are vendored verbatim (ADR 0007) | — |
| S2 | Version of the errata inside the current OCPP 1.6 bundle, and the OCPP-J 1.6 document edition | Phase 3 |
| S3 | Changes in Security Whitepaper Edition 4 relative to Edition 3, in particular any new messages | Phase 3 (security) |
| S4 | T1–T10 against the 2.0.1 Edition 4 and 2.1 Edition 2 Part 4 text and the 2026-09 errata | Phase 2 (T1–T3, T6, T7); Phase 5 (T8, T9); Phase 6 (T4, T5) |
| S5 | Diff of the 2.1 Edition 1 schemas (as shipped by third parties) against the Edition 2 / 2026-09 schemas | Phase 6 |
