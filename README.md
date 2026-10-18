# go-ocpp - Open Charge Point Protocol Client and Server Library for Go

[![Go Version](https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-ocpp.svg)](https://pkg.go.dev/github.com/otfabric/go-ocpp)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-ocpp/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-ocpp/actions/workflows/ci.yml)
[![Interop](https://github.com/otfabric/go-ocpp/actions/workflows/interop.yml/badge.svg)](https://github.com/otfabric/go-ocpp/actions/workflows/interop.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-ocpp/graph/badge.svg)](https://codecov.io/gh/otfabric/go-ocpp)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/go-ocpp?label=release)](https://github.com/otfabric/go-ocpp/releases)

A pure Go implementation of the Open Charge Point Protocol, for both roles:
the Charging Station (OCPP 1.6: Charge Point) and the CSMS (OCPP 1.6: Central
System). Both share one RPC engine, and either side can send requests once
connected.

> **Status: Phase 5 of 8.** OCPP 1.6J (with the Security Whitepaper
> extensions) and OCPP 2.0.1 are complete at the message level: every action
> is typed, validated against the OCA schemas, and dispatchable in both
> roles. Both versions pass against three independent implementations in
> both roles; the 2.0.1 results are pinned once the next ocpp-interop
> release is published. OCPP 2.1 is next (Phase 6). No release yet. See
> [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

Docs: [Architecture](ARCHITECTURE.md) · [Requirements](REQUIREMENTS.md) ·
[Specifications](SPECIFICATIONS.md) · [Plan](IMPLEMENTATION_PLAN.md) ·
[API](API.md) · [Errors](ERRORS.md) · [Observability](OBSERVABILITY.md) ·
[Security](SECURITY.md) · [Interoperability](INTEROPERABILITY.md) ·
[Compatibility](COMPATIBILITY.md) · [Releases](RELEASE.md)

## Table of contents

- [Protocol versions](#protocol-versions)
- [Install](#install)
- [Quick start](#quick-start)
- [Interoperability](#interoperability)
- [Assurance](#assurance)
- [Development](#development)
- [License](#license)

## Protocol versions

| Version | Priority | Status |
|---|---|---|
| OCPP 1.6J, with the Security Whitepaper (Edition 4) extensions | P0 | 39 actions, both roles (`ocpp16`) |
| OCPP 2.0.1 Edition 4, errata 2026-09 | P0 | 64 actions, both roles (`ocpp201`) |
| OCPP 2.1 Edition 2, errata 2026-09 | P1 | Phase 6 |
| OCPP 1.6 SOAP, OCPP 1.5 and older | — | out of scope |

## Install

```bash
go get github.com/otfabric/go-ocpp
```

Requires Go 1.26 or later. The only dependency of the library is
[coder/websocket](https://github.com/coder/websocket)
([ADR 0002](docs/adr/0002-websocket-transport.md)); the tests also use a
reference JSON Schema validator ([ADR 0003](docs/adr/0003-schema-generation.md)).

## Quick start

A CSMS and a charging station, OCPP 2.0.1 (the [examples](examples) run
complete, self-contained versions of this for 1.6 and 2.0.1):

```go
// CSMS: answer the stations' CALLs.
type csms struct{ ocpp201.UnimplementedCSMSHandler }

func (csms) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
    return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

srv, err := server.New(
    server.WithHandler(ocpp.V201, ocpp201.CSMS(csms{})),
    server.WithAuthenticator(server.BasicAuth(lookupPassword)),
)
http.Handle("/ocpp/", srv) // behind TLS

// Send a CALL to a connected station.
if s, ok := srv.Station("STATION001"); ok {
    resp, err := ocpp201.Reset(ctx, s, &ocpp201.ResetRequest{Type: ocpp201.ResetOnIdle})
}

// Charging station.
c, err := client.New("wss://csms.example.com/ocpp/STATION001",
    client.WithHandler(ocpp.V201, ocpp201.ChargingStation(station{})),
    client.WithBasicAuth("STATION001", password),
    client.WithReconnect(client.DefaultReconnect),
    client.WithCompression(),
)
err = c.Connect(ctx)
boot, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
    Reason:          ocpp201.BootReasonPowerUp,
    ChargingStation: ocpp201.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
})
```

Every request is validated against its schema before it is sent, every
response when it arrives, and every incoming request before a handler sees
it; an invalid one is answered with the CALLERROR its violation calls for.

go-ocpp is a protocol library. It transports, validates and dispatches
messages. Charging behaviour, authorization policy, persistence and billing
belong to the application's handlers.

## Interoperability

go-ocpp is tested in both roles against The Mobility House ocpp 2.1.0 and
ChargeTimeEU Java-OCA-OCPP v2.0, as packaged by
[otfabric/ocpp-interop v0.0.1](https://github.com/otfabric/ocpp-interop/releases/tag/v0.0.1).
For OCPP 1.6J with raw payloads, every fixture sample passes in both
directions, except one known upstream defect. See
[INTEROPERABILITY.md](INTEROPERABILITY.md).

```bash
make interop   # needs Docker
```

## Assurance

go-ocpp distinguishes three levels of assurance and claims only the first
two, once tests establish them:

| Level | Meaning |
|---|---|
| Schema conformant | Messages validate against the OCA JSON schemas of their version |
| Interoperability tested | Communication succeeds against independent implementations from [otfabric/ocpp-interop](https://github.com/otfabric/ocpp-interop), in both roles |
| OCA certified | Formal certification of a specific product with the OCA. **Not claimed** |

## Development

```bash
make help     # list targets
make check    # format, lint, vet, vuln, test, coverage, cross-build
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md).

## License

MIT; see [LICENSE](LICENSE). OCPP specifications and schemas are © Open
Charge Alliance, licensed under CC BY-ND 4.0, and are not part of this
licence. See [SPECIFICATIONS.md](SPECIFICATIONS.md#licensing).
