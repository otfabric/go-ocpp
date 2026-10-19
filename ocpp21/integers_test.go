// SPDX-License-Identifier: MIT

package ocpp21

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// JSON Schema draft-06 integers include numbers with a zero fractional part:
// EVerest libocpp sends NotifyEVChargingNeeds so.
func TestIntegralFloatsAreIntegers(t *testing.T) {
	everest := `{"chargingNeeds":{"acChargingParameters":{"energyAmount":20000.0,"evMaxCurrent":32.0,"evMaxVoltage":400.0,"evMinCurrent":6.0},"departureTime":"2026-01-01T18:00:00.000Z","requestedEnergyTransfer":"AC_three_phase"},"evseId":1,"maxScheduleTuples":3}`
	if err := ValidateRequest("NotifyEVChargingNeeds", json.RawMessage(everest)); err != nil {
		t.Fatalf("integral floats refused: %v", err)
	}
	var req NotifyEVChargingNeedsRequest
	if err := json.Unmarshal([]byte(everest), &req); err != nil || req.ChargingNeeds.ACChargingParameters.EnergyAmount != 20000 {
		t.Fatalf("decoded: %v %+v", err, req.ChargingNeeds.ACChargingParameters)
	}
	err := ValidateRequest("NotifyEVChargingNeeds", json.RawMessage(`{"chargingNeeds":{"acChargingParameters":{"energyAmount":20000,"evMaxCurrent":32,"evMaxVoltage":400,"evMinCurrent":6},"requestedEnergyTransfer":"AC_three_phase"},"evseId":1,"maxScheduleTuples":3.5}`))
	var ve *ocpp.ValidationError
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation {
		t.Fatalf("a fraction: %v", err)
	}
	if err := ValidateResponse("Heartbeat", json.RawMessage(`{"currentTime":"2026-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	if err := ValidateResponse("BootNotification", json.RawMessage(`{"currentTime":"2026-01-01T00:00:00Z","interval":3e2,"status":"Accepted"}`)); err != nil {
		t.Fatalf("3e2 is the integer 300: %v", err)
	}
}
