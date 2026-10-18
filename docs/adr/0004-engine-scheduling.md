# ADR 0004: One engine per connection, one outstanding CALL per direction

Status: Accepted (2026-10-18)

## Context

OCPP-J allows at most one outstanding CALL per direction (T6). Applications
nonetheless call from many goroutines: a meter-value loop, a status change
and a heartbeat at the same time. Handlers also send CALLs. A CSMS commonly
sends GetConfiguration or GetVariables while handling BootNotification.

## Decision

- Each connection has one `engine.Endpoint`, with one reader goroutine, one
  writer goroutine and one dispatcher goroutine.
- Outgoing CALLs take a single slot. Other callers wait in a bounded FIFO
  queue (default 64), which fails fast with `ErrBusy` when full. Waiting
  respects the caller's context. The limit is not configurable above one,
  because that would break T6.
- Incoming CALLs are handled sequentially on the dispatcher goroutine. The
  reader never waits for a handler, so a handler can make its own CALLs
  without deadlock.
- SEND (2.1) bypasses the slot (T5).
- Nothing is retried or replayed by the engine.

## Consequences

- Throughput per station is bounded by the round-trip time, as the
  protocol intends. Applications that need more must use fewer, larger
  messages, as OCPP expects.
- A slow handler delays the next incoming CALL, as T6 implies, but does not
  delay responses to the station's own CALLs.
