# vendor-extensions: DataTransfer, customData and raw calls

The ways OCPP leaves room for vendor-specific data, on OCPP 1.6, 2.0.1 and
2.1, with one CSMS and a station per version.

## What it shows

- **DataTransfer** in both directions, with `vendorId` (by convention a
  reversed DNS name) and `messageId`:
  - station to CSMS: `GetTariff` is answered `Accepted` with data; an
    unknown message id gets `UnknownMessageId`, an unknown vendor
    `UnknownVendorId`;
  - CSMS to station: `SetLedColor`.
  - In 1.6 `data` is a string (JSON inside it is a convention); in 2.x it
    is any JSON value (`json.RawMessage`).
- **customData** (2.x): every 2.x object has an optional `customData` with
  a `vendorId` and vendor properties. `ocpp201.CustomData{VendorID, Fields}`
  keeps the properties as raw JSON. The station sends it on
  BootNotification (2.0.1) and StatusNotification (2.1); the CSMS reads it
  and sends customData back in its BootNotification response.
- **A raw call**: an action go-ocpp does not model (`VendorStatus`), sent
  with `json.RawMessage` through `Call` (`ocpp.Caller`; a `*client.Client`
  or a `*server.Session`). The 2.0.1 CSMS handles it in a dispatcher that
  sees the action first (`ocpp.DispatcherFunc`) and passes everything else
  to the generated one. The 1.6 CSMS has no such dispatcher and answers
  `NotImplemented`, returned as a `*ocpp.CallError`.

## Validation still applies

Extensions do not switch validation off for standard messages:

- a typed call validates before sending: a 1.6 `messageId` of 51
  characters (at most 50) and a 2.0.1 `customData.vendorId` of 256
  characters (at most 255) are refused with an `*ocpp.ValidationError`, and
  nothing is sent;
- the receiver validates every standard message, also one built by hand: a
  raw `Heartbeat` with an unknown property is answered with a CALLERROR
  `FormatViolation`.

A raw call of an action that is not standard is not validated by go-ocpp:
both sides own its payload.

## OCPP use cases

- OCPP 1.6: §4.3 DataTransfer (Charge Point to Central System), §5.6
  DataTransfer (Central System to Charge Point).
- OCPP 2.0.1 Part 2: P01 (Data Transfer to the Charging Station), P02
  (Data Transfer to the CSMS). OCPP 2.1 has the same use cases.

## Run it

```bash
go run ./examples/vendor-extensions
```

## Security profile

None: plain WebSocket on the loopback interface without authentication.
See [security-profiles](../security-profiles).

## To production

- Prefer standard messages, then customData on a standard message, then
  DataTransfer. A private action (the raw call) breaks interoperability:
  other CSMSs and stations answer `NotImplemented`. Keep raw calls for
  closed deployments, or for an action of a newer version or draft that the
  library does not model yet.
- Document each vendorId and messageId and the JSON of its data; version
  that JSON yourself.
- Answer `UnknownVendorId` and `UnknownMessageId` instead of a CALLERROR
  for DataTransfer you do not support.
- Wrapping a dispatcher in `ocpp.DispatcherFunc` hides any other interface
  it implements: an `ocpp21` dispatcher's `DispatchSend` (for SEND
  messages) would need a wrapper type that implements
  `ocpp.SendDispatcher` too.
