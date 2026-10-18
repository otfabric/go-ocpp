# Security Policy

## Reporting a Vulnerability

If you find a security vulnerability in `go-ocpp`, please report it
responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, email **security@otfabric.com**, or open a private advisory on
GitHub: on the [Security](https://github.com/otfabric/go-ocpp/security) tab,
choose **Advisories**, then **Report a vulnerability**. Include a
description, the steps to reproduce, and the affected version.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

No release has been made yet.

## Security Considerations

The design is in [ARCHITECTURE.md](ARCHITECTURE.md#security). In summary:

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
  default.

Deployment guidance, including certificate rotation and the limits, will be
written with the implementation (Phases 2 and 3).
