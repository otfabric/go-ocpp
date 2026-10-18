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
| OCPP 1.6 | OCPP 1.6 specification and OCPP-J 1.6 (2015-10-08), "OCPP 1.6 (all files & errata)" bundle | OCPP 1.6 and OCPP-J 1.6 errata sheets 2025-04 | P0 |
| OCPP 1.6 security | Improved security for OCPP 1.6-J, Security Whitepaper **Edition 4** (2026-02-05) | — | P0 |
| OCPP 2.0.1 | OCPP 2.0.1 **Edition 4**, Parts 0–6, Part 2 appendices v1.5 | **Edition 4 Errata 2026-09** | P0 |
| OCPP 2.1 | OCPP 2.1 **Edition 2**, Parts 0–6, Part 2 appendices v2.1 | **Edition 2 Errata 2026-09** | P1 |

Excluded: OCPP 1.6 SOAP, OCPP 1.5 and older.

OCPP 2.0.1 Edition 3 is also published as IEC 63584. go-ocpp follows the OCA
edition, which is newer.

What the repository is built from is recorded in
[schemas/UPSTREAM.json](schemas/UPSTREAM.json) (archive labels, URLs and
SHA-256). A weekly workflow (`spec-check.yml`) compares it with the newest
documents on the OCA download page and opens an issue when the OCA has
published new editions, errata or appendices; see
[schemas/README.md](schemas/README.md#staying-current).

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
| 1.6J core | draft-04 | 28 | 56 | OCPP 1.6 bundle | see `schemas/README.md` |
| 1.6J security extensions | draft-06 | 11 | 22 | Security Whitepaper Ed. 4 bundle | see `schemas/README.md` |
| 2.0.1 | draft-06 | 64 | 128 | 2.0.1 Edition 4 bundle | see `schemas/README.md` |
| 2.1 | draft-06 | 91 | 181 | 2.1 Edition 2 bundle | see `schemas/README.md` |

The counts are those of the OCA archives, whose schemas are committed
unmodified under [schemas/](schemas/README.md):

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

Checked on 2026-10-18 against the OCA texts: OCPP-J 1.6 (2015-10-08) with
the OCPP-J 1.6 errata sheet 2025-04, OCPP 2.0.1 Edition 4 Part 4
(2025-12-03), OCPP 2.1 Edition 2 Part 4 (2025-12-03), and the 2026-09
errata of both.

| # | Rule | Versions | Source | Code |
|---|---|---|---|---|
| T1 | Subprotocols `ocpp1.6`, `ocpp2.0.1`, `ocpp2.1` in `Sec-WebSocket-Protocol`; the station lists the versions it supports, the CSMS selects one. A CSMS that agrees to none MUST complete the handshake without a `Sec-WebSocket-Protocol` header and then immediately close | all | OCPP-J 1.6 §3.1.2; 2.x Part 4 §3.1.2, §3.2 | `server.ServeHTTP` |
| T2 | The station identity is the last path segment of the URL; at most 48 characters in 2.x. A CSMS that does not recognise it SHOULD answer HTTP 404 | all | OCPP-J 1.6 §3.1.1, §3.1.2; 2.x Part 4 §3.1.1, §3.2 | `server.ErrUnknownStation` |
| T3 | Frames: CALL `[2, id, action, payload]`, CALLRESULT `[3, id, payload]`, CALLERROR `[4, id, code, description, details]`. A corrupt message gets a CALLERROR; when its message ID cannot be read, with ID `"-1"` (2.x) | all | OCPP-J 1.6 §4; 2.x Part 4 §4.2.3 | `ocppj`, `engine.malformed` |
| T4 | CALLRESULTERROR `[5, id, code, description, details]`, with the ID of the CALLRESULT it answers | 2.1 | 2.1 Part 4 §4.1.4, §4.2.3 | Phase 6 |
| T5 | SEND `[6, id, action, payload]`: never answered with CALLRESULT or CALLERROR, and may be sent at any time, also while a CALL is outstanding | 2.1 | 2.1 Part 4 §4.1.1, §4.2.4 | Phase 6 |
| T6 | A CALL is not sent until the previous CALL has been answered or has timed out, per connection. 1.6 says SHOULD NOT, 2.x SHALL NOT. The timeout is the implementation's choice | all | OCPP-J 1.6 §4.1.1; 2.x Part 4 §4.1.1 | `engine` (ADR 0004) |
| T7 | Message IDs: at most 36 characters; a CALL's ID differs from every earlier CALL ID of the same sender on any connection of the same identity; a response carries the ID of its CALL | all | OCPP-J 1.6 §4.1.3; 2.x Part 4 §4.1.4 | random UUIDs |
| T8 | RFC 7692 compression: required for a CSMS (and a Local Controller), optional for a Charging Station | 2.0.1, 2.1 | 2.0.1 Part 4 §3.3; 2.1 Part 4 §3.4 | Phase 5 |
| T9 | A message with an unknown message type number is ignored. `MessageTypeNotSupported` is deprecated, and the "extension fallback mechanism" of §4.4 is removed | 2.0.1, 2.1 | errata 1161 and 1163 (2026-02) to Part 4 §4.1.3, §4.3, §4.4 | `engine.malformed` |
| T10 | Security profiles 1–3: Basic authentication over plain WebSocket; TLS with Basic authentication; TLS with client certificates. The user name is the identity, the CSMS SHALL check both user name and password | 1.6 (whitepaper), 2.x | 1.6 Security Whitepaper Ed. 4 A00.FR.204, .207, .324; 2.x Part 2 A00 | `client`, `server.BasicAuth` |
| T11 | A station reconnects with a back-off of RetryBackOffWaitMinimum plus up to RetryBackOffRandomRange, doubling up to RetryBackOffRepeatTimes times | 2.0.1, 2.1 | 2.x Part 4 §5.4, §8 | `client.Reconnect` |

Checking against the text changed three Phase 2 behaviours: a CSMS without a
common subprotocol now completes the handshake and closes instead of
answering HTTP 400 (T1); a corrupt frame on a 2.x connection whose ID cannot
be read is answered with ID `"-1"` instead of being dropped (T3); and the
client's reconnect back-off follows T11.

## Open verification items

| ID | Item | Status |
|---|---|---|
| S1 | Licensing of generated Go types, and vendoring the schemas | Settled 2026-10-18: generated code is MIT; schemas vendored verbatim (ADR 0007) |
| S2 | Errata of the OCPP 1.6 bundle; OCPP-J 1.6 edition | Settled: OCPP-J 1.6 of 2015-10-08 with errata sheet 2025-04 (StopTransaction schema updated, `urn:OCPP:1.6:2025:04`, included in `schemas/ocpp16/core`) |
| S3 | Changes in Security Whitepaper Edition 4 | Settled: no new or changed messages (the schemas are those of 2022); new requirements (TLS 1.0/1.1 removed, Central System Basic-auth checks, certificate renewal, firmware and log flows) are listed in its chapter 11 |
| S4 | T1–T10 against the OCA text | Settled: see the table above |
| S5 | 2.1 schemas of third parties against Edition 2 | Settled for go-ocpp: it uses the Edition 2 schemas from the OCA archive directly; the errata 2026-09 change no schema |
