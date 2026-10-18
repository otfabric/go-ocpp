// SPDX-License-Identifier: MIT

package ocppj

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// FuzzDecode checks that Decode never panics, that what it accepts encodes
// again, and that the re-encoded frame decodes to the same frame.
func FuzzDecode(f *testing.F) {
	for _, s := range []string{
		`[2,"1","Heartbeat",{}]`,
		`[3,"1",{"currentTime":"2026-01-01T00:00:00Z"}]`,
		`[4,"1","GenericError","x",{}]`,
		`[5,"1","GenericError","x",{}]`,
		`[6,"1","NotifyPeriodicEventStream",{"data":[1,2,3]}]`,
		`[2,"1","Heartbeat",{"a":{"b":[null,true,1.5e3,"é"]}}]`,
		`[2, "1" , "Heartbeat", { "a" : [ 1 , 2 ] } ]`,
		`[2.0,"1","x",{}]`, `[]`, `[2]`, `{}`, `"x"`, `[2,"1","x",{}] trailing`,
	} {
		for _, v := range ocpp.Versions() {
			f.Add([]byte(s), uint8(v))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, vb uint8) {
		v := ocpp.Version(vb%3 + 1)
		fr, err := Decode(data, v, Limits{})
		if err != nil {
			return
		}
		out, err := Encode(fr)
		if err != nil {
			t.Fatalf("decoded %q, but cannot encode %+v: %v", data, fr, err)
		}
		again, err := Decode(out, v, Limits{MaxFrameSize: len(out) + 1})
		if err != nil {
			t.Fatalf("re-encoded %q does not decode: %v", out, err)
		}
		// Encode compacts payloads and details; compare them compacted.
		if again.Type != fr.Type || again.ID != fr.ID || again.Action != fr.Action || again.ErrorCode != fr.ErrorCode ||
			again.Description != fr.Description || !sameJSON(again.Payload, fr.Payload) || !sameJSON(again.Details, fr.Details) {
			t.Fatalf("round trip changed %+v into %+v", fr, again)
		}
	})
}

func sameJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
