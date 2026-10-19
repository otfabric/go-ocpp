// SPDX-License-Identifier: MIT

// Package ocpp21 is OCPP 2.1 (Edition 2): a type for every request and
// response, validation against the OCA JSON schemas, a call function per
// action, and the handler interfaces and dispatchers of both roles.
//
// It is used like ocpp201. The Charging Station (the client package) sends
// with the call functions and answers the CSMS through ChargingStation(h):
//
//	c, err := client.New(url, client.WithHandler(ocpp.V21, ocpp21.ChargingStation(h)))
//	resp, err := ocpp21.BootNotification(ctx, c, &ocpp21.BootNotificationRequest{
//	    Reason:          ocpp21.BootReasonPowerUp,
//	    ChargingStation: ocpp21.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
//	})
//
// The CSMS (the server package) answers through CSMS(h) and sends to a
// session with the call functions:
//
//	srv, err := server.New(server.WithHandler(ocpp.V21, ocpp21.CSMS(h)), ...)
//	resp, err := ocpp21.GetVariables(ctx, session, &ocpp21.GetVariablesRequest{...})
//
// OCPP 2.1 adds the SEND message, an unconfirmed message that is never
// answered. Its one action, NotifyPeriodicEventStream, is sent with a call
// function that takes an ocpp.Sender and returns only an error; the
// receiving handler method returns only an error, which is logged and
// counted, never sent. The dispatchers implement ocpp.SendDispatcher.
//
// Every request is validated before it is sent and every response when it
// arrives; incoming requests are validated before a handler sees them and
// answered with the CALLERROR their violation calls for. A response that
// fails validation is answered with a CALLRESULTERROR. Optional fields are
// pointers (see ocpp.Ptr); required arrays must not be nil. CustomData keeps
// vendor extensions as raw JSON.
//
// The types are generated from the schemas in schemas/ocpp21 (CC BY-ND 4.0,
// unmodified); coverage.json lists every action. Do not edit the
// zz_generated files: change internal/gen and run go generate.
package ocpp21

//go:generate go run ../internal/gen -version 2.1 -package ocpp21 -schemas ../schemas/ocpp21
