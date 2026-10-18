# Interoperability

go-ocpp is tested against independent OCPP implementations, which
[otfabric/ocpp-interop](https://github.com/otfabric/ocpp-interop) packages as
containers. This repository consumes the published images, pinned by digest,
and nothing else from that repository.

## Levels of assurance

| Level | Established by |
|---|---|
| Schema conformant | Generated validation, and round-trip tests of every message |
| Interoperability tested | The `interop/` suite, in both roles, against at least two independent implementations |
| OCA certified | OCA certification of a product. go-ocpp does not claim it |

Interoperability with other implementations does not establish conformance
to the specification. Each can share the same misreading.

## Test directions

```text
go-ocpp client (station)  ──▶  reference CSMS
reference client (station) ──▶  go-ocpp server (CSMS)
```

Both are required. In each, CALLs are sent from both ends of the connection.

## Status

Qualified against **otfabric/ocpp-interop v0.1.0** (contract 0.3), pinned
by digest in `interop/harness.go`, the Makefile and
`.github/workflows/interop.yml`:

| Reference | Upstream | OCPP | Image |
|---|---|---|---|
| `mobilityhouse` | The Mobility House ocpp 2.1.0 (Python) | 1.6J, 2.0.1 | `ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:fff81e3c…` |
| `chargetime` | ChargeTimeEU Java-OCA-OCPP v2.0 (Java) | 1.6J, 2.0.1 | `ghcr.io/otfabric/ocpp-interop-chargetime@sha256:01b3d8d3…` |
| `lorenzodonini` | lorenzodonini/ocpp-go v0.19.0 (Go) | 1.6J, 2.0.1 | `ghcr.io/otfabric/ocpp-interop-lorenzodonini@sha256:27801a6a…` |

`TestReferences` checks that each pinned image reports this release.

### What runs

For each reference, OCPP version (1.6J and 2.0.1) and direction, on one
connection:

- every station sample of the image's fixture, sent by the station;
- every CSMS sample, sent by the CSMS;
- an action that does not exist, which must be answered with
  `NotImplemented` or `NotSupported`;
- a close, which the other end must notice.

Each answer is compared with the one the fixture defines, read from the
image (`print-fixture`), including the derived answers: authorization by
id token and the device model of GetVariables and SetVariables. Every
request a reference sends to go-ocpp, and every response go-ocpp receives,
must also pass go-ocpp's typed validation (`ocpp16`, `ocpp201`); for CALLs
the reference sends, the request must also keep every field of the sample.
Security profile 1 (HTTP Basic) is tested in both roles.

### Results (2026-10-18, linux/arm64; CI runs amd64 and arm64)

| Test | Reference | 1.6J station / CSMS | 2.0.1 station / CSMS |
|---|---|---|---|
| go-ocpp client → reference CSMS | mobilityhouse | 13 / 20 pass | 16 / 15 pass |
| go-ocpp client → reference CSMS | chargetime | 13 / 20 pass | 16 / 15 pass |
| go-ocpp client → reference CSMS | lorenzodonini | 13 / 20 pass | 16 / 15 pass |
| reference station → go-ocpp server | mobilityhouse | 12 pass + 1 known / 20 pass | 16 / 15 pass |
| reference station → go-ocpp server | chargetime | 13 / 20 pass | 16 / 15 pass |
| reference station → go-ocpp server | lorenzodonini | 13 / 20 pass | 15 pass + 1 known / 14 pass + 1 known |

"Station" counts the CALLs the station sends, "CSMS" those the CSMS sends.
Unknown action, close and security profile 1 pass in every combination.

### OCPP 2.1 (against ocpp-interop's unreleased Phase 6 build)

The harness runs 2.1 wherever a reference declares it; the pinned v0.1.0
images declare none, so CI skips 2.1 until ocpp-interop v0.2.0 is released
and pinned. Against a local build of its `phase-6-ocpp21` branch (b13c25b,
contract 0.4; 2026-10-18, linux/arm64), with all three references and every
version (1.6J and 2.0.1 unchanged from the table above, 0 failures):

| Test | Reference | 2.1 station / CSMS |
|---|---|---|
| go-ocpp client → reference CSMS | mobilityhouse | 21 pass + 1 known + 1 SEND skipped / 23 pass + 1 known |
| reference station → go-ocpp server | mobilityhouse | 20 pass + 1 known + 1 SEND skipped + 1 not declared / 23 pass + 1 known |

Security profile 1 passes in both roles. The known issue is Mobility House
2.1.0 not answering 2.1 CALLs it has no handler for (below), which also
skips the unknown-action scenario. Neither chargetime (2.1 on its master
branch only) nor lorenzodonini supports 2.1. No reference release sends or
receives SEND or CALLRESULTERROR, so go-ocpp's are verified against go-ocpp
only (e2e) — not against an independent implementation.

### Known issues

| Reference | Scenario | Evidence |
|---|---|---|
| mobilityhouse 2.1.0 | The station cannot send SecurityEventNotification without the optional `techInfo` (`securityEventMinimal`) | [`ocpp/v16/call.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236) |
| ocpp-go v0.19.0 | The 2.0.1 station answers Reset with `"statusInfo": null`, which the schema does not allow and go-ocpp refuses (`resetImmediate`) | [`provisioning/reset.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/reset.go#L61-L64) |
| ocpp-go v0.19.0 | The 2.0.1 station drops `"tbc": false` from NotifyReport (`notifyReport`) | [`provisioning/notify_report.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/notify_report.go#L98-L104) |
| mobilityhouse 2.1.0 | A 2.1 CALL without a handler is never answered, in either role (`reservationStatusUpdate`, `cancelReservation`, `unknown-action`); fixed on master (ae716a1), not released | [`ocpp/charge_point.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/charge_point.py#L161-L193) |

ocpp-interop also records a ChargeTimeEU defect that go-ocpp's tests do not
reach: its 2.0.1 CSMS breaks permessage-deflate with a station that offers
it (Java-WebSocket 1.6.0). go-ocpp's client offers compression only with
`WithCompression`, which the interop suite does not set.

A scenario that a reference does not declare in its capabilities, or that
hits a known issue, is skipped with the reason. It is never counted as
passed.

### Not covered yet

- OCPP 2.1 against released images (ocpp-interop v0.2.0), and against a
  second independent implementation.
- SEND and CALLRESULTERROR against any independent implementation.
- Actions no reference adapter declares: per version, the matrices in
  [docs/compatibility](docs/compatibility) list which actions are
  exchanged with a reference (30/39 in 1.6J, 27/64 in 2.0.1, 43/91 in
  2.1) and which are verified against go-ocpp only.
- Compression against the references.
- TLS (security profiles 2 and 3): not yet in ocpp-interop.
