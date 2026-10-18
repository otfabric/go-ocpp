# Architecture decision records

One file per decision that is hard to reverse, numbered in order. A record
is not edited after it is accepted, except to set its status. A later record
supersedes it instead.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-package-layout.md) | Package layout and version package names | Accepted |
| [0002](0002-websocket-transport.md) | WebSocket transport: coder/websocket behind an internal interface | Accepted |
| [0003](0003-schema-generation.md) | Message types and validation generated from the OCA schemas | Accepted |
| [0004](0004-engine-scheduling.md) | One engine per connection, one outstanding CALL per direction | Accepted |
| [0005](0005-observability.md) | slog for logging, callbacks for metrics, interceptors for tracing | Accepted |
| [0006](0006-go-version-and-ci.md) | Minimum Go version and CI | Accepted |
| [0007](0007-schema-licensing.md) | Licensing of generated code and vendored schemas | Accepted |
