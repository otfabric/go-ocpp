# ADR 0002: WebSocket transport

Status: Accepted (2026-10-18)

## Context

Other OTFabric libraries have no dependencies outside the standard library.
go-openadr carries its own minimal RFC 6455 implementation for this reason.
That implementation has no subprotocols and no extensions.

OCPP needs from a WebSocket:

- subprotocol negotiation (T1);
- text messages with a size limit;
- ping and pong;
- close codes;
- TLS through `net/http`;
- `permessage-deflate` (RFC 7692), which 2.x requires of a CSMS (T8,
  still to be confirmed).

Compression is the expensive part to implement and to get right. It
involves context takeover, window bits negotiation and decompression bombs.

The brief asks to evaluate github.com/coder/websocket (ISC licence; v1.8.15,
2026-06-15) and not to write a WebSocket stack without a documented
standards reason.

## Decision

- Use `github.com/coder/websocket` for the transport, as the single
  production dependency.
- Wrap it in `internal/transport` behind a small interface: read a message,
  write a message, close with a code, and the negotiated subprotocol and
  extensions. The engine and its tests use an in-memory implementation of
  the same interface.
- Add the dependency in Phase 2, with the first code that uses it. Phase 1
  has no dependencies.

## Consequences

- `go.mod` has one requirement. `govulncheck` and the release checklist
  cover it.
- The transport could be replaced by an in-house implementation later
  without any change to the public API.
- Compression is tested in Phase 5 against the reference implementations,
  including decompressed-size limits.
