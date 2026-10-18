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

No interop results yet. The suite arrives with the first ocpp-interop release
(Phase 4). Scenarios that an adapter does not support are reported as
skipped, never as passed.
