// SPDX-License-Identifier: MIT

package ocpp201

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// clearMonitoring records whether ClearVariableMonitoring was called.
type clearMonitoring struct {
	UnimplementedChargingStationHandler
	called *bool
}

func (h clearMonitoring) ClearVariableMonitoring(ctx context.Context, p ocpp.Peer, req *ClearVariableMonitoringRequest) (*ClearVariableMonitoringResponse, error) {
	*h.called = true
	return nil, errors.New("not reached")
}

// A null array element is no integer: the request is answered with
// TypeConstraintViolation at the element, and the handler is not called
// with a 0 the peer never sent.
func TestNullArrayElement(t *testing.T) {
	for _, p := range []string{`{"id":[null]}`, `{"id":[7,null]}`} {
		called := false
		_, err := ChargingStation(clearMonitoring{called: &called}).Dispatch(context.Background(), &fakePeer{v: ocpp.V201}, "ClearVariableMonitoring", json.RawMessage(p))
		var ce *ocpp.CallError
		if !errors.As(err, &ce) || ce.Code != ocpp.TypeConstraintViolation || called {
			t.Errorf("%s: %v (handler called: %v)", p, err, called)
		}
	}
	var ve *ocpp.ValidationError
	err := ValidateRequest("GetChargingProfiles", json.RawMessage(`{"requestId":1,"chargingProfile":{"chargingProfileId":[null]}}`))
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation || ve.Field != "chargingProfileId[0]" {
		t.Errorf("nested null element: %v", err)
	}
	// A typed response is refused too.
	err = ValidateResponse("GetInstalledCertificateIds", json.RawMessage(`{"status":"Accepted","certificateHashDataChain":[null]}`))
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation {
		t.Errorf("null object element in a response: %v", err)
	}
}

// DataTransfer's data has no type in the schema: any JSON value, null
// included, is allowed, in the request and in the response.
func TestDataTransferNull(t *testing.T) {
	for _, p := range []string{`{"vendorId":"v","data":null}`, `{"vendorId":"v","data":[null,1]}`} {
		if err := ValidateRequest("DataTransfer", json.RawMessage(p)); err != nil {
			t.Errorf("%s: %v", p, err)
		}
		var req DataTransferRequest
		if err := json.Unmarshal([]byte(p), &req); err != nil || req.Data == nil {
			t.Errorf("%s: %v, data %s", p, err, req.Data)
		}
	}
	if err := ValidateResponse("DataTransfer", json.RawMessage(`{"status":"Accepted","data":null}`)); err != nil {
		t.Errorf("response data null: %v", err)
	}
	var ve *ocpp.ValidationError
	if err := ValidateRequest("DataTransfer", json.RawMessage(`{"vendorId":null}`)); !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation {
		t.Errorf("vendorId null: %v", err)
	}
}

// A property name that appears twice is a FormatViolation: encoding/json
// would otherwise merge the two idTokens into one.
func TestDuplicateKey(t *testing.T) {
	p := `{"idToken":{"idToken":"A","type":"ISO14443","additionalInfo":[{"additionalIdToken":"x","type":"y"}]},"idToken":{"idToken":"B","type":"ISO14443"}}`
	var ve *ocpp.ValidationError
	if err := ValidateRequest("Authorize", json.RawMessage(p)); !errors.As(err, &ve) || ve.Code != ocpp.FormatViolation {
		t.Errorf("duplicate idToken: %v", err)
	}
	p = `{"idToken":{"idToken":"A","type":"ISO14443","idToken":"B"}}`
	if err := ValidateRequest("Authorize", json.RawMessage(p)); !errors.As(err, &ve) || ve.Code != ocpp.FormatViolation {
		t.Errorf("duplicate nested idToken: %v", err)
	}
}
