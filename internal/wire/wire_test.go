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
	var typ error = &json.UnmarshalTypeError{Value: "number", Type: reflect.TypeFor[string]()}
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
		// Integrality is decided on the decimal text, not on the nearest
		// float64: these are not integers, though they round to one.
		`{"a":1e-400}`:                 ``,
		`{"a":0.99999999999999999999}`: ``,
		`{"a":9007199254740992.5}`:     ``,
		// Beyond 2^53 the float64 would change the value.
		`{"a":9007199254740993.0}`:  ``,
		`{"a":9007199254740992.0}`:  `{"a":9007199254740992}`,
		`{"a":-9007199254740992e0}`: `{"a":-9007199254740992}`,
		// Exact integers written with many digits or a large exponent.
		`{"a":0.0e99999999999999999999}`: `{"a":0}`,
		`{"a":-0.0}`:                     `{"a":0}`,
		`{"a":120e-1,"b":1.20e1}`:        `{"a":12,"b":12}`,
		`{"a":1e-99999999999999999999}`:  ``,
		`{"a":1e99999999999999999999}`:   ``,
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
	for _, n := range []string{`0.99999999999999999999`, `1e-400`, `9007199254740993.0`} {
		o = obj{}
		if err := Unmarshal([]byte(`{"n":`+n+`}`), &o); !errors.As(err, &te) {
			t.Fatalf("%s, which is not an integer a float64 holds, decoded as %d: %v", n, o.N, err)
		}
	}
	if err := Unmarshal([]byte(`{"n":"x"}`), &o); !errors.As(err, &te) {
		t.Fatalf("a string is still refused: %v", err)
	}
	if err := Unmarshal([]byte(`{"n":`), &o); err == nil {
		t.Fatal("a syntax error is still an error")
	}
}

func TestIntegral(t *testing.T) {
	for lit, want := range map[string]int64{
		"1.0": 1, "-2e0": -2, "0e5": 0, "0.000e-7": 0, "-0.0": 0, "1.5e1": 15, "4E+2": 400,
		"9007199254740992.0": maxExact, "123.4500e2": 12345,
	} {
		if got, ok := integral(lit); !ok || got != want {
			t.Errorf("%s: %d %v, want %d", lit, got, ok, want)
		}
	}
	for _, lit := range []string{
		"1.5", "1e-1", "1e", "1e+", "0e", "0e-x", "1.2.3", "1-2", ".5", "-", "1e5000000000", "1e-5000000000",
		"9007199254740993e0", "1e16", "123456789012345678e-2", "12345678901234567e0",
	} {
		if got, ok := integral(lit); ok {
			t.Errorf("%s: integral %d", lit, got)
		}
	}
}
