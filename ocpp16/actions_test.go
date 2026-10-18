// SPDX-License-Identifier: MIT

package ocpp16

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/schematest"
)

// The dispatchers of the two roles, answering every action with
// NotSupported.
var (
	receivingStation = ChargePoint(UnimplementedChargePointHandler{})
	receivingCSMS    = CentralSystem(UnimplementedCentralSystemHandler{})
)

// fakePeer is an ocpp.Peer whose calls are answered from a table.
type fakePeer struct {
	v       ocpp.Version
	role    ocpp.Role
	answers map[string]json.RawMessage
	sent    []string
}

func (p *fakePeer) ID() string            { return "CS1" }
func (p *fakePeer) Version() ocpp.Version { return p.v }
func (p *fakePeer) Role() ocpp.Role       { return p.role }
func (p *fakePeer) Call(ctx context.Context, action string, req, resp any) error {
	p.sent = append(p.sent, action)
	if _, err := json.Marshal(req); err != nil {
		return err
	}
	return json.Unmarshal(p.answers[action], resp)
}

func roleOf(from string) ocpp.Role {
	switch from {
	case "ChargingStation":
		return ocpp.ChargingStation
	case "CSMS":
		return ocpp.CSMS
	}
	return 0
}

func other(r ocpp.Role) ocpp.Role {
	if r == ocpp.CSMS {
		return ocpp.ChargingStation
	}
	return ocpp.CSMS
}

// TestEveryAction sends every action with its call function and dispatches
// it to both roles.
func TestEveryAction(t *testing.T) {
	ctx := context.Background()
	for _, a := range readManifest(t).Actions {
		t.Run(a.Action, func(t *testing.T) {
			reqJSON := schematest.Instance(t, filepath.Join("..", a.RequestSchema), true)
			respJSON := schematest.Instance(t, filepath.Join("..", a.ResponseSchema), true)
			from := roleOf(a.From)
			sender := from
			if sender == 0 {
				sender = ocpp.ChargingStation
			}
			newReq := func() any {
				req := newRequest(a.Action)
				if err := json.Unmarshal(reqJSON, req); err != nil {
					t.Fatal(err)
				}
				return req
			}

			// The call function.
			peer := &fakePeer{v: testVersion, role: sender, answers: map[string]json.RawMessage{a.Action: respJSON}}
			resp, err := callByName[a.Action](ctx, peer, newRequest(a.Action))
			if err == nil {
				// The zero request is invalid for actions with required fields;
				// when it is valid the call simply succeeds.
				_ = resp
			}
			resp, err = callByName[a.Action](ctx, peer, newReq().(interface{ Validate() error }))
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			if out, _ := json.Marshal(resp); len(out) == 0 {
				t.Fatal("no response")
			}
			// A response that is not the action's.
			bad := &fakePeer{v: testVersion, role: sender, answers: map[string]json.RawMessage{a.Action: json.RawMessage(`{"notInTheSchema":1}`)}}
			if _, err := callByName[a.Action](ctx, bad, newReq().(interface{ Validate() error })); err == nil {
				t.Fatal("an invalid response was accepted")
			}
			// The wrong version, and the wrong role.
			wrongV := &fakePeer{v: otherVersion, role: sender}
			if _, err := callByName[a.Action](ctx, wrongV, newReq().(interface{ Validate() error })); !errors.Is(err, ocpp.ErrUnsupportedVersion) {
				t.Fatalf("wrong version: %v", err)
			}
			if from != 0 {
				wrongR := &fakePeer{v: testVersion, role: other(from)}
				if _, err := callByName[a.Action](ctx, wrongR, newReq().(interface{ Validate() error })); !errors.Is(err, ocpp.ErrWrongDirection) {
					t.Fatalf("wrong role: %v", err)
				}
			}

			// The dispatchers: the receiving role's answers NotSupported
			// (Unimplemented), and so does the sending role's.
			for _, d := range []ocpp.Dispatcher{receivingStation, receivingCSMS} {
				_, err := d.Dispatch(ctx, peer, a.Action, reqJSON)
				var ce *ocpp.CallError
				if !errors.As(err, &ce) || ce.Code != ocpp.NotSupported {
					t.Fatalf("dispatch: %v", err)
				}
			}
			if err := ValidateRequest(a.Action, reqJSON); err != nil {
				t.Fatal(err)
			}
			if err := ValidateResponse(a.Action, respJSON); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnknownAction(t *testing.T) {
	ctx := context.Background()
	for _, d := range []ocpp.Dispatcher{receivingStation, receivingCSMS} {
		_, err := d.Dispatch(ctx, &fakePeer{v: testVersion}, "NoSuchAction", json.RawMessage(`{}`))
		var ce *ocpp.CallError
		if !errors.As(err, &ce) || ce.Code != ocpp.NotImplemented {
			t.Fatalf("got %v", err)
		}
	}
	if err := ValidateRequest("NoSuchAction", nil); err == nil {
		t.Fatal("ValidateRequest accepted an unknown action")
	}
	if err := ValidateResponse("NoSuchAction", nil); err == nil {
		t.Fatal("ValidateResponse accepted an unknown action")
	}
	if newRequest("NoSuchAction") != nil || newResponse("NoSuchAction") != nil {
		t.Fatal("factories know an unknown action")
	}
}
