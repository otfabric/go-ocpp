// SPDX-License-Identifier: MIT

package ocppj

import (
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// The places of a frame that hold text, with %s where the test puts it.
var textPlaces = []struct {
	name    string
	v       ocpp.Version
	frame   string
	typ     MessageType
	idKnown bool // the message ID is readable when the text is elsewhere
}{
	{"message ID", ocpp.V16, `[2,"i%s","Heartbeat",{}]`, Call, false},
	{"message ID of a CALLRESULT", ocpp.V201, `[3,"i%s",{}]`, CallResult, false},
	{"action", ocpp.V16, `[2,"1","Heart%sbeat",{}]`, Call, true},
	{"action of a SEND", ocpp.V21, `[6,"1","Notify%s",{}]`, Send, true},
	{"error code", ocpp.V201, `[4,"1","Generic%sError","",{}]`, CallError, true},
	{"error description", ocpp.V201, `[4,"1","GenericError","d%s",{}]`, CallError, true},
	{"error details", ocpp.V201, `[4,"1","GenericError","",{"k":"%s"}]`, CallError, true},
	{"description of a CALLRESULTERROR", ocpp.V21, `[5,"1","GenericError","d%s",{}]`, CallResultError, true},
	{"payload value", ocpp.V16, `[2,"1","Authorize",{"idTag":"A%sB"}]`, Call, true},
	{"payload value, nested", ocpp.V201, `[3,"1",{"a":[{"b":{"c":"%s"}}]}]`, CallResult, true},
	{"payload property name", ocpp.V16, `[2,"1","Authorize",{"id%sTag":"A"}]`, Call, true},
}

// A frame whose text is not well-formed is refused, wherever the text is:
// encoding/json would decode it to U+FFFD. The message ID is reported when
// it is not the place.
func TestDecodeTextNotWellFormed(t *testing.T) {
	texts := map[string]string{
		"a byte that is not UTF-8":       "\xff",
		"a truncated UTF-8 sequence":     "\xe2\x82",
		"a surrogate encoded as UTF-8":   "\xed\xa0\x80",
		"escape of a high surrogate":     `\ud83d`,
		"escape of a low surrogate":      `\ude00`,
		"low surrogate before high":      `\ude00\ud83d`,
		"high surrogate, then a letter":  `\ud83dx`,
		"high surrogate, then an escape": `\ud83dA`,
		"upper case escape":              `\uD83D`,
	}
	for _, p := range textPlaces {
		for name, text := range texts {
			t.Run(p.name+"/"+name, func(t *testing.T) {
				in := strings.Replace(p.frame, "%s", text, 1)
				f, err := Decode([]byte(in), p.v, Limits{})
				var derr *DecodeError
				if !errors.As(err, &derr) || !errors.Is(err, ErrMalformed) {
					t.Fatalf("Decode(%q) = %+v, %v; want a *DecodeError", in, f, err)
				}
				if derr.Type != p.typ || derr.UnknownType {
					t.Errorf("Type = %s (unknown %v), want %s", derr.Type, derr.UnknownType, p.typ)
				}
				if derr.IDKnown != p.idKnown || (p.idKnown && derr.ID != "1") || (!p.idKnown && derr.ID != "") {
					t.Errorf("ID = %q (known %v), want known %v", derr.ID, derr.IDKnown, p.idKnown)
				}
				if !strings.Contains(derr.Reason, "not well-formed") {
					t.Errorf("Reason = %q", derr.Reason)
				}
			})
		}
	}
}

// Well-formed text in the same places is decoded as it is, U+FFFD included
// when the frame writes it.
func TestDecodeTextWellFormed(t *testing.T) {
	texts := map[string]string{
		"an escaped pair":           `\ud83d\ude00`,
		"a four byte character":     "\U0001F600",
		"U+FFFD literally":          "\uFFFD",
		"U+FFFD escaped":            `\ufffd`,
		"an escaped backslash":      `\\ud83d`,
		"around the surrogates":     `\ud7ff\ue000`,
		"the first and last pairs":  `\ud800\udc00\udbff\udfff`,
		"two byte characters":       "éü",
		"nothing":                   ``,
		"an escaped quote and a \\": `\"\\`,
	}
	for _, p := range textPlaces {
		for name, text := range texts {
			t.Run(p.name+"/"+name, func(t *testing.T) {
				in := strings.Replace(p.frame, "%s", text, 1)
				f, err := Decode([]byte(in), p.v, Limits{})
				if err != nil {
					t.Fatalf("Decode(%q): %v", in, err)
				}
				out, err := Encode(f)
				if err != nil {
					t.Fatalf("Encode(%+v): %v", f, err)
				}
				again, err := Decode(out, p.v, Limits{})
				if err != nil || again.ID != f.ID || again.Action != f.Action || again.ErrorCode != f.ErrorCode || again.Description != f.Description {
					t.Fatalf("round trip of %q through %q: %+v, %v", in, out, again, err)
				}
			})
		}
	}
	// What the strings hold is the text of the frame.
	f, err := Decode([]byte(`[4,"\ud83d\ude00","Generic\ufffdError","d`+"\uFFFD"+`",{}]`), ocpp.V201, Limits{})
	if err != nil || f.ID != "\U0001F600" || f.ErrorCode != "Generic\uFFFDError" || f.Description != "d\uFFFD" {
		t.Fatalf("decoded %+v, %v", f, err)
	}
}

// The other refusals keep their reasons when the text is not well-formed
// either: an unknown message type is still reported as one, which is what
// decides that the frame is dropped.
func TestDecodeTextAndOtherViolations(t *testing.T) {
	for in, want := range map[string]struct {
		unknownType bool
		reason      string
	}{
		`[9,"1","x\ud800",{}]`:   {true, "message type 9 is not defined"},
		`["2","1","x\ud800",{}]`: {false, "message type is not an integer"},
		`[2,7,"x\ud800",{}]`:     {false, "not well-formed"},
		`[2,"1","x\ud800"]`:      {false, "not well-formed"},
	} {
		_, err := Decode([]byte(in), ocpp.V201, Limits{})
		var derr *DecodeError
		if !errors.As(err, &derr) || derr.UnknownType != want.unknownType || !strings.Contains(derr.Reason, want.reason) {
			t.Errorf("Decode(%s) = %v, want %q (unknown type %v)", in, err, want.reason, want.unknownType)
		}
	}
}
