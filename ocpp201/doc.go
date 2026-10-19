// SPDX-License-Identifier: MIT

// Package ocpp201 is OCPP 2.0.1 (Edition 4): a type for every request and
// response, validation against the OCA JSON schemas, a call function per
// action, and the handler interfaces and dispatchers of both roles.
//
// The Charging Station (the client package) sends with the call functions
// and answers the CSMS through ChargingStation(h):
//
//	c, err := client.New(url, client.WithHandler(ocpp.V201, ocpp201.ChargingStation(h)))
//	resp, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
//	    Reason:          ocpp201.BootReasonPowerUp,
//	    ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "TestStation"},
//	})
//
// The CSMS (the server package) answers through CSMS(h) and sends to a
// session with the call functions:
//
//	srv, err := server.New(server.WithHandler(ocpp.V201, ocpp201.CSMS(h)), ...)
//	resp, err := ocpp201.GetVariables(ctx, session, &ocpp201.GetVariablesRequest{...})
//
// Every request is validated before it is sent and every response when it
// arrives; incoming requests are validated before a handler sees them and
// answered with the CALLERROR their violation calls for. Optional fields
// are pointers (see ocpp.Ptr); required arrays must not be nil. CustomData
// keeps vendor extensions as raw JSON.
//
// The device model (components, variables, their attributes and
// characteristics) is represented by the schema types ComponentType,
// VariableType, VariableAttributeType and VariableCharacteristicsType and
// the messages that carry them; storing variables is the application's.
//
// The types are generated from the schemas in schemas/ocpp201 (CC BY-ND
// 4.0, unmodified); coverage.json lists every action. Do not edit the
// zz_generated files: change internal/gen and run go generate.
package ocpp201

//go:generate go run ../internal/gen -version 2.0.1 -package ocpp201 -schemas ../schemas/ocpp201
