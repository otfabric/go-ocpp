// SPDX-License-Identifier: MIT

package wire

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
)

// The classification of decoding errors is the same on every Go release,
// whatever error type the standard library returns.
func TestClassify(t *testing.T) {
	c := CodesFor(ocpp.V201)
	var syntax error = &json.SyntaxError{}
	var typ error = &json.UnmarshalTypeError{Value: "number", Type: reflect.TypeOf("")}
	_, parse := time.Parse(time.RFC3339, "yesterday")
	for name, tt := range map[string]struct {
		err  error
		code ocpp.ErrorCode
	}{
		"validation":           {Invalid(c.Occurrence, "x", "required"), c.Occurrence},
		"syntax":               {syntax, c.Format},
		"type":                 {typ, c.Type},
		"time parse":           {parse, c.Type},
		"plain (Go 1.26 time)": {errors.New("Time.UnmarshalJSON: input is not a JSON string"), c.Type},
	} {
		if got := Classify(tt.err, c).Code; got != tt.code {
			t.Errorf("%s: %s, want %s", name, got, tt.code)
		}
	}
	var v struct{ T time.Time }
	err := json.Unmarshal([]byte(`{"T":5}`), &v)
	if got := Classify(err, c).Code; got != c.Type {
		t.Errorf("a number for a date-time on %T: %s, want %s", err, got, c.Type)
	}
}
