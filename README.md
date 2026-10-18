# go-ocpp - Open Charge Point Protocol Client and Server Library for Go

[![Go Version](https://img.shields.io/badge/Go-1.26%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-ocpp.svg)](https://pkg.go.dev/github.com/otfabric/go-ocpp)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-ocpp/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-ocpp/actions/workflows/ci.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-ocpp/graph/badge.svg)](https://codecov.io/gh/otfabric/go-ocpp)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/go-ocpp?label=release)](https://github.com/otfabric/go-ocpp/releases)

A pure Go implementation of the Open Charge Point Protocol, for both roles:
the Charging Station (OCPP 1.6: Charge Point) and the CSMS (OCPP 1.6: Central
System). Both share one RPC engine, and either side can send requests once
connected.

> **Status: foundations (Phase 1 of 8).** The architecture is designed and the
> repository is set up. The protocol engine, the client, the server and the
> message types are not implemented yet, so nothing below is usable. See
> [IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md).

Docs: [Architecture](ARCHITECTURE.md) · [Requirements](REQUIREMENTS.md) ·
[Specifications](SPECIFICATIONS.md) · [Plan](IMPLEMENTATION_PLAN.md) ·
[API](API.md) · [Errors](ERRORS.md) · [Observability](OBSERVABILITY.md) ·
[Security](SECURITY.md) · [Interoperability](INTEROPERABILITY.md) ·
[Compatibility](COMPATIBILITY.md) · [Releases](RELEASE.md)

## Table of contents

- [Protocol versions](#protocol-versions)
- [Install](#install)
- [Design at a glance](#design-at-a-glance)
- [Assurance](#assurance)
- [Development](#development)
- [License](#license)

## Protocol versions

| Version | Priority | Status |
|---|---|---|
| OCPP 1.6J, with the Security Whitepaper (Edition 4) extensions | P0 | designed |
| OCPP 2.0.1 Edition 4, errata 2026-09 | P0 | designed |
| OCPP 2.1 Edition 2, errata 2026-09 | P1 | designed |
| OCPP 1.6 SOAP, OCPP 1.5 and older | — | out of scope |

## Install

```bash
go get github.com/otfabric/go-ocpp
```

Requires Go 1.26 or later. The module has no dependencies yet. From Phase 2
on it depends on [coder/websocket](https://github.com/coder/websocket), and
on nothing else ([ADR 0002](docs/adr/0002-websocket-transport.md)).

## Design at a glance

This is the target API from [ARCHITECTURE.md](ARCHITECTURE.md). It is not
implemented yet.

```go
// Charging Station
c, err := client.New("wss://csms.example.com/ocpp/STATION001",
    client.WithVersions(ocpp.V201),
    client.WithHandler(ocpp.V201, ocpp201.ChargingStation(handler)),
    client.WithBasicAuth("STATION001", password),
)
err = c.Connect(ctx)
resp, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{...})

// CSMS
srv, err := server.New(
    server.WithHandler(ocpp.V16, ocpp16.CentralSystem(h16)),
    server.WithHandler(ocpp.V201, ocpp201.CSMS(h201)),
    server.WithAuthenticator(auth),
)
http.Handle("/ocpp/", srv)
station, ok := srv.Station("STATION001")
```

go-ocpp is a protocol library. It transports, validates and dispatches
messages. Charging behaviour, authorization policy, persistence and billing
belong to the application's handlers.

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
