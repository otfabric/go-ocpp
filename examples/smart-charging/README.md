# smart-charging: charging profiles and composite schedules

A CSMS limits a charging station's power with charging profiles, and asks
the station what limit results. OCPP 2.0.1 by default, OCPP 1.6 with
`-version 1.6`.

## What it shows

- `SetChargingProfile` from the CSMS:
  - a `ChargingStationMaxProfile` (1.6: `ChargePointMaxProfile`) on EVSE 0
    (1.6: connector 0), the whole station, in W: the grid connection allows
    15 kW;
  - a `TxDefaultProfile` in A, with three periods: 32 A, 10 A during an
    evening peak, 32 A again;
  - a `TxProfile` without a transaction, which the station rejects (2.0.1
    with `statusInfo.reasonCode` `TxNotFound`).
- A station handler that stores profiles in memory, replacing a profile
  with the same id.
- `GetCompositeSchedule`: the station computes the composite schedule for
  EVSE 1 (1.6: connector 1) in A.
- `ClearChargingProfile` by purpose, then the composite schedule again, and
  a clear for an unknown profile id (`Unknown`).

The composite schedule is deliberately simple (see `compose` in
[main.go](main.go)): per purpose, the profile with the highest stack level
wins; the result is the lower of the station maximum and the transaction
limit (TxProfile, else TxDefaultProfile), and the hardware limit (32 A)
where no profile applies. W and A are converted at 230 V × 3 phases. A
real station also handles recurring profiles, validity periods, phases,
external limits and the minimum charging rate. This is application logic:
go-ocpp carries and validates the messages, and what a station does with a
profile is the station's.

## OCPP use cases

- OCPP 2.0.1 Part 2, functional block K (smart charging): K01
  (SetChargingProfile), K08 (Get Composite Schedule), K10 (Clear Charging
  Profile).
- OCPP 1.6: §3.13 (Smart Charging), §5.5 ClearChargingProfile, §5.7
  GetCompositeSchedule, §5.16 SetChargingProfile.

## Run it

```bash
go run ./examples/smart-charging
go run ./examples/smart-charging -version 1.6
```

## Security profile

None: plain WebSocket on the loopback interface without authentication
(`server.WithoutAuthentication`), to keep the example about smart charging.
See [security-profiles](../security-profiles) for profiles 1–3.

## To production

- Station side: implement the full composite schedule of the version you
  support, persist profiles across reboots, apply them to the hardware,
  and honour `validFrom`/`validTo` and recurrency. Report the limits you
  support in the device model (SmartChargingCtrlr).
- CSMS side: keep the profiles you sent per station, and send them again
  after a station reboots or reports that it lost them. Choose stack levels
  so that later profiles override earlier ones as you intend.
- 2.0.1 allows up to three charging schedules per profile; this station
  uses the first.
