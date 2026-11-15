// SPDX-License-Identifier: MIT

package jsontext_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp/internal/jsontext"
	"github.com/otfabric/go-ocpp/internal/schematest"
)

// texts are JSON documents with what WellFormed says of each. Every one is
// valid JSON for encoding/json.
var texts = []struct {
	name string
	in   string
	want bool
}{
	{"empty object", `{}`, true},
	{"ASCII", `{"a":"b","c":[1,"d",{"e":null}]}`, true},
	{"two byte character", `{"a":"é"}`, true},
	{"four byte character", `{"a":"😀"}`, true},
	{"the last code point", "{\"a\":\"\U0010FFFF\"}", true},
	{"escaped pair", `{"a":"\ud83d\ude00"}`, true},
	{"escaped pair, upper case", `{"a":"\uD83D\uDE00"}`, true},
	{"first and last surrogates as a pair", `{"a":"\ud800\udc00\udbff\udfff"}`, true},
	{"two pairs", `{"a":"\ud83d\ude00\ud83d\ude00"}`, true},
	{"U+FFFD literally", "{\"a\":\"\uFFFD\"}", true},
	{"U+FFFD escaped", `{"a":"\ufffd \uFFFD"}`, true},
	{"the code points around the surrogates", `{"a":"\ud7ff\ue000"}`, true},
	{"an escaped backslash before the letters", `{"a":"\\ud800"}`, true},
	{"two escaped backslashes", `{"a":"\\\\ud800"}`, true},
	{"an escaped quote", `{"a":"\"\\","b":"\ud83d\ude00"}`, true},
	{"other escapes", `{"a":"\n\t\/\b\f\r\u0041"}`, true},
	{"outside strings", `[1,true,null,-2.5e3]`, true},

	{"a byte that is not UTF-8 in a value", "{\"a\":\"x\xffy\"}", false},
	{"a byte that is not UTF-8 in a name", "{\"a\xff\":1}", false},
	{"a truncated sequence", "{\"a\":\"\xe2\x82\"}", false},
	{"an overlong sequence", "{\"a\":\"\xc0\xaf\"}", false},
	{"a surrogate encoded as UTF-8", "{\"a\":\"\xed\xa0\x80\"}", false},
	{"beyond the last code point", "{\"a\":\"\xf4\x90\x80\x80\"}", false},
	{"high surrogate alone", `{"a":"\ud800"}`, false},
	{"the last high surrogate alone", `{"a":"\udbff"}`, false},
	{"low surrogate alone", `{"a":"\udc00"}`, false},
	{"the last low surrogate alone", `{"a":"\udfff"}`, false},
	{"low before high", `{"a":"\ude00\ud83d"}`, false},
	{"high, then a letter", `{"a":"\ud83dx"}`, false},
	{"high, then another escape", `{"a":"\ud83d\n"}`, false},
	{"high, then a character that is no surrogate", `{"a":"\ud83d\u0041"}`, false},
	{"high, then high", `{"a":"\ud83d\ud83d"}`, false},
	{"high at the end of the string", `{"a":"x\ud83d"}`, false},
	{"high, then an escaped backslash and the letters of a low one", `{"a":"\ud83d\\ude00"}`, false},
	{"a pair, then a low surrogate", `{"a":"\ud83d\ude00\ude00"}`, false},
	{"in a property name", `{"\ud800":1}`, false},
	{"nested", `{"a":[{"b":{"c":"\udc00"}}]}`, false},
}

func TestWellFormed(t *testing.T) {
	for _, tt := range texts {
		t.Run(tt.name, func(t *testing.T) {
			if !json.Valid([]byte(tt.in)) {
				t.Fatalf("the case is not valid JSON: %q", tt.in)
			}
			if got := jsontext.WellFormed([]byte(tt.in)); got != tt.want {
				t.Errorf("WellFormed(%q) = %v, want %v", tt.in, got, tt.want)
			}
			// The reference agrees: what is refused is what encoding/json
			// would decode to other text.
			if replaced := schematest.TextReplaced([]byte(tt.in)); replaced == tt.want {
				t.Errorf("encoding/json replaces text of %q: %v, want %v", tt.in, replaced, !tt.want)
			}
		})
	}
}

// Text that is cut short, which json.Valid refuses before WellFormed is
// asked, is read without going past its end.
func TestWellFormedCutShort(t *testing.T) {
	for _, in := range []string{``, `"`, `"\`, `"\u`, `"\u1`, `"\ud83d`, `"\ud83d\`, `"\ud83d\u`, `"\ud83d\ude0`, `"\uzzzz"`, `"\ud83d\uzzzz"`} {
		for i := 0; i <= len(in); i++ {
			_ = jsontext.WellFormed([]byte(in[:i]))
		}
	}
	if jsontext.WellFormed([]byte(`"\uzzzz"`)) {
		t.Error("an escape without four hexadecimal digits is well-formed")
	}
}

func TestWellFormedDoesNotAllocate(t *testing.T) {
	in := []byte(`[2,"id","Action",{"a":"` + strings.Repeat(`é\ud83d\ude00\n`, 100) + `"}]`)
	if n := testing.AllocsPerRun(100, func() { _ = jsontext.WellFormed(in) }); n != 0 {
		t.Errorf("WellFormed allocates %v times", n)
	}
}

// FuzzWellFormed compares WellFormed with what encoding/json does: a valid
// JSON text is well-formed exactly when encoding/json decodes its strings
// without putting U+FFFD where the text has none.
func FuzzWellFormed(f *testing.F) {
	for _, tt := range texts {
		f.Add([]byte(tt.in))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		got := jsontext.WellFormed(data) // any input: it must return
		if !json.Valid(data) {
			return
		}
		if replaced := schematest.TextReplaced(data); got == replaced {
			t.Fatalf("WellFormed(%q) = %v, but encoding/json replaces text: %v", data, got, replaced)
		}
	})
}
