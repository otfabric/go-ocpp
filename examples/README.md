# Examples

Each example starts a CSMS and a charging station in one process, runs a
short exchange in both directions, and exits. They use security profile 2:
TLS, with a certificate authority generated at start-up, and HTTP Basic
authentication. Run them with `make examples` or `go run ./examples/ocpp16`.

| Example | Shows |
|---|---|
| [ocpp16](ocpp16/main.go) | OCPP 1.6J: BootNotification and a CSMS that asks for the configuration while handling it, StatusNotification, Authorize, RemoteStartTransaction from the CSMS |
| [ocpp201](ocpp201/main.go) | OCPP 2.0.1 over a compressed connection: BootNotification, the device model with GetVariables, TransactionEvent, Reset from the CSMS |
