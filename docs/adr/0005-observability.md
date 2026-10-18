# ADR 0005: slog for logging, callbacks for metrics, interceptors for tracing

Status: Accepted (2026-10-18)

## Context

go-iec104 defines its own `Logger` interface with `Debugf`-style methods and
adapters for `log/slog`. Since Go 1.21, `*slog.Logger` with an application
`slog.Handler` covers every adapter that interface exists for. The brief
asks for slog, pluggable metrics with no Prometheus dependency, and tracing
hooks with no OpenTelemetry dependency.

## Decision

- Options take a `*slog.Logger`. A nil logger, the default, discards
  everything. There is no Logger interface of our own.
- `ocpp.Metrics` is a callback interface in go-iec104's style: synchronous,
  must not block, must be safe for concurrent use. `ocpp.NopMetrics` is
  provided for embedding.
- Tracing is provided by call and handler interceptors (`func(ctx, info,
  next) error`), plus a frame observer.

## Consequences

- This departs from go-iec104's Logger interface. The departure is
  deliberate and is noted in API.md once written.
- No observability dependency enters `go.mod`.
