// SPDX-License-Identifier: MIT

package ocpp16

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// A null array element is no string: the request is refused, and a typed
// call does not return a response with "" the peer never sent.
func TestNullArrayElement(t *testing.T) {
	var ve *ocpp.ValidationError
	err := ValidateRequest("GetConfiguration", json.RawMessage(`{"key":[null]}`))
	if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation || ve.Field != "key[0]" {
		t.Errorf("request: %v", err)
	}
	peer := &fakePeer{v: ocpp.V16, role: ocpp.CSMS, answers: map[string]json.RawMessage{"GetConfiguration": json.RawMessage(`{"unknownKey":["A",null]}`)}}
	if resp, err := GetConfiguration(context.Background(), peer, &GetConfigurationRequest{}); err == nil {
		t.Errorf("response accepted: %q", resp.UnknownKey)
	}
}

// A property name that appears twice is a FormationViolation.
func TestDuplicateKey(t *testing.T) {
	var ve *ocpp.ValidationError
	err := ValidateRequest("Authorize", json.RawMessage(`{"idTag":"A","idTag":"B"}`))
	if !errors.As(err, &ve) || ve.Code != ocpp.FormationViolation {
		t.Errorf("duplicate idTag: %v", err)
	}
}
