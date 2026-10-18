// SPDX-License-Identifier: MIT

package ocpp201

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/schematest"
)

// FuzzDispatch feeds arbitrary payloads to both dispatchers, for every
// action: decoding and validation must never panic, and must answer with a
// CALLERROR or a response.
func FuzzDispatch(f *testing.F) {
	for i := range Actions {
		f.Add(uint8(i), []byte(`{}`))
		f.Add(uint8(i), []byte(`{"customData":{"vendorId":"x"},"idTag":"A","meterValue":[{"timestamp":"2026-01-01T00:00:00Z","sampledValue":[{"value":"1"}]}]}`))
	}
	// Every action's request and response, with every property and with
	// the required ones only, valid against the schemas.
	for i, a := range readManifest(f).Actions {
		for _, full := range []bool{true, false} {
			f.Add(uint8(i), []byte(schematest.Instance(f, filepath.Join("..", a.RequestSchema), full)))
			if a.ResponseSchema != "" {
				f.Add(uint8(i), []byte(schematest.Instance(f, filepath.Join("..", a.ResponseSchema), full)))
			}
		}
	}
	f.Add(uint8(0), []byte(`[1,2,3]`))
	f.Add(uint8(1), []byte(`null`))
	dispatchers := []ocpp.Dispatcher{ChargingStation(UnimplementedChargingStationHandler{}), CSMS(UnimplementedCSMSHandler{})}
	peer := &fakePeer{v: ocpp.V201}
	f.Fuzz(func(t *testing.T, i uint8, payload []byte) {
		action := Actions[int(i)%len(Actions)].Name
		for _, d := range dispatchers {
			out, err := d.Dispatch(context.Background(), peer, action, json.RawMessage(payload))
			if err == nil && !json.Valid(out) {
				t.Fatalf("%s: invalid response %q", action, out)
			}
		}
		_ = ValidateRequest(action, payload)
		_ = ValidateResponse(action, payload)
	})
}
