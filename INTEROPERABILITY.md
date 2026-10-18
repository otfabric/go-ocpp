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

OCPP 1.6J only, with raw JSON payloads: the version packages arrive in
Phase 3. On one connection per reference and direction:

- every station sample of the image's fixture, sent by the station;
- every CSMS sample, sent by the CSMS;
- an action that does not exist, which must be answered with
  `NotImplemented` or `NotSupported`;
- a close, which the other end must notice.

Each answer is compared with the one the fixture defines, read from the
image (`print-fixture`). For CALLs the reference sends, the request that
reaches go-ocpp must also keep every field of the sample. Security profile 1
(HTTP Basic) is tested in both roles: the correct password, a wrong one and
none.

### Results (2026-10-18, linux/arm64; CI runs amd64 and arm64)

| Test | Reference | Sent by the station | Sent by the CSMS |
|---|---|---|---|
| go-ocpp client → reference CSMS | mobilityhouse | 13 pass | 20 pass |
| go-ocpp client → reference CSMS | chargetime | 13 pass | 20 pass |
| reference station → go-ocpp server | mobilityhouse | 12 pass, 1 known issue | 20 pass |
| reference station → go-ocpp server | chargetime | 13 pass | 20 pass |

Unknown action, close and security profile 1 also pass, against both
references and in both roles.

### Known issues

| Reference | Scenario | Evidence |
|---|---|---|
| mobilityhouse 2.1.0 | The station cannot send SecurityEventNotification without the optional `techInfo` (`securityEventMinimal`) | [`ocpp/v16/call.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236) |

A scenario that a reference does not declare in its capabilities, or that
hits a known issue, is skipped with the reason. It is never counted as
passed.

### Not covered yet

- OCPP 2.0.1 and 2.1: the references have 1.6J adapters only (ocpp-interop
  Phase 5).
- TLS (security profiles 2 and 3): not in ocpp-interop v0.0.1.
- Typed messages and schema validation on the go-ocpp side (Phase 3).
