// SPDX-License-Identifier: MIT

package ocpp16

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/schematest"
)

// FuzzDispatch feeds arbitrary payloads to both dispatchers, for every
// action: decoding and validation must always return, and must answer with a
// CALLERROR or a response. A payload that validation accepts is valid UTF-8
// which encoding/json decodes without replacing any of its text
// (schematest.TextReplaced).
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
	for i := range Actions {
		f.Add(uint8(i), []byte(`{"idTag":"A\ud83dB","idToken":{"idToken":"A\ude00B","type":"Central"}}`))
		f.Add(uint8(i), []byte("{\"idTag\":\"A\xffB\",\"idToken\":{\"idToken\":\"A\xe2\x82\",\"type\":\"Central\"}}"))
		f.Add(uint8(i), []byte(`{"idTag":"A\ud83d\ude00B","idToken":{"idToken":"A\ufffdB","type":"Central"}}`))
	}
	f.Add(uint8(0), []byte(`[1,2,3]`))
	f.Add(uint8(1), []byte(`null`))
	dispatchers := []ocpp.Dispatcher{ChargePoint(UnimplementedChargePointHandler{}), CentralSystem(UnimplementedCentralSystemHandler{})}
	peer := &fakePeer{v: ocpp.V16}
	f.Fuzz(func(t *testing.T, i uint8, payload []byte) {
		action := Actions[int(i)%len(Actions)].Name
		for _, d := range dispatchers {
			out, err := d.Dispatch(context.Background(), peer, action, json.RawMessage(payload))
			if err == nil && !json.Valid(out) {
				t.Fatalf("%s: invalid response %q", action, out)
			}
		}
		for what, validate := range map[string]func(string, json.RawMessage) error{"request": ValidateRequest, "response": ValidateResponse} {
			if validate(action, payload) != nil {
				continue
			}
			if !utf8.Valid(payload) {
				t.Fatalf("%s %s: accepted %q, which is not valid UTF-8", action, what, payload)
			}
			if schematest.TextReplaced(payload) {
				t.Fatalf("%s %s: accepted %q, of which encoding/json replaces text", action, what, payload)
			}
		}
	})
}
