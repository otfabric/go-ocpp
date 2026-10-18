// SPDX-License-Identifier: MIT

package ocppj

import "github.com/otfabric/go-ocpp"

var errorCodes16 = []ocpp.ErrorCode{
	ocpp.NotImplemented, ocpp.NotSupported, ocpp.InternalError, ocpp.ProtocolError,
	ocpp.SecurityError, ocpp.FormationViolation, ocpp.PropertyConstraintViolation,
	ocpp.OccurenceConstraintViolation, ocpp.TypeConstraintViolation, ocpp.GenericError,
}

var errorCodes2x = []ocpp.ErrorCode{
	ocpp.FormatViolation, ocpp.GenericError, ocpp.InternalError, ocpp.MessageTypeNotSupported,
	ocpp.NotImplemented, ocpp.NotSupported, ocpp.OccurrenceConstraintViolation,
	ocpp.PropertyConstraintViolation, ocpp.ProtocolError, ocpp.RpcFrameworkError,
	ocpp.SecurityError, ocpp.TypeConstraintViolation,
}

// ErrorCodes returns the CALLERROR error codes version v defines
// (OCPP-J 1.6 §4.2.3; OCPP 2.0.1 and 2.1 Part 4 §4.3).
func ErrorCodes(v ocpp.Version) []ocpp.ErrorCode {
	switch v {
	case ocpp.V16:
		return append([]ocpp.ErrorCode(nil), errorCodes16...)
	case ocpp.V201, ocpp.V21:
		return append([]ocpp.ErrorCode(nil), errorCodes2x...)
	default:
		return nil
	}
}

// ValidErrorCode reports whether version v defines code.
func ValidErrorCode(v ocpp.Version, code ocpp.ErrorCode) bool {
	for _, c := range ErrorCodes(v) {
		if c == code {
			return true
		}
	}
	return false
}

// FormatErrorCode returns the code for a payload that is not valid JSON or
// does not have the structure its action defines: FormationViolation in
// OCPP 1.6 and FormatViolation in 2.x.
func FormatErrorCode(v ocpp.Version) ocpp.ErrorCode {
	if v == ocpp.V16 {
		return ocpp.FormationViolation
	}
	return ocpp.FormatViolation
}

// OccurrenceErrorCode returns the code for a missing required field or a
// wrong number of array elements, spelt as version v spells it.
func OccurrenceErrorCode(v ocpp.Version) ocpp.ErrorCode {
	if v == ocpp.V16 {
		return ocpp.OccurenceConstraintViolation
	}
	return ocpp.OccurrenceConstraintViolation
}

// RPCErrorCode returns the code for a CALL whose frame is not a valid RPC
// request although its message ID could be read: RpcFrameworkError in
// OCPP 2.x. OCPP 1.6 has no such code; FormationViolation is used, the
// closest 1.6 code (an implementation choice, see ARCHITECTURE.md).
func RPCErrorCode(v ocpp.Version) ocpp.ErrorCode {
	if v == ocpp.V16 {
		return ocpp.FormationViolation
	}
	return ocpp.RpcFrameworkError
}
