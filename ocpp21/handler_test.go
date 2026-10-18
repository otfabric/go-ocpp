// SPDX-License-Identifier: MIT

package ocpp21

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// station answers GetVariables from a small device model.
type station struct {
	UnimplementedChargingStationHandler
	vars map[[2]string]string
}

func (s station) GetVariables(ctx context.Context, p ocpp.Peer, req *GetVariablesRequest) (*GetVariablesResponse, error) {
	resp := &GetVariablesResponse{GetVariableResult: []GetVariableResultType{}}
	for _, d := range req.GetVariableData {
		r := GetVariableResultType{Component: d.Component, Variable: d.Variable, AttributeStatus: GetVariableStatusUnknownVariable}
		if v, ok := s.vars[[2]string{d.Component.Name, d.Variable.Name}]; ok {
			r.AttributeStatus, r.AttributeValue = GetVariableStatusAccepted, ocpp.Ptr(v)
		}
		resp.GetVariableResult = append(resp.GetVariableResult, r)
	}
	return resp, nil
}

func TestHandler(t *testing.T) {
	ctx := context.Background()
	h := ChargingStation(station{vars: map[[2]string]string{{"OCPPCommCtrlr", "HeartbeatInterval"}: "300"}})
	req := json.RawMessage(`{"getVariableData":[
		{"component":{"name":"OCPPCommCtrlr"},"variable":{"name":"HeartbeatInterval"}},
		{"component":{"name":"OCPPCommCtrlr","customData":{"vendorId":"com.example","shade":"blue"}},"variable":{"name":"Nope"}}]}`)
	out, err := h.Dispatch(ctx, &fakePeer{v: ocpp.V21}, "GetVariables", req)
	if err != nil {
		t.Fatal(err)
	}
	var resp GetVariablesResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	r := resp.GetVariableResult
	if len(r) != 2 || r[0].AttributeStatus != GetVariableStatusAccepted || *r[0].AttributeValue != "300" ||
		r[1].AttributeStatus != GetVariableStatusUnknownVariable {
		t.Fatalf("response %s", out)
	}
	// CustomData keeps its vendor's properties through a round trip.
	cd := r[1].Component.CustomData
	if cd == nil || cd.VendorID != "com.example" || string(cd.Fields["shade"]) != `"blue"` {
		t.Fatalf("customData %+v", cd)
	}

	for _, tt := range []struct {
		name, payload string
		code          ocpp.ErrorCode
	}{
		{"empty array", `{"getVariableData":[]}`, ocpp.OccurrenceConstraintViolation},
		{"missing component name", `{"getVariableData":[{"component":{},"variable":{"name":"X"}}]}`, ocpp.OccurrenceConstraintViolation},
		{"unknown attribute", `{"getVariableData":[{"component":{"name":"C"},"variable":{"name":"X"},"attributeType":"Wanted"}]}`, ocpp.PropertyConstraintViolation},
		{"customData without vendorId", `{"getVariableData":[{"component":{"name":"C","customData":{}},"variable":{"name":"X"}}]}`, ocpp.OccurrenceConstraintViolation},
		{"customData vendorId not a string", `{"getVariableData":[{"component":{"name":"C","customData":{"vendorId":1}},"variable":{"name":"X"}}]}`, ocpp.TypeConstraintViolation},
		{"unknown property", `{"getVariableData":[{"component":{"name":"C"},"variable":{"name":"X"}}],"extra":true}`, ocpp.FormatViolation},
	} {
		_, err := h.Dispatch(ctx, &fakePeer{v: ocpp.V21}, "GetVariables", json.RawMessage(tt.payload))
		var ce *ocpp.CallError
		if !errors.As(err, &ce) || ce.Code != tt.code {
			t.Errorf("%s: %v, want %s", tt.name, err, tt.code)
		}
	}

	// CustomData encodes its vendorId and its other properties; a vendorId
	// among Fields does not override VendorID.
	b, err := json.Marshal(CustomData{VendorID: "v", Fields: map[string]json.RawMessage{"n": json.RawMessage(`1`), "vendorId": json.RawMessage(`"x"`)}})
	if err != nil || string(b) != `{"n":1,"vendorId":"v"}` {
		t.Fatalf("%s %v", b, err)
	}
	if err := (&CustomData{VendorID: string(make([]byte, 256))}).Validate(); err == nil {
		t.Fatal("a vendorId over 255 characters was accepted")
	}
	var cdNotObject CustomData
	if err := json.Unmarshal([]byte(`"x"`), &cdNotObject); err == nil {
		t.Fatal("a string was accepted as customData")
	}
}

// null is no value of any schema type: an optional property is left out,
// not null. Vendor properties of customData are free.
func TestNull(t *testing.T) {
	var cd CustomData
	if err := json.Unmarshal([]byte(`{"vendorId":"v","extra":null}`), &cd); err != nil {
		t.Fatalf("null vendor property refused: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"vendorId":null}`), &cd); err == nil {
		t.Fatal("null vendorId accepted")
	}
	err := ValidateResponse("Reset", json.RawMessage(`{"status":"Accepted","statusInfo":null}`))
	var ve *ocpp.ValidationError
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation || ve.Field != "statusInfo" {
		t.Fatalf("statusInfo null: %v", err)
	}
}
