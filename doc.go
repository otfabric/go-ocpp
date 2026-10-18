// SPDX-License-Identifier: MIT

// Package ocpp holds what the OCPP versions and both roles of go-ocpp
// share: the protocol versions and their WebSocket subprotocols, the
// contracts between the layers (Peer, Caller, Dispatcher), CALLERRORs and
// their codes, the sentinel errors, and the observability hooks.
//
// The roles are in package client (Charging Station) and package server
// (CSMS); the OCPP-J frame codec is in package ocppj. The typed messages of
// the version packages ocpp16, ocpp201 and ocpp21 are designed in
// ARCHITECTURE.md and are not implemented yet: until then, requests and
// responses are raw JSON.
//
// Documentation:
//
//   - ARCHITECTURE.md: layers, packages, the engine, both roles
//   - REQUIREMENTS.md: what the library must do
//   - SPECIFICATIONS.md: the OCA editions and errata it follows
//   - IMPLEMENTATION_PLAN.md: phases and their gates
//   - ERRORS.md: error semantics
package ocpp
