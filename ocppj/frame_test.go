// SPDX-License-Identifier: MIT

package ocppj

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp"
)

func TestDecodeValid(t *testing.T) {
	tests := []struct {
		name string
		v    ocpp.Version
		in   string
		want Frame
	}{
		{"call", ocpp.V16, `[2,"19223201","BootNotification",{"chargePointVendor":"VendorX","chargePointModel":"SingleSocketCharger"}]`,
			Frame{Type: Call, ID: "19223201", Action: "BootNotification", Payload: json.RawMessage(`{"chargePointVendor":"VendorX","chargePointModel":"SingleSocketCharger"}`)}},
		{"call with whitespace", ocpp.V201, " [ 2 , \"a\" , \"Heartbeat\" , { } ] \n",
			Frame{Type: Call, ID: "a", Action: "Heartbeat", Payload: json.RawMessage(`{ }`)}},
		{"callresult", ocpp.V16, `[3,"19223201",{"status":"Accepted","currentTime":"2013-02-01T20:53:32.486Z","interval":300}]`,
			Frame{Type: CallResult, ID: "19223201", Payload: json.RawMessage(`{"status":"Accepted","currentTime":"2013-02-01T20:53:32.486Z","interval":300}`)}},
		{"callerror", ocpp.V201, `[4,"162376037","NotSupported","SetDisplayMessageRequest not implemented",{}]`,
			Frame{Type: CallError, ID: "162376037", ErrorCode: ocpp.NotSupported, Description: "SetDisplayMessageRequest not implemented", Details: json.RawMessage(`{}`)}},
		{"callerror empty description", ocpp.V16, `[4,"1","GenericError","",{"k":1}]`,
			Frame{Type: CallError, ID: "1", ErrorCode: ocpp.GenericError, Details: json.RawMessage(`{"k":1}`)}},
		{"callresulterror", ocpp.V21, `[5,"1","FormatViolation","bad",{}]`,
			Frame{Type: CallResultError, ID: "1", ErrorCode: ocpp.FormatViolation, Description: "bad", Details: json.RawMessage(`{}`)}},
		{"send", ocpp.V21, `[6,"1","NotifyPeriodicEventStream",{"id":1}]`,
			Frame{Type: Send, ID: "1", Action: "NotifyPeriodicEventStream", Payload: json.RawMessage(`{"id":1}`)}},
		{"36 character id", ocpp.V16, `[2,"` + strings.Repeat("x", 36) + `","Heartbeat",{}]`,
			Frame{Type: Call, ID: strings.Repeat("x", 36), Action: "Heartbeat", Payload: json.RawMessage(`{}`)}},
		{"36 multibyte characters", ocpp.V16, `[2,"` + strings.Repeat("é", 36) + `","Heartbeat",{}]`,
			Frame{Type: Call, ID: strings.Repeat("é", 36), Action: "Heartbeat", Payload: json.RawMessage(`{}`)}},
		{"escaped id", ocpp.V16, `[3,"a\"bA",{}]`,
			Frame{Type: CallResult, ID: `a"bA`, Payload: json.RawMessage(`{}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Decode([]byte(tt.in), tt.v, Limits{})
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got.Type != tt.want.Type || got.ID != tt.want.ID || got.Action != tt.want.Action ||
				got.ErrorCode != tt.want.ErrorCode || got.Description != tt.want.Description ||
				!bytes.Equal(got.Payload, tt.want.Payload) || !bytes.Equal(got.Details, tt.want.Details) {
				t.Errorf("Decode = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDecodeInvalid(t *testing.T) {
	tests := []struct {
		name        string
		v           ocpp.Version
		in          string
		idKnown     bool
		id          string
		unknownType bool
	}{
		{"empty", ocpp.V16, ``, false, "", false},
		{"not json", ocpp.V16, `hello`, false, "", false},
		{"object", ocpp.V16, `{"a":1}`, false, "", false},
		{"null", ocpp.V16, `null`, false, "", false},
		{"one element", ocpp.V16, `[2]`, false, "", false},
		{"trailing data", ocpp.V16, `[2,"1","Heartbeat",{}] x`, false, "", false},
		{"two arrays", ocpp.V16, `[2,"1","Heartbeat",{}][2,"1","Heartbeat",{}]`, false, "", false},
		{"type as string", ocpp.V16, `["2","1","Heartbeat",{}]`, true, "1", false},
		{"type as float", ocpp.V16, `[2.0,"1","Heartbeat",{}]`, true, "1", false},
		{"type with exponent", ocpp.V16, `[2e0,"1","Heartbeat",{}]`, true, "1", false},
		{"negative type", ocpp.V16, `[-2,"1","Heartbeat",{}]`, true, "1", false},
		{"type out of range", ocpp.V16, `[1002,"1","Heartbeat",{}]`, true, "1", false},
		{"type far out of range", ocpp.V16, `[99999999999999999999,"1","Heartbeat",{}]`, true, "1", false},
		{"type 258 is not 2", ocpp.V16, `[258,"1","Heartbeat",{}]`, true, "1", false},
		{"null id", ocpp.V16, `[2,null,"Heartbeat",{}]`, false, "", false},
		{"unknown type", ocpp.V16, `[7,"1","Heartbeat",{}]`, true, "1", true},
		{"send in 1.6", ocpp.V16, `[6,"1","Heartbeat",{}]`, true, "1", true},
		{"send in 2.0.1", ocpp.V201, `[6,"1","Heartbeat",{}]`, true, "1", true},
		{"callresulterror in 2.0.1", ocpp.V201, `[5,"1","GenericError","",{}]`, true, "1", true},
		{"numeric id", ocpp.V16, `[2,1,"Heartbeat",{}]`, false, "", false},
		{"id too long", ocpp.V16, `[2,"` + strings.Repeat("x", 37) + `","Heartbeat",{}]`, false, "", false},
		{"call missing payload", ocpp.V16, `[2,"1","Heartbeat"]`, true, "1", false},
		{"call extra element", ocpp.V16, `[2,"1","Heartbeat",{},{}]`, true, "1", false},
		{"call payload array", ocpp.V16, `[2,"1","Heartbeat",[]]`, true, "1", false},
		{"call payload null", ocpp.V16, `[2,"1","Heartbeat",null]`, true, "1", false},
		{"call empty action", ocpp.V16, `[2,"1","",{}]`, true, "1", false},
		{"call numeric action", ocpp.V16, `[2,"1",5,{}]`, true, "1", false},
		{"callresult payload string", ocpp.V16, `[3,"1","ok"]`, true, "1", false},
		{"callerror four elements", ocpp.V16, `[4,"1","GenericError",""]`, true, "1", false},
		{"callerror details null", ocpp.V16, `[4,"1","GenericError","",null]`, true, "1", false},
		{"callerror numeric description", ocpp.V16, `[4,"1","GenericError",3,{}]`, true, "1", false},
		{"callerror empty code", ocpp.V16, `[4,"1","","",{}]`, true, "1", false},
		{"invalid nested json", ocpp.V16, `[2,"1","Heartbeat",{"a":}]`, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.in), tt.v, Limits{})
			var derr *DecodeError
			if !errors.As(err, &derr) {
				t.Fatalf("Decode error = %v, want *DecodeError", err)
			}
			if !errors.Is(err, ErrMalformed) {
				t.Errorf("error does not wrap ErrMalformed")
			}
			if derr.IDKnown != tt.idKnown || derr.ID != tt.id {
				t.Errorf("ID = %q (known %v), want %q (known %v)", derr.ID, derr.IDKnown, tt.id, tt.idKnown)
			}
			if derr.UnknownType != tt.unknownType {
				t.Errorf("UnknownType = %v, want %v", derr.UnknownType, tt.unknownType)
			}
		})
	}
}

func TestDecodeSizeLimit(t *testing.T) {
	frame := []byte(`[2,"1","DataTransfer",{"vendorId":"` + strings.Repeat("x", 100) + `"}]`)
	if _, err := Decode(frame, ocpp.V16, Limits{MaxFrameSize: len(frame)}); err != nil {
		t.Fatalf("frame at the limit: %v", err)
	}
	_, err := Decode(frame, ocpp.V16, Limits{MaxFrameSize: len(frame) - 1})
	var derr *DecodeError
	if !errors.As(err, &derr) || derr.IDKnown {
		t.Fatalf("frame over the limit: err = %v", err)
	}
	big := make([]byte, DefaultMaxFrameSize+1)
	if _, err := Decode(big, ocpp.V16, Limits{}); err == nil {
		t.Fatal("frame over the default limit accepted")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	frames := []Frame{
		{Type: Call, ID: "1", Action: "Heartbeat"},
		{Type: Call, ID: "2", Action: "Authorize", Payload: json.RawMessage(`{"idTag":"<&>"}`)},
		{Type: CallResult, ID: "3", Payload: json.RawMessage(`{"currentTime":"2026-01-01T00:00:00Z"}`)},
		{Type: CallError, ID: "4", ErrorCode: ocpp.NotImplemented, Description: "no \"handler\""},
		{Type: CallResultError, ID: "5", ErrorCode: ocpp.GenericError, Details: json.RawMessage(`{"x":[1,2]}`)},
		{Type: Send, ID: "6", Action: "NotifyPeriodicEventStream", Payload: json.RawMessage(`{"id":1}`)},
	}
	for _, f := range frames {
		data, err := Encode(f)
		if err != nil {
			t.Fatalf("Encode(%+v): %v", f, err)
		}
		got, err := Decode(data, ocpp.V21, Limits{})
		if err != nil {
			t.Fatalf("Decode(%s): %v", data, err)
		}
		wantPayload, wantDetails := f.Payload, f.Details
		if f.Type == Call || f.Type == CallResult || f.Type == Send {
			if wantPayload == nil {
				wantPayload = json.RawMessage(`{}`)
			}
		} else if wantDetails == nil {
			wantDetails = json.RawMessage(`{}`)
		}
		if got.Type != f.Type || got.ID != f.ID || got.Action != f.Action || got.ErrorCode != f.ErrorCode ||
			got.Description != f.Description || !bytes.Equal(got.Payload, wantPayload) || !bytes.Equal(got.Details, wantDetails) {
			t.Errorf("round trip of %+v gave %+v (%s)", f, got, data)
		}
	}
	if data, _ := Encode(frames[1]); !bytes.Contains(data, []byte(`<&>`)) {
		t.Errorf("Encode escaped HTML: %s", data)
	}
}

func TestEncodeRejects(t *testing.T) {
	for _, f := range []Frame{
		{Type: 0, ID: "1"},
		{Type: 9, ID: "1"},
		{Type: Call, ID: "1"},
		{Type: Call, ID: strings.Repeat("x", 37), Action: "Heartbeat"},
		{Type: Call, ID: "1", Action: "Heartbeat", Payload: json.RawMessage(`[]`)},
		{Type: Call, ID: "1", Action: "Heartbeat", Payload: json.RawMessage(`{`)},
		{Type: CallResult, ID: "1", Payload: json.RawMessage(`[]`)},
		{Type: CallError, ID: "1"},
		{Type: CallError, ID: "1", ErrorCode: ocpp.GenericError, Details: json.RawMessage(`"x"`)},
	} {
		if data, err := Encode(f); err == nil {
			t.Errorf("Encode(%+v) = %s, want error", f, data)
		}
	}
}

func TestErrorCodes(t *testing.T) {
	if !ValidErrorCode(ocpp.V16, ocpp.FormationViolation) || ValidErrorCode(ocpp.V16, ocpp.FormatViolation) {
		t.Error("1.6 must have FormationViolation and not FormatViolation")
	}
	if !ValidErrorCode(ocpp.V16, ocpp.OccurenceConstraintViolation) || ValidErrorCode(ocpp.V16, ocpp.OccurrenceConstraintViolation) {
		t.Error("1.6 spells OccurenceConstraintViolation with one r")
	}
	for _, v := range []ocpp.Version{ocpp.V201, ocpp.V21} {
		for _, c := range []ocpp.ErrorCode{ocpp.FormatViolation, ocpp.OccurrenceConstraintViolation, ocpp.MessageTypeNotSupported, ocpp.RpcFrameworkError} {
			if !ValidErrorCode(v, c) {
				t.Errorf("%s lacks %s", v, c)
			}
		}
		if ValidErrorCode(v, ocpp.FormationViolation) {
			t.Errorf("%s has FormationViolation", v)
		}
	}
	if len(ErrorCodes(0)) != 0 {
		t.Error("an invalid version has error codes")
	}
	if FormatErrorCode(ocpp.V16) != ocpp.FormationViolation || FormatErrorCode(ocpp.V201) != ocpp.FormatViolation {
		t.Error("FormatErrorCode")
	}
	if OccurrenceErrorCode(ocpp.V16) != ocpp.OccurenceConstraintViolation || OccurrenceErrorCode(ocpp.V21) != ocpp.OccurrenceConstraintViolation {
		t.Error("OccurrenceErrorCode")
	}
	if RPCErrorCode(ocpp.V16) != ocpp.FormationViolation || RPCErrorCode(ocpp.V201) != ocpp.RpcFrameworkError {
		t.Error("RPCErrorCode")
	}
	for _, v := range ocpp.Versions() {
		for _, c := range []ocpp.ErrorCode{FormatErrorCode(v), OccurrenceErrorCode(v), RPCErrorCode(v)} {
			if !ValidErrorCode(v, c) {
				t.Errorf("helper returned %s, which %s does not define", c, v)
			}
		}
	}
}

func TestMessageTypeString(t *testing.T) {
	for typ, want := range map[MessageType]string{Call: "CALL", CallResult: "CALLRESULT", CallError: "CALLERROR",
		CallResultError: "CALLRESULTERROR", Send: "SEND", 9: "MessageType(9)"} {
		if got := typ.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", typ, got, want)
		}
	}
}
