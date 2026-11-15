// SPDX-License-Identifier: MIT

package wire

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// tagged is a message with one string of at most 20 characters and any
// other property.
type tagged struct {
	Tag   string         `json:"tag"`
	Extra map[string]any `json:"extra,omitempty"`
}

func (m *tagged) Validate() error { return MaxLength(CodesFor(ocpp.V201), "tag", m.Tag, 20) }

// Decode refuses a payload whose text is not well-formed with the format
// code of the version, and decodes well-formed text as it is.
func TestDecodeTextNotWellFormed(t *testing.T) {
	for _, v := range ocpp.Versions() {
		c := CodesFor(v)
		for name, payload := range map[string]string{
			"a byte that is not UTF-8":     "{\"tag\":\"A\xffB\"}",
			"a truncated UTF-8 sequence":   "{\"tag\":\"A\xe2\x82\"}",
			"escape of a high surrogate":   `{"tag":"A\ud83dB"}`,
			"escape of a low surrogate":    `{"tag":"A\ude00B"}`,
			"low surrogate before high":    `{"tag":"\ude00\ud83d"}`,
			"in another property":          `{"tag":"A","extra":{"k":["\ud83d"]}}`,
			"in a property name":           `{"tag":"A","extra":{"k\udc00":1}}`,
			"a byte that is not UTF-8 too": "{\"tag\":\"A\",\"extra\":{\"k\xc0\":1}}",
		} {
			var m tagged
			err := Decode(json.RawMessage(payload), &m, c)
			var ve *ocpp.ValidationError
			if !errors.As(err, &ve) || ve.Code != c.Format {
				t.Errorf("%s, %s: Decode(%q) = %v (decoded %q), want %s", v, name, payload, err, m.Tag, c.Format)
			}
			if err := Request(json.RawMessage(payload), &tagged{}, c); !isCallError(err, c.Format) {
				t.Errorf("%s, %s: Request(%q) = %v, want CALLERROR %s", v, name, payload, err, c.Format)
			}
		}
		for payload, want := range map[string]string{
			`{"tag":"A\ud83d\ude00B"}`:     "A\U0001F600B",
			"{\"tag\":\"A\U0001F600B\"}":   "A\U0001F600B",
			`{"tag":"A\ufffdB"}`:           "A\uFFFDB",
			"{\"tag\":\"A\uFFFDB\"}":       "A\uFFFDB",
			`{"tag":"A\\ud83dB"}`:          `A\ud83dB`,
			`{"tag":"é","extra":{"é":[]}}`: "é",
		} {
			var m tagged
			if err := Decode(json.RawMessage(payload), &m, c); err != nil || m.Tag != want {
				t.Errorf("%s: Decode(%q) = %q, %v; want %q", v, payload, m.Tag, err, want)
			}
		}
	}
	// A payload that is not JSON keeps its own reason.
	var ve *ocpp.ValidationError
	if err := Decode(json.RawMessage(`{"tag":"\ud83d`), &tagged{}, CodesFor(ocpp.V16)); !errors.As(err, &ve) || ve.Code != ocpp.FormationViolation {
		t.Errorf("a payload cut short: %v", err)
	}
}

func isCallError(err error, code ocpp.ErrorCode) bool {
	ce, ok := errors.AsType[*ocpp.CallError](err)
	return ok && ce.Code == code
}
