# remote-management: triggers, firmware, logs and resets

The remote operations a CSMS uses to look after a fleet of OCPP 2.0.1
stations: ask for a message, update the firmware, fetch a log and reset
the station.

## What it shows

- `TriggerMessage`: the station answers `Accepted` and then sends the
  message asked for: StatusNotification (for EVSE 1, connector 1),
  MeterValues (context `Trigger`) and LogStatusNotification (`Idle`: no
  upload in progress). A trigger it does not support is answered
  `NotImplemented`.
- `UpdateFirmware`: the station accepts and reports each stage with
  `FirmwareStatusNotification` and the request id: Downloading,
  Downloaded, Installing, Installed. Its next BootNotification carries the
  new firmware version.
- `GetLog`: the station accepts with the file name, then reports
  `LogStatusNotification` Uploading and Uploaded.
- `Reset OnIdle` during a transaction: answered `Scheduled`; the station
  resets when the transaction ends. `Reset Immediate`: answered `Accepted`,
  reset at once.
- The reboot: the station closes its client, waits, creates a new client,
  connects and sends BootNotification with reason `RemoteReset`. The CSMS
  sees the session end and a new one start (`server.WithSessionHandler`).

Nothing is downloaded or uploaded: timers stand in for the work.

## OCPP use cases

- OCPP 2.0.1 Part 2: F06 (Trigger Message), L01/L02 (firmware update),
  N01 (Retrieve Log Information), B11 (reset without an ongoing
  transaction) and B12 (reset with an ongoing transaction).

## Run it

```bash
go run ./examples/remote-management
```

## Security profile

None: plain WebSocket on the loopback interface without authentication.
See [security-profiles](../security-profiles).

## To production

- Firmware: download from `firmware.location` at `retrieveDateTime`,
  honour `retries` and `retryInterval`, verify the signature with the
  signing certificate (secure firmware update), install at
  `installDateTime`. Firmware that needs a reboot reports
  `InstallRebooting`, reboots, boots with reason `FirmwareUpdate` and then
  reports `Installed`. Report failures (`DownloadFailed`,
  `InvalidSignature`, `InstallationFailed`).
- Logs: upload to `log.remoteLocation` (for example HTTPS PUT or FTP),
  limited to `oldestTimestamp`/`latestTimestamp`, and report
  `UploadFailure` when it fails.
- Reset: end ongoing transactions according to the reset type before
  rebooting, persist what must survive, and start again with the
  BootNotification reason that says why.
- The station sends its follow-up messages with `ocpp.AfterResponse`
  (`later`), which guarantees the response to the CSMS's CALL goes out
  first; the delay in `later` only stands for the work. A real station
  queues long work instead of holding a goroutine per request.
- A `client.Client` that was closed stays closed; the example makes a new
  one per boot, as a restarting station process would.
