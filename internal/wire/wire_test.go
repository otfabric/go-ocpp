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

func TestIntegralNumbers(t *testing.T) {
	for in, want := range map[string]string{
		`{"a":20000.0}`:                  `{"a":20000}`,
		`{"a":2e4,"b":-3.00,"c":1E2}`:    `{"a":20000,"b":-3,"c":100}`,
		`{"a":2.5e1}`:                    `{"a":25}`,
		`{"a":[1.0,{"b":0.0}]}`:          `{"a":[1,{"b":0}]}`,
		`{"a":"20000.0","b\"x":"1.0\\"}`: ``, // strings untouched: no change
		`{"a":20000.5}`:                  ``,
		`{"a":1.5e-1}`:                   ``,
		`{"a":12345678901234567890.0}`:   ``, // beyond 2^53
		`{"a":20000}`:                    ``,
		`{"s":"x\"y 1.0","n":1.0}`:       `{"s":"x\"y 1.0","n":1}`,
	} {
		got, changed := IntegralNumbers([]byte(in))
		if want == "" {
			if changed || string(got) != in {
				t.Errorf("%s: changed to %s", in, got)
			}
			continue
		}
		if !changed || string(got) != want {
			t.Errorf("%s: %s (changed %v), want %s", in, got, changed, want)
		}
	}
}

func TestUnmarshalIntegralFloats(t *testing.T) {
	type obj struct {
		N   int             `json:"n"`
		F   float64         `json:"f"`
		Raw json.RawMessage `json:"raw"`
	}
	var o obj
	if err := Unmarshal([]byte(`{"n":20000.0,"f":1.5,"raw":{"x":2.0}}`), &o); err != nil || o.N != 20000 || o.F != 1.5 {
		t.Fatalf("integral float: %v %+v", err, o)
	}
	o = obj{}
	if err := Unmarshal([]byte(`{"n":7,"f":2.0,"raw":{"x":2.0}}`), &o); err != nil || string(o.Raw) != `{"x":2.0}` {
		t.Fatalf("a payload that decodes as it is is not rewritten: %v %s", err, o.Raw)
	}
	var te *json.UnmarshalTypeError
	if err := Unmarshal([]byte(`{"n":20000.5}`), &o); !errors.As(err, &te) {
		t.Fatalf("a fraction is still refused: %v", err)
	}
	if err := Unmarshal([]byte(`{"n":"x"}`), &o); !errors.As(err, &te) {
		t.Fatalf("a string is still refused: %v", err)
	}
	if err := Unmarshal([]byte(`{"n":`), &o); err == nil {
		t.Fatal("a syntax error is still an error")
	}
}
