# Compatibility

## Feature coverage

From the coverage manifests (`ocpp16/coverage.json`, `ocpp201/coverage.json`,
`ocpp21/coverage.json`),
which a test keeps in step with the schemas. "Round-trip tested" means the
conformance suite (`internal/schematest`) checks the request and the
response against the schema and an independent validator.

| Version | Actions | Typed | Validated | Dispatchable | Round-trip tested | Interop tested |
|---|---|---|---|---|---|---|
| 1.6J (+ security) | 39 | 39 | 39 | 39 | 39 | see INTEROPERABILITY.md |
| 2.0.1 | 64 | 64 | 64 | 64 | 64 | see INTEROPERABILITY.md |
| 2.1 | 91 | 91 | 91 | 91 | 91 (90 responses: NotifyPeriodicEventStream is a SEND) | not yet |

## Per-version matrices against the reference stacks

One matrix per version lists every action, what go-ocpp does with it, what
each reference stack's station and CSMS declare for it, and the interop
scenarios that exchange it. They are generated from the reference images
(`make compat`):

- [OCPP 1.6J](docs/compatibility/ocpp16.md): 39 actions; 30 exchanged with a reference
- [OCPP 2.0.1](docs/compatibility/ocpp201.md): 64 actions; 27 exchanged with a reference
- [OCPP 2.1](docs/compatibility/ocpp21.md): 91 actions; 43 exchanged with Mobility House, the only reference with 2.1

The actions without a scenario are verified against go-ocpp itself (every
action, both directions, `TestMatrix`) and the OCA schemas, not yet against
an independent implementation; FEATURES.md tracks extending the reference
adapters to them.

## Comparison with the reference stacks

From each project's documentation and source (sources in FEATURES.md) and
from our tests. "unverified" means we could not establish it.

| | go-ocpp | lorenzodonini/ocpp-go v0.19.0 | mobilityhouse/ocpp 2.1.0 | ChargeTimeEU/Java-OCA-OCPP v2.0 |
|---|---|---|---|---|
| Language | Go | Go | Python | Java |
| License | MIT | MIT | MIT | MIT |
| OCPP versions | 1.6J (+ Security Whitepaper), 2.0.1, 2.1 | 1.6J, 2.0.1 | 1.6, 2.0.1; 2.1 types (experimental) | 1.6 (JSON, SOAP), 2.0.1; 2.1 on master only |
| SEND and CALLRESULTERROR (2.1) | yes | no | no | master only |
| Charging station role | yes | yes | yes | yes |
| CSMS role | yes | yes | yes | yes |
| Several versions on one endpoint | yes | unverified | partial (application chooses) | yes (MultiProtocolJSONServer) |
| Schema validation | generated from the OCA schemas, always on, both directions, checked against an independent validator | struct tags, not the OCA schemas; can be turned off globally | OCA JSON schemas; can be skipped per call | hand-written `validate()` |
| Security profiles | 1, 2, 3 | 1, 2, 3 | left to the WebSocket library | TLS and Basic |
| WebSocket compression | yes | unverified | left to the WebSocket library | yes |
| Ping/pong keepalive | yes (`WithPingInterval`) | yes | left to the WebSocket library | yes |
| Reconnection | yes, the 2.x back-off, changeable at runtime | yes (RetryBackOff) | left to the application | unverified |
| Offline queue of transaction messages | no (application; FEATURES.md #13) | in-memory, no retry | no | in-memory |
| Interceptors, frame observer | yes | no (invalid-message hook) | `@after` hooks | no |
| Logging | slog | logger interface | Python logging | slf4j |
| Metrics | callbacks (`ocpp.Metrics`) | no | no | no |
| Limits (stations, size, queues, bad messages, rate) | yes | partial | no | partial (frame size) |
| Test kit | `ocpptest` | mocks | partial | test modules |
| Tested interoperability | against all three, both roles, 1.6J, 2.0.1, 2.1 (INTEROPERABILITY.md) | — | — | — |

The comparison with more implementations, and the gaps go-ocpp tracks, are
in [FEATURES.md](FEATURES.md).

## API stability

go-ocpp follows semantic versioning. Before v1.0.0, a minor release may
change the API; every change is listed in RELEASE.md, and `TestAPI` makes
each one visible in review (`testdata/api`). From v1.0.0:

- A patch release fixes bugs and follows the specifications more closely;
  it does not change the API. A fix that refuses messages a release
  accepted, because they violate a schema or a SHALL, is a fix.
- A minor release adds: options, methods, sentinel errors, `Metrics`
  callbacks (embed `ocpp.NopMetrics` so that an implementation keeps
  compiling), and handler methods when the OCA publishes new actions (embed
  the `Unimplemented…Handler`).
- A major release is needed for anything else.
- Generated version packages follow the OCA schemas: an erratum that
  changes a schema changes the types, in a minor release, with the change
  listed.
