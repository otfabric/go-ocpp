# csms: a multi-version CSMS

A small Charging Station Management System that serves OCPP 1.6, 2.0.1 and
2.1 on one endpoint. One `server.Server` has a handler per version
(`server.WithHandler`), and each station's `Sec-WebSocket-Protocol` decides
which one it talks to.

## What it shows

- One server for three versions: `ocpp16.CentralSystem`, `ocpp201.CSMS` and
  `ocpp21.CSMS` side by side.
- HTTP Basic authentication with a per-station password table
  (`server.BasicAuth`). An unknown identity gets HTTP 404, a wrong password
  HTTP 401.
- Session hooks (`server.WithSessionHandler`) that keep an in-memory station
  registry up to date. `server.WithPingInterval` detects dead connections.
- BootNotification acceptance, Heartbeat and StatusNotification.
- Authorize against an id-token allow-list (`Invalid` in 1.6, `Unknown` in
  2.x for a token the CSMS does not know).
- Transaction tracking:
  - 1.6: StartTransaction (the CSMS assigns the transaction id),
    MeterValues and StopTransaction;
  - 2.0.1 and 2.1: TransactionEvent Started, Updated (with meter values) and
    Ended (the station assigns the transaction id).
- Remote start and stop from the CSMS: RemoteStartTransaction and
  RemoteStopTransaction in 1.6, RequestStartTransaction and
  RequestStopTransaction in 2.x. `server.Station(id)` finds the session to
  send to, and `Session.Version()` picks the message set.

The in-process stations are minimal, just enough to drive the CSMS.
[station-simulator](../station-simulator) is a fuller station.

## OCPP use cases

- OCPP 1.6: §4.1 Authorize, §4.2 BootNotification, §4.6 Heartbeat, §4.7
  MeterValues, §4.8 StartTransaction, §4.9 StatusNotification, §4.10
  StopTransaction, §5.11 RemoteStartTransaction, §5.12
  RemoteStopTransaction.
- OCPP 2.0.1 Part 2: B01 (cold boot), C01 (authorization with RFID), E01–E07
  (starting and stopping transactions), F01–F03 (remote start and stop),
  G01 (status notification), G02 (heartbeat), J02 (transaction-related meter
  values). OCPP 2.1 has the same use cases.
- Connection: OCPP-J 1.6 §3 and 2.x Part 4 §3 (subprotocol selection, the
  identity in the URL, HTTP 404 for an unknown station).

## Run it

```bash
go run ./examples/csms
```

starts the CSMS on `127.0.0.1` with a random port, connects one station of
each version, runs a local and a remote transaction on each, prints a
summary of stations and transactions, and exits.

To keep it running as a CSMS for real stations or the simulator:

```bash
go run ./examples/csms -listen 127.0.0.1:8887
go run ./examples/station-simulator -version 2.0.1 \
    -url ws://127.0.0.1:8887/ocpp/CS201-0001 -password cs201-example-password-001
```

Stop it with Ctrl-C; it prints the summary on the way out. The password
table has `CS16-0001`, `CS201-0001` and `CS21-0001`; `-password secret`
also admits any other identity with that password (for development only).

## Security profile

Security profile 1: HTTP Basic authentication over plain WebSocket. The
station side needs `client.WithInsecureTransport()` to use `ws://`. Profile 1
sends the password in the clear and belongs on loopback or a trusted
network only. Production uses profile 2 (TLS with Basic authentication) or
profile 3 (TLS with client certificates): see
[security-profiles](../security-profiles).

## To production

What is simplified here:

- The registry, the password table and the allow-list are in memory, with
  no persistence. A real CSMS keeps them in a database, stores passwords
  hashed or in a secret store, and survives restarts.
- A 1.6 transaction id comes from a counter; a real CSMS needs ids that are
  unique across restarts.
- Authorization accepts or refuses a token; a real CSMS also answers with
  expiry, group tokens and personal messages, and handles `ConcurrentTx`.
- Meter values are reduced to the energy register in Wh.
- Remote start does not reserve the connector or wait for the station's
  `TransactionEvent` with the `remoteStartId`.

What to change:

- Serve TLS (`server.WithTLS`, or mount the server behind your own
  `http.Server` with TLS) and drop `WithInsecureTransport` on stations.
- Return a `BootNotification` interval that suits your fleet, and use
  `Pending` while provisioning a new station.
- Add `server.WithLogger` and `server.WithMetrics` (see
  [observability](../observability)).
