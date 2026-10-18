# security-profiles: OCPP security profiles 1, 2 and 3

One OCPP 2.0.1 CSMS that accepts all three security profiles, with stations
connecting with each, connections that are refused, and a server
certificate rotated while the CSMS runs.

| Profile | Transport | Station authenticates with |
|---|---|---|
| 1 | plain WebSocket (`ws://`) | HTTP Basic: identity and password |
| 2 | TLS (`wss://`) | HTTP Basic: identity and password |
| 3 | TLS (`wss://`) | a client certificate (mutual TLS) |

## What it shows

- A throwaway certificate authority generated at start-up, with a server
  certificate for `127.0.0.1` and client certificates whose CommonName is
  the station identity. No keys are stored or committed.
- `server.WithTLS` with `ClientAuth: tls.VerifyClientCertIfGiven` and
  `ClientCAs`: profile 3 stations present a certificate, profile 2
  stations do not, and the same TLS listener takes both.
- One `server.Server` on two listeners: TLS through `Serve`, and plain
  WebSocket for profile 1 by mounting the server in an `http.Server`
  without TLS. Both share the handler and the sessions.
- An authenticator (`server.AuthenticatorFunc`) that:
  - knows which profile each station is registered for and refuses
    anything else, so that a profile 2 station cannot be downgraded to
    plain WebSocket;
  - for profiles 1 and 2 requires the user name to equal the identity and
    compares the password in constant time (as `server.BasicAuth` does);
  - for profile 3 requires a verified client certificate whose CommonName
    equals the identity;
  - returns `server.ErrUnknownStation` for an unknown identity (HTTP 404)
    and `server.ErrUnauthorized` otherwise (HTTP 401).
- Refused connections: a wrong password, the certificate of another
  station, a profile 2 station over plain WebSocket, an unknown station.
  `Connect` returns an error wrapping `ocpp.ErrConnectFailed` with the HTTP
  status.
- Certificate rotation: `tls.Config.GetCertificate` reads the current
  certificate on every handshake, so storing a new one takes effect for
  the next connection without a restart.
- Passwords never print: `server.ConnectRequest.Password` is a
  `server.Secret`, which formats as `[redacted]`.

## OCPP requirements

- OCPP 2.0.1 Part 2, A00 (security profiles); the same in OCPP 2.1, and
  the OCPP 1.6 Security Whitepaper for 1.6J.
- 2.x Part 4 §3.2 and OCPP-J 1.6 §3.1.2: HTTP 404 for an identity the CSMS
  does not know.
- Issuing and renewing station certificates (SignCertificate,
  CertificateSigned; 2.0.1 Part 2 A02 and A03) is not shown: here the
  certificates come from the example CA directly.

## Run it

```bash
go run ./examples/security-profiles
```

## To production

- Use certificates from your PKI: a CSMS certificate from a CA the
  stations trust, and station certificates issued by your CSMS CA, with
  the organisation and identity your PKI policy requires. Rotate before
  expiry; `GetCertificate` lets you reload from disk or a secret store.
- Keep the profile per station in your station database, and only raise
  it (profile 1 to 2 to 3), never lower it, when a station is migrated.
- Keep profile 1 for closed networks only. Prefer profile 2 or 3 anywhere
  else.
- Check certificate revocation (OCSP or CRLs) in
  `tls.Config.VerifyPeerCertificate` or in the authenticator, if your PKI
  requires it.
- Store passwords hashed (or in a secret store) and set them per station
  with SetVariables (`SecurityCtrlr.BasicAuthPassword`, 2.x) or
  ChangeConfiguration (`AuthorizationKey`, 1.6 Security Whitepaper).
