// SPDX-License-Identifier: MIT

package ocppj

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// A Frame owns its memory: Decode copies what it keeps, so the caller may
// reuse the input, and appending to the Frame's payload or details does not
// write into the input.
func TestDecodeDoesNotAliasInput(t *testing.T) {
	for _, in := range []string{
		`[2,"id-1","Authorize",{"idTag":"ABC","list":[1,2,3]}]`,
		`[3,"id-1",{"status":"Accepted"}]`,
		`[4,"id-1","GenericError","a description",{"detail":"x"}]`,
		`[6,"id-1","NotifyPeriodicEventStream",{"id":1}]`,
	} {
		data := []byte(in)
		f, err := Decode(data, ocpp.V21, Limits{})
		if err != nil {
			t.Fatalf("Decode(%s): %v", in, err)
		}
		want := Frame{Type: f.Type, ID: f.ID, Action: f.Action, ErrorCode: f.ErrorCode, Description: f.Description,
			Payload: bytes.Clone(f.Payload), Details: bytes.Clone(f.Details)}

		// The payload and the details do not reach into the input.
		for name, part := range map[string]json.RawMessage{"payload": f.Payload, "details": f.Details} {
			if len(part) > 0 && len(data) > 0 && overlap(part, data) {
				t.Errorf("%s: the %s shares memory with the input", in, name)
			}
		}
		// Overwriting the input changes nothing in the Frame.
		for i := range data {
			data[i] = 'X'
		}
		if f.ID != want.ID || f.Action != want.Action || f.ErrorCode != want.ErrorCode || f.Description != want.Description ||
			!bytes.Equal(f.Payload, want.Payload) || !bytes.Equal(f.Details, want.Details) {
			t.Errorf("%s: the Frame changed with its input: %+v, want %+v", in, f, want)
		}
		// Appending to a part of the Frame writes into neither the input nor
		// the other part.
		data = []byte(in)
		g, _ := Decode(data, ocpp.V21, Limits{})
		keep := Frame{Payload: bytes.Clone(g.Payload), Details: bytes.Clone(g.Details)}
		_ = append(g.Payload, "PAYLOAD-APPENDED"...)
		_ = append(g.Details, "DETAILS-APPENDED"...)
		if string(data) != in || !bytes.Equal(g.Payload, keep.Payload) || !bytes.Equal(g.Details, keep.Details) {
			t.Errorf("%s: an append to the Frame wrote into the input or into its other part", in)
		}
	}
}

// overlap reports whether a and b share memory.
func overlap(a, b []byte) bool {
	if cap(a) == 0 || cap(b) == 0 {
		return false
	}
	a, b = a[:cap(a)], b[:cap(b)]
	for i := range a {
		if &a[i] == &b[0] {
			return true
		}
	}
	for i := range b {
		if &b[i] == &a[0] {
			return true
		}
	}
	return false
}

// Encode copies what it writes: the encoded frame shares no memory with the
// Frame, so its payload and details may be changed or reused afterwards.
func TestEncodeDoesNotAliasFrame(t *testing.T) {
	payload := json.RawMessage(`{"idTag":"ABC"}`)
	details := json.RawMessage(`{"detail":"x"}`)
	for _, f := range []Frame{
		{Type: Call, ID: "1", Action: "Authorize", Payload: payload},
		{Type: CallResult, ID: "1", Payload: payload},
		{Type: CallError, ID: "1", ErrorCode: ocpp.GenericError, Description: "d", Details: details},
	} {
		copy(payload, `{"idTag":"ABC"}`)
		copy(details, `{"detail":"x"}`)
		out, err := Encode(f)
		if err != nil {
			t.Fatal(err)
		}
		want := string(out)
		if overlap(out, payload) || overlap(out, details) {
			t.Errorf("%s: the encoded frame shares memory with the Frame", f.Type)
		}
		copy(payload, `{"idTag":"XYZ"}`)
		copy(details, `{"detail":"y"}`)
		if string(out) != want {
			t.Errorf("%s: the encoded frame changed with the Frame: %s, want %s", f.Type, out, want)
		}
		_ = append(out, "APPENDED"...)
		if string(payload) != `{"idTag":"XYZ"}` || string(details) != `{"detail":"y"}` {
			t.Errorf("%s: an append to the encoded frame wrote into the Frame", f.Type)
		}
	}
}
