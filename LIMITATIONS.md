# Limitations

What go-ocpp does not do, and what has not been verified. The per-action
status is in [COMPATIBILITY.md](COMPATIBILITY.md) and
[docs/compatibility](docs/compatibility), the test evidence in
[INTEROPERABILITY.md](INTEROPERABILITY.md), and planned features in
[FEATURES.md](FEATURES.md).

## Scope not implemented

- OCPP 1.6 SOAP, and OCPP 1.5 and older: out of scope.
- BootNotification, Heartbeat and StatusNotification are not sent by the
  library. They are application messages.
- No charging logic: the library does not start or stop charging, download
  firmware, store configuration or the device model, or keep local
  authorization lists ([ARCHITECTURE.md](ARCHITECTURE.md#what-the-library-does-not-do)).
- Nothing is persisted and nothing is queued while offline: `Call` fails at
  once with `ErrNotConnected` or `ErrConnectionLost`, and the application
  queues. An offline queue is planned and needs an ADR (FEATURES.md, item 13).
- No translation between OCPP versions.
- After SetNetworkProfile, the client does not switch URL or security profile
  or fall back along NetworkConfigurationPriority: the application creates a
  new `Client` (planned, FEATURES.md, item 5).
- No Local Controller role (planned, FEATURES.md, item 11); no reference stack
  implements it to test against.
- Validation cannot be exempted per peer: an application that must accept a
  station that does not conform handles that action with a raw dispatcher.
- Operating a certificate store is left to the application: OCPP-level
  security messages are ordinary messages, and the library holds no
  certificates. `ocppcert` covers CertificateHashData only.

## Verification

- Independently tested against the reference builds of
  [otfabric/ocpp-interop](https://github.com/otfabric/ocpp-interop), pinned by
  digest, as listed in [INTEROPERABILITY.md](INTEROPERABILITY.md#status).
  Interoperability does not establish conformance: implementations can share a
  misreading.
- Not covered by any independent implementation:
  - SEND and CALLRESULTERROR: no released reference implements them. This
    includes the 2.1 action NotifyPeriodicEventStream, which is a SEND.
  - Compression with go-ocpp as the CSMS against a second independent station.
  - 2.1 BatterySwap is exchanged in one direction only: no reference station
    can send it.
  - The station-initiated 2.1 messages that CitrineOS refuses have no second
    CSMS, and CitrineOS's answers to the others are checked only against the
    schema.
- Not tested against commercial charging stations or CSMS products.
- Certification: none. OCA certification has not been attempted, and go-ocpp
  does not claim it.

## Known deviations

Deviations of peers, not of go-ocpp, are listed with their upstream sources in
[INTEROPERABILITY.md](INTEROPERABILITY.md).
