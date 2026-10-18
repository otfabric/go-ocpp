// SPDX-License-Identifier: MIT

package ocpp

import (
	"encoding/json"
	"fmt"
)

// ErrorCode is the error code of a CALLERROR (and, in OCPP 2.1, of a
// CALLRESULTERROR). Which codes a version allows is defined by package
// ocppj (ocppj.ValidErrorCode).
type ErrorCode string

// The CALLERROR error codes of OCPP-J 1.6 §4.2.3 and OCPP 2.0.1/2.1 Part 4
// §4.3. Most codes exist in every version. Where the versions differ, the
// constant's comment says so.
const (
	NotImplemented              ErrorCode = "NotImplemented"
	NotSupported                ErrorCode = "NotSupported"
	InternalError               ErrorCode = "InternalError"
	ProtocolError               ErrorCode = "ProtocolError"
	SecurityError               ErrorCode = "SecurityError"
	PropertyConstraintViolation ErrorCode = "PropertyConstraintViolation"
	TypeConstraintViolation     ErrorCode = "TypeConstraintViolation"
	GenericError                ErrorCode = "GenericError"

	// FormationViolation is the 1.6 name of the code 2.x calls FormatViolation.
	FormationViolation ErrorCode = "FormationViolation"
	// FormatViolation is OCPP 2.x only. See FormationViolation.
	FormatViolation ErrorCode = "FormatViolation"

	// OccurenceConstraintViolation is the OCPP 1.6 spelling, with one "r",
	// as the 1.6 specification writes it.
	OccurenceConstraintViolation ErrorCode = "OccurenceConstraintViolation"
	// OccurrenceConstraintViolation is the OCPP 2.x spelling.
	OccurrenceConstraintViolation ErrorCode = "OccurrenceConstraintViolation"

	// MessageTypeNotSupported is OCPP 2.x only.
	MessageTypeNotSupported ErrorCode = "MessageTypeNotSupported"
	// RpcFrameworkError is OCPP 2.x only.
	RpcFrameworkError ErrorCode = "RpcFrameworkError" //nolint:revive // the name the specification uses
)

// CallError is a CALLERROR: the peer's answer that it could not process a
// CALL. A call returns it as the error (test with errors.As), and a handler
// returns it to answer with a CALLERROR of its choosing.
//
// A CALLERROR is not a business rejection. A BootNotification answered with
// status Rejected is a successful call.
type CallError struct {
	Code        ErrorCode
	Description string
	// Details is a JSON object, or nil for {}.
	Details json.RawMessage
}

// NewCallError returns a CallError with the given code and description.
func NewCallError(code ErrorCode, description string) *CallError {
	return &CallError{Code: code, Description: description}
}

// Error implements error.
func (e *CallError) Error() string {
	if e.Description == "" {
		return fmt.Sprintf("ocpp: CALLERROR %s", e.Code)
	}
	return fmt.Sprintf("ocpp: CALLERROR %s: %s", e.Code, e.Description)
}
