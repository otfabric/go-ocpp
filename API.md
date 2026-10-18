# API Reference: otfabric/go-ocpp

The reference is written as each package is implemented. Until then the
design of the public API is in
[ARCHITECTURE.md](ARCHITECTURE.md#messages-handlers-and-calls).

## Package ocpp

### Versions

```go
type Version uint8

const (
    V16  Version = iota + 1 // "1.6",   subprotocol "ocpp1.6"
    V201                    // "2.0.1", subprotocol "ocpp2.0.1"
    V21                     // "2.1",   subprotocol "ocpp2.1"
)

func Versions() []Version
func (v Version) Valid() bool
func (v Version) String() string
func (v Version) Subprotocol() string
func ParseVersion(s string) (Version, bool)
func ParseSubprotocol(s string) (Version, bool)
```

`ParseSubprotocol` compares exactly, because WebSocket subprotocol tokens are
case-sensitive.

### Errors

See [ERRORS.md](ERRORS.md).

## Departures from other OTFabric libraries

- Logging takes a `*slog.Logger` instead of a library-specific `Logger`
  interface ([ADR 0005](docs/adr/0005-observability.md)).
- Version packages are named `ocpp16`, `ocpp201` and `ocpp21`
  ([ADR 0001](docs/adr/0001-package-layout.md)).
