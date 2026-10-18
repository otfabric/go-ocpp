# Observability

Not implemented yet (Phase 2). The design is in
[ARCHITECTURE.md](ARCHITECTURE.md#observability) and
[ADR 0005](docs/adr/0005-observability.md):

- logging with `log/slog`, silent unless a logger is supplied;
- metrics through the `ocpp.Metrics` callback interface, with no metrics
  library;
- call and handler interceptors for tracing, with no OpenTelemetry
  dependency;
- a frame observer for tools such as ocppctl.

Credentials and authorization tokens are never logged.
