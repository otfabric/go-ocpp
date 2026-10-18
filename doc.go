// SPDX-License-Identifier: MIT

// Package ocpp holds what the OCPP versions and both roles of go-ocpp
// share: the protocol versions and their WebSocket subprotocols, and the
// sentinel errors.
//
// go-ocpp is in its foundation phase. The RPC engine, the client
// (Charging Station role), the server (CSMS role) and the version packages
// ocpp16, ocpp201 and ocpp21 are designed in ARCHITECTURE.md and are not
// implemented yet.
//
// Documentation:
//
//   - ARCHITECTURE.md: layers, packages, the engine, both roles
//   - REQUIREMENTS.md: what the library must do
//   - SPECIFICATIONS.md: the OCA editions and errata it follows
//   - IMPLEMENTATION_PLAN.md: phases and their gates
//   - ERRORS.md: error semantics
package ocpp
