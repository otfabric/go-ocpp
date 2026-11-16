// SPDX-License-Identifier: MIT

package ocpp16

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/schematest"
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

// A schema integer is a Go int: it decodes in the range of the platform's
// int, 64 bits on 64-bit builds and 32 bits on 32-bit builds
// (ARCHITECTURE.md, Implementation choices). The table expects the result
// of the platform it runs on; the nightly test-386 job of hardening.yml runs
// it with a 32-bit int. One field of a draft-04 (core) and one of a draft-06
// (security) schema.
func TestIntegerRange(t *testing.T) {
	schematest.CheckIntegerRange(t, schematest.IntegerField{
		Action:   "StartTransaction",
		Field:    "meterStart",
		Payload:  `{"connectorId":1,"idTag":"T","meterStart":%s,"timestamp":"2026-01-01T00:00:00Z"}`,
		Validate: ValidateRequest,
		Decode: func(p json.RawMessage) (int64, error) {
			var r StartTransactionRequest
			err := json.Unmarshal(p, &r)
			return int64(r.MeterStart), err
		},
	})
	schematest.CheckIntegerRange(t, schematest.IntegerField{
		Action:   "SignedFirmwareStatusNotification",
		Field:    "requestId",
		Payload:  `{"status":"Idle","requestId":%s}`,
		Draft06:  true,
		Validate: ValidateRequest,
		Decode: func(p json.RawMessage) (int64, error) {
			var r SignedFirmwareStatusNotificationRequest
			if err := json.Unmarshal(p, &r); err != nil {
				return 0, err
			}
			if r.RequestID == nil {
				return 0, errors.New("requestId not decoded")
			}
			return int64(*r.RequestID), nil
		},
	})
}
