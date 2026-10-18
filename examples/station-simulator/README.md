# station-simulator: a charging station for testing a CSMS

A charging station simulator for OCPP 1.6, 2.0.1 or 2.1. Point it at a CSMS
to see how the CSMS handles a station that boots, reports its status, sends
heartbeats and runs charging sessions.

## What it shows

- A station built from the typed call functions (`ocpp16.BootNotification`,
  `ocpp201.TransactionEvent`, ...) behind a small per-version interface, so
  the scenario is written once.
- Boot until accepted: `Pending` and `Rejected` mean "try again after the
  interval".
- Heartbeat at the interval from the BootNotification response, in its own
  goroutine.
- The charging session: Authorize, StatusNotification Occupied (1.6:
  Charging), start a transaction, meter values with an increasing
  `Energy.Active.Import.Register`, stop the transaction, StatusNotification
  Available.
- In 2.x the transaction's meter values go in `TransactionEvent` (Updated);
  `MeterValues` is for values outside a transaction. 1.6 uses `MeterValues`
  with the transaction id.
- Handlers for the CSMS's RemoteStartTransaction (1.6) or
  RequestStartTransaction (2.x) and Reset. A 2.x Reset OnIdle during a
  transaction is answered `Scheduled`.
- `client.WithReconnect(client.DefaultReconnect())`: the station redials
  after losing the connection, with the back-off of 2.x Part 4 §5.4.
  Nothing is resent; a call that was pending fails, and the simulator stops.
- `client.WithStateHandler` to report connection changes.

## OCPP use cases

- OCPP 1.6: §4.1 Authorize, §4.2 BootNotification, §4.6 Heartbeat, §4.7
  MeterValues, §4.8 StartTransaction, §4.9 StatusNotification, §4.10
  StopTransaction, §5.11 RemoteStartTransaction, §5.14 Reset.
- OCPP 2.0.1 Part 2: B01–B03 (boot accepted, pending, rejected), B11–B12
  (reset), C01 (authorization), E01–E07 (transactions), F02 (remote start),
  G01–G02 (status and heartbeat), J02 (transaction-related meter values).
  OCPP 2.1 has the same use cases.

## Run it

```bash
go run ./examples/station-simulator                  # OCPP 2.0.1, embedded CSMS
go run ./examples/station-simulator -version 1.6
go run ./examples/station-simulator -version 2.1 -sessions 3
```

Without `-url` it starts an embedded go-ocpp CSMS that accepts everything,
asks for one remote start once the station's own sessions are done, then a
reset, so the run ends by itself. The heartbeat interval from the CSMS (300
s) is capped to 1 s so that a heartbeat shows.

Against your own CSMS, or [csms](../csms) with `-listen`:

```bash
go run ./examples/station-simulator -version 2.0.1 \
    -url ws://127.0.0.1:8887/ocpp/CS201-0001 \
    -password cs201-example-password-001 -sessions 2 -linger 30s
```

| Flag | Default | Meaning |
|---|---|---|
| `-version` | `2.0.1` | `1.6`, `2.0.1` or `2.1` |
| `-url` | embedded CSMS | CSMS URL; the last path segment is the station identity |
| `-password` | none | HTTP Basic password; the user name is the identity |
| `-sessions` | 1 | Charging sessions to run |
| `-linger` | 0 | With `-url`: stay connected this long for a remote start or reset |

It exits 0 after the scenario, or with an error message when the CSMS
refuses the connection (HTTP 401 or 404) or a call fails.

## Security profile

`ws://` URLs: security profile 1 (Basic authentication over plain
WebSocket) when `-password` is set, none otherwise. `wss://` URLs: TLS with
the system trust roots, profile 2 with `-password`. The embedded CSMS uses
no authentication. Profile 3 (client certificates) needs `client.WithTLS`
with a certificate; see [security-profiles](../security-profiles).

## To production

This is a test tool, not station firmware. What a real station adds:

- A transaction queue that survives connection loss and restarts, and
  resends what was not acknowledged (the library resends nothing).
- Persistent transaction ids and sequence numbers, and a real meter.
- The device model (HeartbeatInterval, retry back-off, ...) read and set by
  the CSMS: see [device-model](../device-model).
- A real reset: close the connection, reboot and boot with reason
  `RemoteReset`: see [remote-management](../remote-management).
