# Compatibility

## Feature coverage

Generated from the coverage manifests (`ocpp16/coverage.json` and the
others) once they exist. Until then:

| Version | Actions | Typed | Validated | Dispatchable | Round-trip tested | Interop tested |
|---|---|---|---|---|---|---|
| 1.6J (+ security) | 39 | 0 | 0 | 0 | 0 | 0 |
| 2.0.1 | 64 | 0 | 0 | 0 | 0 | 0 |
| 2.1 | 91 | 0 | 0 | 0 | 0 | 0 |

## Comparison with other implementations

Filled in only with what each project documents or what our tests establish.
Cells we have not verified are marked "unverified".

| | go-ocpp | lorenzodonini/ocpp-go | mobilityhouse/ocpp | ChargeTimeEU/Java-OCA-OCPP |
|---|---|---|---|---|
| Language | Go | Go | Python | Java |
| License | MIT | MIT | MIT | MIT |
| OCPP versions (documented) | 1.6J, 2.0.1, 2.1 (planned) | 1.6J, 2.0.1 | 1.6, 2.0.1 (2.1 in code since 2.1.0) | 1.6 (JSON, SOAP), 2.0.1, 2.1 |
| Charging station role | yes (raw payloads; typed from Phase 3) | yes | yes | yes |
| CSMS role | yes (raw payloads; typed from Phase 3) | yes | yes | yes |
| Schema validation | planned | unverified | yes (JSON schema) | unverified |
| Security profiles | 1, 2, 3 (tested against itself) | unverified | unverified | unverified |
| Reconnection | yes, backoff with jitter | unverified | unverified | unverified |
| Logging | slog | unverified | unverified | unverified |
| Metrics | callbacks | unverified | unverified | unverified |
| Tested interoperability | 1.6J, both roles, against mobilityhouse and chargetime (INTEROPERABILITY.md) | — | — | — |
