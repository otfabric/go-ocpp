# OCA JSON schemas

The official OCPP JSON schemas, unmodified, as the Open Charge Alliance
publishes them (licence: [LICENSE](LICENSE), CC BY-ND 4.0;
[ADR 0007](../docs/adr/0007-schema-licensing.md)). `internal/gen` generates
the message types and validation of the version packages from them, and CI
checks that the generated code is up to date.

| Directory | Files | Schema draft | Source archive (openchargealliance.org) | Archive SHA-256 |
|---|---|---|---|---|
| `ocpp16/core` | 56 | draft-04 | OCPP 1.6 (all files & errata), `OCPP_1.6_documentation/schemas/json`; errata sheet 2025-04 (StopTransaction updated, `urn:OCPP:1.6:2025:04`) | `2a1d80284ca60449e85951fc55bc0538b5d45bd6f97c6d9228745acacb52c11b` |
| `ocpp16/security` | 22 | draft-06 | OCPP 1.6 Security Whitepaper Edition 4 (2026-02-05), `JSON_schemas.zip` | `158b883e8ee712fd80fad3610b69fad355f7831d7a0ccdd996ce6b5452ce6859` |
| `ocpp201` | 128 | draft-06 | OCPP 2.0.1 Edition 4 (all files), `OCPP-2.0.1_part3_JSON_schemas.zip` (2020-03-31; unchanged by Editions 2–4 and errata 2026-09) | `9e025b7d0ef45deb221a7b0bb24b6eb0d199dce8927e1512a8aa75419f511a64` |
| `ocpp21` | 181 | draft-06 | OCPP 2.1 Edition 2 (all files), `OCPP-2.1_part3_JSON_schemas.zip` (unchanged by errata 2026-09) | `2774ada26a709e3bf8680ee6721b3b215b8bc345dc6d8979b4c6b3d77f3a1f69` |

`SHA256SUMS` lists every file. Check with `shasum -a 256 -c SHA256SUMS` in
this directory.
