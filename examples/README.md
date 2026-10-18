# Examples

Each example is one `main.go` that starts a CSMS and one or more charging
stations in the same process, on `127.0.0.1` with a random port, runs its
exchange in both directions, prints a trace and exits. Run one with
`go run ./examples/<name>`; `make examples` runs the self-contained set.

The examples use only the public API and the standard library. The
security profile differs per example and is listed below: the quick starts
show TLS, the use cases mostly keep to plain WebSocket on loopback, and
[security-profiles](security-profiles) shows all three profiles.

## Quick starts

| Example | OCPP versions | Use case | Shows | Security |
|---|---|---|---|---|
| [ocpp16](ocpp16/main.go) | 1.6 | A first Central System and Charge Point | BootNotification and a CSMS that asks for the configuration while handling it, StatusNotification, Authorize, RemoteStartTransaction from the CSMS | Profile 2: TLS and Basic authentication |
| [ocpp201](ocpp201/main.go) | 2.0.1 | A first CSMS and Charging Station | A compressed connection, BootNotification, GetVariables on a small device model, TransactionEvent, Reset from the CSMS | Profile 2: TLS and Basic authentication |
| [ocpp21](ocpp21/main.go) | 2.1 (2.0.1 offered as fallback) | Version negotiation and SEND | The station offers 2.1 and 2.0.1 and the CSMS picks 2.1; OpenPeriodicEventStream and NotifyPeriodicEventStream as SEND messages, which are never answered | None: plain WebSocket, no authentication |
| [observability](observability/main.go) | 2.0.1 | Logs, metrics and tracing hooks | A JSON `slog` logger, an `ocpp.Metrics` that counts, call and handler interceptors that time every CALL, a frame observer | None: plain WebSocket, no authentication |

## Use cases

| Example | OCPP versions | Use case | Shows | Security |
|---|---|---|---|---|
| [csms](csms) | 1.6, 2.0.1, 2.1 | A small CSMS for all three versions | One server with a handler per version, Basic authentication with a per-station password table, a station registry kept by session hooks, Authorize against an allow-list, transactions (1.6 Start/Stop/MeterValues, 2.x TransactionEvent), remote start and stop; `-listen` keeps it running for real stations | Profile 1: Basic authentication over plain WebSocket, loopback only |
| [station-simulator](station-simulator) | 1.6, 2.0.1 or 2.1 | Testing a CSMS with a simulated station | Boot until accepted, heartbeats at the CSMS's interval, charging sessions with Authorize, transaction and increasing energy meter values, remote start and reset handlers, automatic reconnection; `-url` points it at any CSMS | None with the embedded CSMS; profile 1 or 2 against your CSMS with `-password` |
| [smart-charging](smart-charging) | 2.0.1 or 1.6 | Limiting a station's power | SetChargingProfile (station maximum in W, TxDefaultProfile in A, a rejected TxProfile), a station that stores profiles and computes a simple composite schedule, GetCompositeSchedule, ClearChargingProfile | None: plain WebSocket, no authentication |
| [security-profiles](security-profiles) | 2.0.1 | Securing connections | Profiles 1, 2 and 3 on one server with two listeners, a CA generated at start-up, an authenticator for passwords and client certificates, refused connections, server certificate rotation | Profiles 1, 2 and 3 |
| [device-model](device-model) | 2.0.1 or 2.1 | Reading and configuring a station | An in-memory device model, GetBaseReport with NotifyReport split by `tbc`, GetVariables, SetVariables with a rejected read-only variable, SetVariableMonitoring | None: plain WebSocket, no authentication |
| [remote-management](remote-management) | 2.0.1 | Operating a fleet remotely | TriggerMessage and the triggered messages, UpdateFirmware with FirmwareStatusNotification, GetLog with LogStatusNotification, Reset OnIdle (Scheduled during a transaction) and Immediate with a station that reboots and boots with reason RemoteReset | None: plain WebSocket, no authentication |
| [vendor-extensions](vendor-extensions) | 1.6, 2.0.1, 2.1 | Vendor-specific data | DataTransfer in both directions with vendorId and messageId, 2.x customData on standard messages, a raw call of an action that is not modelled, and validation that still applies to standard messages | None: plain WebSocket, no authentication |

Each use case has a README with the OCPP use cases it follows, how to run
it, and what to change for production. All of them keep their state in
memory: a real CSMS or station persists it.
