// SPDX-License-Identifier: MIT

// Package ocpp16 is OCPP 1.6J, with the extensions of the OCPP 1.6
// Security Whitepaper Edition 4: a type for every request and response,
// validation against the OCA JSON schemas, a call function per action, and
// the handler interfaces and dispatchers of both roles.
//
// The Charge Point (the client package) sends with the call functions and
// answers the Central System through ChargePoint(h):
//
//	c, err := client.New(url, client.WithHandler(ocpp.V16, ocpp16.ChargePoint(h)))
//	resp, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{
//	    ChargePointVendor: "OTFabric",
//	    ChargePointModel:  "TestStation",
//	})
//
// The Central System (the server package) answers through CentralSystem(h)
// and sends to a session with the call functions:
//
//	srv, err := server.New(server.WithHandler(ocpp.V16, ocpp16.CentralSystem(h)), ...)
//	resp, err := ocpp16.Reset(ctx, session, &ocpp16.ResetRequest{Type: ocpp16.ResetSoft})
//
// Every request is validated before it is sent and every response when it
// arrives; incoming requests are validated before a handler sees them and
// answered with the CALLERROR their violation calls for. Optional fields
// are pointers (see ocpp.Ptr); required arrays must not be nil.
//
// The types are generated from the schemas in schemas/ocpp16 (CC BY-ND 4.0,
// unmodified); coverage.json lists every action. Do not edit the
// zz_generated files: change internal/gen and run go generate.
package ocpp16

//go:generate go run ../internal/gen -version 1.6 -package ocpp16 -schemas ../schemas/ocpp16/core,../schemas/ocpp16/security
