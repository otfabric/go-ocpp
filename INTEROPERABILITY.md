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

Qualified against **otfabric/ocpp-interop v0.0.1**, pinned by digest in
`interop/harness.go`, the Makefile and `.github/workflows/interop.yml`:

| Reference | Upstream | Image |
|---|---|---|
| `mobilityhouse` | The Mobility House ocpp 2.1.0 (Python) | `ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:6243da69…` |
| `chargetime` | ChargeTimeEU Java-OCA-OCPP v2.0 (Java) | `ghcr.io/otfabric/ocpp-interop-chargetime@sha256:beeb0e2b…` |

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

### Results with the pinned images (ocpp-interop v0.0.1, OCPP 1.6J)

| Test | Reference | Sent by the station | Sent by the CSMS |
|---|---|---|---|
| go-ocpp client → reference CSMS | mobilityhouse | 13 pass | 20 pass |
| go-ocpp client → reference CSMS | chargetime | 13 pass | 20 pass |
| reference station → go-ocpp server | mobilityhouse | 12 pass, 1 known issue | 20 pass |
| reference station → go-ocpp server | chargetime | 13 pass | 20 pass |

These images declare OCPP 1.6 only, so the 2.0.1 runs skip.

### Results with the next ocpp-interop (unreleased, 2026-10-18)

Run locally (linux/arm64) against the images built from ocpp-interop's
`phase-5-ocpp201-lorenzodonini` branch, which adds OCPP 2.0.1 to every
adapter and the third reference, lorenzodonini/ocpp-go v0.19.0:

```bash
make interop OCPP_INTEROP_MOBILITYHOUSE_IMAGE=…:dev OCPP_INTEROP_CHARGETIME_IMAGE=…:dev OCPP_INTEROP_LORENZODONINI_IMAGE=…:dev
```

| Test | Reference | 1.6J station / CSMS | 2.0.1 station / CSMS |
|---|---|---|---|
| go-ocpp client → reference CSMS | mobilityhouse | 13 / 20 pass | 16 / 15 pass |
| go-ocpp client → reference CSMS | chargetime | 13 / 20 pass | 16 / 15 pass |
| go-ocpp client → reference CSMS | lorenzodonini | 13 / 20 pass | 16 / 15 pass |
| reference station → go-ocpp server | mobilityhouse | 12 pass + 1 known / 20 pass | 16 / 15 pass |
| reference station → go-ocpp server | chargetime | 13 / 20 pass | 16 / 15 pass |
| reference station → go-ocpp server | lorenzodonini | 13 / 20 pass | 15 pass + 1 known / 14 pass + 1 known |

Unknown action, close and security profile 1 pass in every combination.
These results become the qualified ones when ocpp-interop v0.1.0 is
published and its digests are pinned here.

### Known issues

| Reference | Scenario | Evidence |
|---|---|---|
| mobilityhouse 2.1.0 | The station cannot send SecurityEventNotification without the optional `techInfo` (`securityEventMinimal`) | [`ocpp/v16/call.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236) |
| ocpp-go v0.19.0 | The 2.0.1 station answers Reset with `"statusInfo": null`, which the schema does not allow and go-ocpp refuses (`resetImmediate`) | [`provisioning/reset.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/reset.go#L61-L64) |
| ocpp-go v0.19.0 | The 2.0.1 station drops `"tbc": false` from NotifyReport (`notifyReport`) | [`provisioning/notify_report.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/notify_report.go#L98-L104) |

ocpp-interop also records a ChargeTimeEU defect that go-ocpp's tests do not
reach: its 2.0.1 CSMS breaks permessage-deflate with a station that offers
it (Java-WebSocket 1.6.0). go-ocpp's client offers compression only with
`WithCompression`, which the interop suite does not set.

A scenario that a reference does not declare in its capabilities, or that
hits a known issue, is skipped with the reason. It is never counted as
passed.

### Not covered yet

- OCPP 2.0.1 with the pinned images: waits for ocpp-interop v0.1.0.
- OCPP 2.1: Phase 6.
- Compression against the references.
- TLS (security profiles 2 and 3): not yet in ocpp-interop.
