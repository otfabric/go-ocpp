# Security Policy

## Reporting a Vulnerability

If you find a security vulnerability in `go-ocpp`, please report it
responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, email **security@otfabric.io**, or open a private advisory on
GitHub: on the [Security](https://github.com/otfabric/go-ocpp/security) tab,
choose **Advisories**, then **Report a vulnerability**. Include a
description, the steps to reproduce, and the affected version.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

Releases are listed in [RELEASE.md](RELEASE.md).

## Security Considerations

Implemented in the client and server (Phase 2); see
[ARCHITECTURE.md](ARCHITECTURE.md#security). In summary:

| Profile | Transport | Authentication |
|---|---|---|
| 1 | `ws://` | HTTP Basic. Needs an explicitly insecure option, and is for closed networks and tests only |
| 2 | `wss://` | HTTP Basic, and the server certificate |
| 3 | `wss://` | Client certificate (mutual TLS) |

- TLS verification cannot be disabled through any go-ocpp option.
- A server needs an authenticator, or an explicitly named option that turns
  authentication off. Authentication runs before the WebSocket upgrade.
- Credentials and authorization tokens are never logged, and never appear in
  error strings or metric labels.
- Frame size, station count, queue lengths and handshake time are bounded by
  default. With compression, the size limit applies after decompression, so
  a small compressed message cannot inflate past it.
- Every incoming payload is validated against its OCA schema before a
  handler sees it; unknown properties are refused where the schema closes
  the object.
- On 2.x connections the server refuses station identities that are not
  identifierStrings of at most 48 characters without ":" (HTTP 400).
- The server refuses WebSocket upgrades whose browser Origin names another
  host (403), unless `WithOriginPatterns` allows it: a web page cannot
  open a station session in a user's browser.
- `WithIncomingRateLimit` bounds how fast one station's CALLs reach the
  handlers.
- `WithWriteTimeout` (default 10 s) bounds a peer that stops reading;
  `WithPingInterval` detects half-open connections; `WithMaxBadMessages`
  closes a peer that keeps sending malformed frames.
- Hardening tests cover slow handshakes, station floods, oversized
  messages, compression bombs, call floods, slow readers, half-open links
  and TLS refusals (expired, foreign-CA and missing client certificates,
  TLS below 1.2); the handshake, the engine and every dispatcher are
  fuzzed nightly.

### Deployment

- Use profile 2 or 3. Profile 1 sends the password in clear text.
- With `server.BasicAuth`, the user name must equal the station identity in
  the URL, and passwords are compared in constant time. For profile 3, check
  in the authenticator that the client certificate belongs to the identity
  in the URL (`ConnectRequest.TLS.PeerCertificates`); the TLS handshake only
  proves that the certificate is trusted.
- When the server is mounted in an application's `http.Server`, that server
  does the TLS: set its `TLSConfig` (`ClientAuth`, `ClientCAs` for profile 3)
  and `ReadHeaderTimeout`.
- Certificate rotation: supply a `tls.Config` with `GetCertificate`
  (server), `GetClientCertificate` (client) or `GetConfigForClient`. The
  library keeps no certificates of its own.
- Limits and their defaults: 10 000 stations, 64 KiB per message, 64 queued
  calls per connection, 30 s response timeout, 10 s for request headers on
  the server's own listener. Refusals are HTTP errors before the upgrade.
- An identity the authenticator does not know (`server.ErrUnknownStation`,
  which `server.BasicAuth` returns) is refused with HTTP 404, as OCPP-J
  recommends; wrong credentials with 401.
- Session replacement (`ReplaceExisting`, the default) happens only after
  authentication succeeds, so an unauthenticated client cannot evict a
  station.
- Origin checks: browsers send an `Origin` header; a WebSocket upgrade whose
  origin differs from the host is refused (coder/websocket's default).
  Stations do not send one.
