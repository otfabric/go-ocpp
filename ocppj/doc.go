// SPDX-License-Identifier: MIT

// Package ocppj is the OCPP-J RPC frame codec: the JSON arrays that carry
// OCPP messages over WebSocket. It performs no I/O.
//
// Frames (OCPP-J 1.6 §4; OCPP 2.0.1 and 2.1 Part 4 §4):
//
//	CALL             [2, "<id>", "<action>", {payload}]
//	CALLRESULT       [3, "<id>", {payload}]
//	CALLERROR        [4, "<id>", "<code>", "<description>", {details}]
//	CALLRESULTERROR  [5, "<id>", "<code>", "<description>", {details}]  OCPP 2.1 only
//	SEND             [6, "<id>", "<action>", {payload}]                 OCPP 2.1 only
//
// Payloads stay json.RawMessage here. Typed decoding and validation belong to
// the version packages.
package ocppj
