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

Qualified against **otfabric/ocpp-interop v0.3.1** (contract 0.5), pinned
by digest in `interop/harness.go`, the Makefile and
`.github/workflows/interop.yml`:

| Reference | Upstream | OCPP | Roles | TLS (profiles 2, 3) | Compression | Image |
|---|---|---|---|---|---|---|
| `mobilityhouse` | The Mobility House ocpp 2.1.0 (Python) | 1.6J, 2.0.1, 2.1 | station, CSMS | yes | yes | `ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:e8289633…` |
| `chargetime` | ChargeTimeEU Java-OCA-OCPP v2.0 (Java) | 1.6J, 2.0.1 | station, CSMS | yes | 2.0.1, with upstream defects | `ghcr.io/otfabric/ocpp-interop-chargetime@sha256:a46c0db2…` |
| `lorenzodonini` | lorenzodonini/ocpp-go v0.19.0 (Go) | 1.6J, 2.0.1 | station, CSMS | yes | no (its CSMS cannot accept it) | `ghcr.io/otfabric/ocpp-interop-lorenzodonini@sha256:8328f5bf…` |
| `everest` | EVerest libocpp, everest-core 2026.02.1 (C++) | 1.6J, 2.0.1, 2.1 | station only (libocpp has no CSMS) | yes | no (libocpp cannot offer it) | `ghcr.io/otfabric/ocpp-interop-everest@sha256:990c3ea9…` |

`TestReferences` checks that each pinned image reports this release. The
per-version action matrices against these references are in
[docs/compatibility](docs/compatibility) (`make compat`).

### What runs

For each reference, OCPP version and role it declares, on one connection:

- every station sample of the image's fixture, sent by the station;
- every CSMS sample, sent by the CSMS (the fixtures have a sample of every
  action);
- an action that does not exist, which must be answered with
  `NotImplemented` or `NotSupported`;
- a close, which the other end must notice.

Each answer is compared with the one the fixture defines, read from the
image (`print-fixture`), including the derived answers: authorization by
id token and the device model of GetVariables and SetVariables. Every
request a reference sends to go-ocpp, and every response go-ocpp receives,
must also pass go-ocpp's typed validation (`ocpp16`, `ocpp201`, `ocpp21`);
for CALLs the reference sends, the request must also keep every field of
the sample. A CALL go-ocpp sends with an action the reference does not
declare handling is a pass if it is answered as the fixture says, and is
skipped otherwise.

Transport, in both roles: security profile 1 (HTTP Basic); profile 2 (TLS
and Basic) and profile 3 (mutual TLS, the client certificate's CommonName
is the identity), with a CA generated per run, including the refusals of a
wrong password, another station's certificate and an untrusted server
(`TestStationTLS`, `TestReferenceStationTLS`); compression, with the
negotiation checked on the wire (`TestStationCompression`,
`TestReferenceStationCompression`).

### Results (v0.3.1 images, 2026-10-18, linux/arm64; CI runs amd64 and arm64)

All versions, all four references, both roles: **1237 scenarios pass, 0
fail**; 98 are skipped, each with its reason (a documented upstream defect,
a role, version, profile or feature a reference does not declare, a SEND no
reference supports). By version: 1.6J 289 pass / 5 skipped, 2.0.1 456 / 20,
2.1 261 / 24.

- **TLS**: profiles 2 and 3 pass with every reference CSMS (go-ocpp as the
  station: The Mobility House 1.6J, 2.0.1, 2.1; ChargeTimeEU and ocpp-go
  1.6J, 2.0.1) and with every reference station (go-ocpp as the CSMS: the
  same, and EVerest libocpp in 1.6J, 2.0.1 and 2.1).
- **Compression**: passes with The Mobility House in both roles, 1.6J,
  2.0.1 and 2.1. ChargeTimeEU 2.0.1 is a known upstream defect in both
  roles; ocpp-go and EVerest do not support it.
- **OCPP 2.1** against two independent implementations: The Mobility House
  in both roles, and EVerest libocpp as a station against the go-ocpp CSMS.
  No released reference sends or receives SEND or CALLRESULTERROR, so
  go-ocpp's are verified against go-ocpp only (e2e).

### Known issues

| Reference | Scenario | Evidence |
|---|---|---|
| mobilityhouse 2.1.0 | The station cannot send SecurityEventNotification without the optional `techInfo` (`securityEventMinimal`) | [`ocpp/v16/call.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236) |
| mobilityhouse 2.1.0 | A 2.1 CALL without a handler is never answered, in either role (`unknown-action`); fixed on master (ae716a1), not released | [`ocpp/charge_point.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/charge_point.py#L161-L193) |
| ocpp-go v0.19.0 | The 2.0.1 station answers Reset and SetNetworkProfile with `"statusInfo": null`, which the schema does not allow and go-ocpp refuses (`resetImmediate`, `setNetworkProfile`) | [`provisioning/reset.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/reset.go#L61-L64), [`set_network_profile.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/set_network_profile.go#L170-L172) |
| ocpp-go v0.19.0 | The 2.0.1 station drops `"tbc": false` from NotifyReport (`notifyReport`) | [`provisioning/notify_report.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/notify_report.go#L98-L104) |
| ocpp-go v0.19.0 | GetCompositeScheduleResponse does not have the OCA structure (`getCompositeSchedule`) | [`smartcharging/get_composite_schedule.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/smartcharging/get_composite_schedule.go#L31-L50) |
| chargetime v2.0 | Compression, 2.0.1: the CSMS announces `server_no_context_takeover` but keeps its context; the station offers `client_no_context_takeover` but keeps its context (Java-WebSocket 1.6.0) | [`PerMessageDeflateExtension.java`](https://github.com/TooTallNate/Java-WebSocket/blob/da3cf2a777aed862f2f5b5cf060cae7969958667/src/main/java/org/java_websocket/extensions/permessage_deflate/PerMessageDeflateExtension.java#L326-L340) |
| everest 2026.02.1 | libocpp's `ocpp::v2::MessageType` has no ReportDERControl (nor NotifyWebPaymentStarted, which the station does not declare): the station cannot send it (`reportDERControl`) | [`ocpp/v2/types.hpp`](https://github.com/EVerest/everest-core/blob/b61bb12b8cb89ee9d0fab037b05c3bb8cea9b13e/lib/everest/ocpp/include/ocpp/v2/types.hpp#L15-L195) |

A scenario that a reference does not declare in its capabilities, or that
hits a known issue, is skipped with the reason. It is never counted as
passed.

### Not covered yet

- OCPP 2.1 with go-ocpp as the station against a second independent CSMS:
  EVerest libocpp has no CSMS. CitrineOS (TypeScript) is the candidate.
- SEND and CALLRESULTERROR against any independent implementation: no
  released reference implements them.
- Compression against a second independent stack in 2.0.1 (ChargeTimeEU's
  is defective, ocpp-go's CSMS and EVerest cannot).
- One action with no independent peer: NotifyPeriodicEventStream (2.1), a
  SEND. 2.1 BatterySwap is exchanged in one direction only: The Mobility
  House's CSMS receives it from go-ocpp, but no reference station can send
  it.
