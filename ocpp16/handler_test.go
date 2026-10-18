// SPDX-License-Identifier: MIT

package ocpp16

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
)

type centralSystem struct {
	UnimplementedCentralSystemHandler
	answer func(req *BootNotificationRequest) (*BootNotificationResponse, error)
}

func (h centralSystem) BootNotification(ctx context.Context, p ocpp.Peer, req *BootNotificationRequest) (*BootNotificationResponse, error) {
	return h.answer(req)
}

func TestHandler(t *testing.T) {
	ctx := context.Background()
	peer := &fakePeer{v: ocpp.V16}
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	accept := func(req *BootNotificationRequest) (*BootNotificationResponse, error) {
		if req.ChargePointVendor != "OTFabric" || req.ChargePointSerialNumber == nil || *req.ChargePointSerialNumber != "OTF-0001" {
			return nil, ocpp.NewCallError(ocpp.PropertyConstraintViolation, "unexpected request")
		}
		return &BootNotificationResponse{Status: RegistrationStatusAccepted, CurrentTime: now, Interval: 300}, nil
	}
	valid := json.RawMessage(`{"chargePointVendor":"OTFabric","chargePointModel":"TestStation","chargePointSerialNumber":"OTF-0001"}`)

	out, err := CentralSystem(centralSystem{answer: accept}).Dispatch(ctx, peer, "BootNotification", valid)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"status":"Accepted","currentTime":"2026-10-18T12:00:00Z","interval":300}` {
		t.Fatalf("response %s", out)
	}

	for _, tt := range []struct {
		name    string
		payload string
		code    ocpp.ErrorCode
	}{
		{"missing required", `{"chargePointVendor":"OTFabric"}`, ocpp.OccurenceConstraintViolation},
		{"unknown property", `{"chargePointVendor":"OTFabric","chargePointModel":"M","colour":"red"}`, ocpp.FormationViolation},
		{"wrong type", `{"chargePointVendor":"OTFabric","chargePointModel":7}`, ocpp.TypeConstraintViolation},
		{"too long", `{"chargePointVendor":"OTFabric with a very long name","chargePointModel":"M"}`, ocpp.PropertyConstraintViolation},
		{"not JSON", `{"chargePointVendor":`, ocpp.FormationViolation},
		{"handler CALLERROR", `{"chargePointVendor":"Other","chargePointModel":"M"}`, ocpp.PropertyConstraintViolation},
	} {
		_, err := CentralSystem(centralSystem{answer: accept}).Dispatch(ctx, peer, "BootNotification", json.RawMessage(tt.payload))
		var ce *ocpp.CallError
		if !errors.As(err, &ce) || ce.Code != tt.code {
			t.Errorf("%s: %v, want CALLERROR %s", tt.name, err, tt.code)
		}
	}

	// A handler without a response, or with an invalid one: an error that
	// is not a CALLERROR, which the engine answers with InternalError.
	for name, answer := range map[string]func(*BootNotificationRequest) (*BootNotificationResponse, error){
		"no response": func(*BootNotificationRequest) (*BootNotificationResponse, error) { return nil, nil },
		"invalid response": func(*BootNotificationRequest) (*BootNotificationResponse, error) {
			return &BootNotificationResponse{Status: "Maybe"}, nil
		},
	} {
		_, err := CentralSystem(centralSystem{answer: answer}).Dispatch(ctx, peer, "BootNotification", valid)
		var ce *ocpp.CallError
		if err == nil || errors.As(err, &ce) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A required array that is nil is refused before it is sent.
	err = (&MeterValuesRequest{ConnectorID: 1}).Validate()
	var ve *ocpp.ValidationError
	if !errors.As(err, &ve) || ve.Field != "meterValue" || !errors.Is(err, ocpp.ErrInvalidMessage) {
		t.Fatalf("nil required array: %v", err)
	}
}
