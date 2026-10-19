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

Qualified against **otfabric/ocpp-interop v0.4.1** (contract 0.5), pinned
by digest in `interop/harness.go`, the Makefile and
`.github/workflows/interop.yml`:

| Reference | Upstream | OCPP | Roles | TLS (profiles 2, 3) | Compression | Image |
|---|---|---|---|---|---|---|
| `mobilityhouse` | The Mobility House ocpp 2.1.0 (Python) | 1.6J, 2.0.1, 2.1 | station, CSMS | yes | yes | `ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:3f14958e…` |
| `chargetime` | ChargeTimeEU Java-OCA-OCPP v2.0 (Java) | 1.6J, 2.0.1 | station, CSMS | yes | 2.0.1, with upstream defects | `ghcr.io/otfabric/ocpp-interop-chargetime@sha256:221f0cec…` |
| `lorenzodonini` | lorenzodonini/ocpp-go v0.19.0 (Go) | 1.6J, 2.0.1 | station, CSMS | yes | no (its CSMS cannot accept it) | `ghcr.io/otfabric/ocpp-interop-lorenzodonini@sha256:e6cc15c2…` |
| `everest` | EVerest libocpp, everest-core 2026.02.1 (C++) | 1.6J, 2.0.1, 2.1 | station only (libocpp has no CSMS) | yes | no (libocpp cannot offer it) | `ghcr.io/otfabric/ocpp-interop-everest@sha256:abd99af8…` |
| `citrineos` | CitrineOS core v2.0.1 (TypeScript) | 1.6J, 2.0.1, 2.1 | CSMS only (CitrineOS has no station) | yes, with SNI only (upstream defect) | yes | `ghcr.io/otfabric/ocpp-interop-citrineos@sha256:143878b7…` |

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

A reference CSMS that answers by itself, not from the fixture (feature
`fixtureAnswers: false`, contract 0.5: `citrineos`, an application whose
answers come from its own database and logic), declares in `handles` only
the actions whose answer is the fixture's; those are compared with the
fixture as for the other references. A CALL go-ocpp sends with any other
action cannot be compared with the fixture, so what is checked is what holds
whatever the answer: a CALLRESULT must pass go-ocpp's typed validation of
the version (then it is a pass), and a CALLERROR must carry an error code the
version defines. A CALLERROR is the reference not processing the sample:
`NotImplemented` or `NotSupported`, or a refusal of its own
(`FormatViolation`, `PropertyConstraintViolation`), is skipped as
unsupported with the code and description, never passed. This mirrors
ocpp-interop's own `scripts/interop.sh`, which reports the from-station
`NotImplemented` test against such a CSMS as SKIP_UNSUPPORTED. The CSMS's
CALLs to go-ocpp (sent through CitrineOS's REST message API) are checked as
for every other reference: go-ocpp's answers are the fixture's.

Transport, in both roles: security profile 1 (HTTP Basic); profile 2 (TLS
and Basic) and profile 3 (mutual TLS, the client certificate's CommonName
is the identity), with a CA generated per run, including the refusals of a
wrong password, another station's certificate and an untrusted server
(`TestStationTLS`, `TestReferenceStationTLS`). go-ocpp as the station
connects to each reference CSMS twice, by address (`wss://127.0.0.1`, no
server name, RFC 6066 section 3) and by host name (`wss://localhost`, SNI,
the `-sni` scenarios); compression, with the
negotiation checked on the wire (`TestStationCompression`,
`TestReferenceStationCompression`).

### Results (v0.4.1 images, 2026-10-19, linux/arm64; CI runs amd64 and arm64)

All versions, all five references, both roles: **1458 scenarios pass, 0
fail**; 163 are skipped, each with its reason. Counted as the leaf tests of
`go test -json` (a role or version a reference does not declare is one
skipped test). By version: 1.6J 393 pass / 40 skipped, 2.0.1 632 / 34, 2.1
432 / 88. v0.4.1 changed no upstream: its EVerest adapter declares every
action libocpp exchanges (it declared the sets of v0.2.0 before), which
turns 32 EVerest skips into passes. By reference:

| Reference | Pass | Skip | Skipped because |
|---|---|---|---|
| mobilityhouse | 524 | 7 | known upstream defects (4), SEND not declared or no contract operation (2), BatterySwap not sent (1) |
| chargetime | 244 | 16 | OCPP 2.1 not supported (12), compression: 1.6 not declared, 2.0.1 known upstream defect (4) |
| lorenzodonini | 236 | 24 | OCPP 2.1 not supported (12), compression not supported (4), known upstream defects (8) |
| everest | 216 | 28 | no CSMS (21), compression not supported (3), GetCertificateChainStatus not sent (libocpp has no such message type, 1), SEND (1), ReportDERControl known upstream defect (1), NotifyWebPaymentStarted not declared and answered `NotImplemented` (1) |
| citrineos | 237 | 87 | below |

**citrineos** (CitrineOS core v2.0.1, the second independent OCPP 2.1
CSMS), with go-ocpp as the station; station-initiated passes (compared with
the fixture + checked against the schema only) / CSMS-initiated passes:

| OCPP | Station → CSMS | CSMS → station | Basic auth, TLS 2 and 3 (SNI), compression |
|---|---|---|---|
| 1.6J | 12 (10 + 2) | 18 | pass |
| 2.0.1 | 27 (20 + 7) | 38 | pass |
| 2.1 | 29 (20 + 9) | 39 | pass |

Its skips: no station (it has no client role: 15); CSMS actions its REST
message API cannot send (29: the 1.6 reservation and Security Whitepaper
messages, most actions OCPP 2.1 adds, and CancelReservation,
GetCompositeSchedule, InstallCertificate, SignedUpdateFirmware for the
baseline samples); `NotImplemented` for the 1.6 Security Whitepaper
messages from the station (LogStatusNotification, SecurityEventNotification,
SignCertificate, SignedFirmwareStatusNotification: 5 scenarios, twice with
compression); `PropertyConstraintViolation` for NotifyCustomerInformation and
NotifyDisplayMessages with a `requestId` it did not send (2.0.1, 2.1);
`FormatViolation` for nine 2.1 station messages (BatterySwap,
Open/ClosePeriodicEventStream, GetCertificateChainStatus, NotifyDERAlarm,
NotifyDERStartStop, NotifyPriorityCharging, PullDynamicScheduleUpdate,
ReportDERControl) whose samples validate against the OCA 2.1 schemas and pass
go-ocpp's validation, as ocpp-interop's adapter README also records; SEND not
supported (1); TLS by address (6, the known upstream defect below).

- **TLS**: profiles 2 and 3 pass with every reference CSMS (go-ocpp as the
  station: The Mobility House 1.6J, 2.0.1, 2.1; ChargeTimeEU and ocpp-go
  1.6J, 2.0.1, by address and by host name; CitrineOS 1.6J, 2.0.1, 2.1 by
  host name) and with every reference station (go-ocpp as the CSMS: The
  Mobility House, ChargeTimeEU, ocpp-go and EVerest libocpp).
- **Compression**: passes with The Mobility House in both roles and with
  CitrineOS as the CSMS, 1.6J, 2.0.1 and 2.1. ChargeTimeEU 2.0.1 is a known
  upstream defect in both roles; ocpp-go and EVerest do not support it.
- **OCPP 2.1** against three independent implementations: two CSMSs (The
  Mobility House, CitrineOS) with go-ocpp as the station, and two stations
  (The Mobility House, EVerest libocpp) with go-ocpp as the CSMS. No
  released reference sends or receives SEND or CALLRESULTERROR, so go-ocpp's
  are verified against go-ocpp only (e2e).
- The compression test's probe connection: CitrineOS handles the close of a
  connection after a new connection of the same identity is registered, and
  the new one then gets no answers. The test therefore connects again only
  after the reference reports the probe's `connection-closed` event.

### Known issues

| Reference | Scenario | Evidence |
|---|---|---|
| mobilityhouse 2.1.0 | The station cannot send SecurityEventNotification without the optional `techInfo` (`securityEventMinimal`) | [`ocpp/v16/call.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236) |
| mobilityhouse 2.1.0 | A 2.1 CALL without a handler is never answered, in either role (`unknown-action`); fixed on master (ae716a1), not released | [`ocpp/charge_point.py`](https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/charge_point.py#L161-L193) |
| ocpp-go v0.19.0 | The 2.0.1 station answers Reset and SetNetworkProfile with `"statusInfo": null`, which the schema does not allow and go-ocpp refuses (`resetImmediate`, `setNetworkProfile`) | [`provisioning/reset.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/reset.go#L61-L64), [`set_network_profile.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/set_network_profile.go#L170-L172) |
| ocpp-go v0.19.0 | The 2.0.1 station drops `"tbc": false` from NotifyReport (`notifyReport`) | [`provisioning/notify_report.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/notify_report.go#L98-L104) |
| ocpp-go v0.19.0 | GetCompositeScheduleResponse does not have the OCA structure (`getCompositeSchedule`) | [`smartcharging/get_composite_schedule.go`](https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/smartcharging/get_composite_schedule.go#L31-L50) |
| chargetime v2.0 | Compression, 2.0.1: the CSMS announces `server_no_context_takeover` but keeps its context; the station offers `client_no_context_takeover` but keeps its context (Java-WebSocket 1.6.0) | [`PerMessageDeflateExtension.java`](https://github.com/TooTallNate/Java-WebSocket/blob/da3cf2a777aed862f2f5b5cf060cae7969958667/src/main/java/org/java_websocket/extensions/permessage_deflate/PerMessageDeflateExtension.java#L326-L340) |
| citrineos v2.0.1 | TLS (profiles 2 and 3) by IP address: the https server has no certificate of its own, only an SNICallback, so a station that sends no server name gets the alert handshake_failure (`tls-profile2-csms`, `tls-profile3-csms`, 1.6J, 2.0.1, 2.1); the same profiles pass with a host name (`-sni`) | [`websocket-network-connection.ts`](https://github.com/citrineos/citrineos-core/blob/1a83638c31eebdb1dd8d6cdb0f5b8a9f142a1372/packages/ocpp/src/transport/network-connection/websocket-network-connection.ts#L937-L956) |
| everest 2026.02.1 | libocpp's `ocpp::v2::MessageType` has no ReportDERControl (nor NotifyWebPaymentStarted, which the station does not declare): the station cannot send it (`reportDERControl`) | [`ocpp/v2/types.hpp`](https://github.com/EVerest/everest-core/blob/b61bb12b8cb89ee9d0fab037b05c3bb8cea9b13e/lib/everest/ocpp/include/ocpp/v2/types.hpp#L15-L195) |

A scenario that a reference does not declare in its capabilities, or that
hits a known issue, is skipped with the reason. It is never counted as
passed.

### Not covered yet

- SEND and CALLRESULTERROR against any independent implementation: no
  released reference implements them.
- Compression with go-ocpp as the CSMS against a second independent
  station (ChargeTimeEU's is defective in 2.0.1, ocpp-go and EVerest
  cannot; CitrineOS has no station).
- Station-initiated 2.1 messages that CitrineOS refuses (above) against a
  second CSMS; their answers by CitrineOS are checked only against the
  schema, not against an expected value.
- One action with no independent peer: NotifyPeriodicEventStream (2.1), a
  SEND. 2.1 BatterySwap is exchanged in one direction only: The Mobility
  House's CSMS receives it from go-ocpp, but no reference station can send
  it.
