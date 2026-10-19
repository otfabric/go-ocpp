// SPDX-License-Identifier: MIT

package ocpp

import "errors"

// ErrInvalidMessage is wrapped by every *ValidationError: a payload that
// does not conform to the JSON schema of its action.
var ErrInvalidMessage = errors.New("ocpp: invalid message")

// ValidationError says why a payload does not conform to the schema of its
// action. The version packages return it for requests and responses they
// refuse, outgoing and incoming. An incoming CALL that fails validation is
// answered with a CALLERROR carrying Code.
type ValidationError struct {
	// Code is the CALLERROR code that fits the violation, spelt as the
	// connection's version spells it: FormationViolation/FormatViolation
	// for structure, OccurenceConstraintViolation/OccurrenceConstraintViolation
	// for a missing field or a wrong number of elements,
	// TypeConstraintViolation for a value of the wrong type, and
	// PropertyConstraintViolation for a value outside its constraints.
	Code ErrorCode
	// Field is the path of the offending field, e.g.
	// "meterValue[0].sampledValue[1].measurand"; empty for the payload.
	Field string
	// Reason says what is wrong.
	Reason string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return "ocpp: invalid message: " + e.Reason
	}
	return "ocpp: invalid message: " + e.Field + ": " + e.Reason
}

// Unwrap returns ErrInvalidMessage.
func (e *ValidationError) Unwrap() error { return ErrInvalidMessage }

// CallError returns the CALLERROR that answers a CALL failing validation.
func (e *ValidationError) CallError() *CallError {
	desc := e.Reason
	if e.Field != "" {
		desc = e.Field + ": " + e.Reason
	}
	return NewCallError(e.Code, desc)
}

// Ptr returns a pointer to v, for the optional fields of messages:
//
//	req := &ocpp16.BootNotificationRequest{
//	    ChargePointVendor:       "OTFabric",
//	    ChargePointModel:        "TestStation",
//	    ChargePointSerialNumber: ocpp.Ptr("OTF-0001"),
//	}
func Ptr[T any](v T) *T { return &v }
