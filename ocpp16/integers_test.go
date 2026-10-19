// SPDX-License-Identifier: MIT

package ocpp16

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// The integer rule follows the draft of each schema. The 1.6 core schemas are
// JSON Schema draft-04, in which an integer has no fraction and no exponent:
// 1.0 is refused. The Security Whitepaper schemas are draft-06, in which 1.0
// is an integer.
func TestIntegersFollowTheSchemaDraft(t *testing.T) {
	var ve *ocpp.ValidationError
	err := ValidateRequest("StartTransaction", json.RawMessage(`{"connectorId":1.0,"idTag":"T","meterStart":0,"timestamp":"2026-01-01T00:00:00Z"}`))
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation {
		t.Fatalf("draft-04 (core) refuses 1.0 for an integer: %v", err)
	}
	if err := ValidateRequest("SignedFirmwareStatusNotification", json.RawMessage(`{"status":"Idle","requestId":7.0}`)); err != nil {
		t.Fatalf("draft-06 (security) accepts 7.0 for an integer: %v", err)
	}
	err = ValidateRequest("SignedFirmwareStatusNotification", json.RawMessage(`{"status":"Idle","requestId":7.5}`))
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation {
		t.Fatalf("draft-06 refuses a fraction: %v", err)
	}
}
