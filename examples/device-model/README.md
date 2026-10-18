# device-model: reading and configuring a station's device model

OCPP 2.x describes a charging station as components (OCPPCommCtrlr,
SecurityCtrlr, EVSE, ...) with variables and attributes. A CSMS reads the
whole model with a base report, and reads and writes single variables.
OCPP 2.0.1 by default, OCPP 2.1 with `-version 2.1`.

## What it shows

- A station handler with an in-memory device model: ten variables on
  standard components, read-only and read-write, of type integer,
  boolean, decimal, string and option list, one on EVSE 1.
- `GetBaseReport` (FullInventory): the station answers `Accepted` and then
  sends the report in `NotifyReport` messages. It splits the report by its
  own `DeviceDataCtrlr.ItemsPerMessage[GetReport]` (4 here), with `seqNo`
  counting up and `tbc` ("to be continued") true on all but the last. The
  CSMS waits for the last part. ConfigurationInventory returns only the
  writable variables; SummaryInventory is answered `NotSupported`.
- `GetVariables`: values, `UnknownVariable` and `UnknownComponent`.
- `SetVariables`: a read-write variable is accepted, a read-only one is
  `Rejected` with reason code `ReadOnly`, and an invalid boolean is
  `Rejected` with `InvalidValue`. Reading again shows the new value.
- `SetVariableMonitoring`: an upper threshold on `EVSE[1].Power` is
  accepted with a monitor id; a monitor on a variable that does not support
  monitoring is rejected.

The device model is application data: which components and variables a
station has, and what it does when one changes, is the station's. go-ocpp
carries and validates the messages.

## OCPP use cases

- OCPP 2.0.1 Part 2: B05 (Set Variables), B06 (Get Variables), B07 (Get
  Base Report), N04 (Set Variable Monitoring). OCPP 2.1 has the same use
  cases.

## Run it

```bash
go run ./examples/device-model
go run ./examples/device-model -version 2.1
```

## Security profile

None: plain WebSocket on the loopback interface without authentication.
See [security-profiles](../security-profiles).

## To production

- Station side: the device model is part of the firmware. Persist
  read-write values, apply them (a new HeartbeatInterval changes the
  heartbeat timer), validate them against the variable's characteristics
  (data type, limits, values list), and answer `RebootRequired` where a
  change takes effect only after a reboot.
- Report all attributes a variable has (Actual, Target, MinSet, MaxSet)
  and support GetReport with component and variable criteria.
- The station sends NotifyReport after its response with
  `ocpp.AfterResponse`, which guarantees the GetBaseReport response goes
  out first. A real station would also bound how many reports it builds at
  once.
- CSMS side: store the reported model per station and use it to decide
  what to configure; expect reports to arrive in several parts and to be
  interleaved with other messages.
