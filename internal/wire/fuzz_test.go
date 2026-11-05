// SPDX-License-Identifier: MIT

package wire

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"regexp"
	"testing"
)

// hugeExponent matches an exponent of five digits or more, which big.Rat
// would take long to expand; the oracle skips such inputs.
var hugeExponent = regexp.MustCompile(`[eE][+-]?0*[0-9]{5,}`)

// tokens returns the JSON tokens of b, numbers as json.Number, or false
// when b is not one JSON value.
func tokens(b []byte) ([]json.Token, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out []json.Token
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, len(out) > 0
		}
		if err != nil {
			return nil, false
		}
		out = append(out, tok)
	}
}

// FuzzIntegralNumbers checks IntegralNumbers against exact arithmetic: it
// changes only numbers, a changed number has exactly the value it had, and
// that value is an integer of at most 2^53 in magnitude.
func FuzzIntegralNumbers(f *testing.F) {
	for _, s := range []string{
		`{"a":20000.0}`, `{"a":[1.0,{"b":-2.5e1}],"s":"1.0"}`, `{"a":1e-400}`, `{"a":0.99999999999999999999}`,
		`{"a":9007199254740993.0}`, `{"a":-0.0e5}`, `[1E2,"x\"1.0"]`, `{"a":120e-1}`,
	} {
		f.Add([]byte(s))
	}
	limit := new(big.Rat).SetInt64(maxExact)
	f.Fuzz(func(t *testing.T, b []byte) {
		in, ok := tokens(b)
		if !ok || !json.Valid(b) || hugeExponent.Match(b) {
			return
		}
		got, changed := IntegralNumbers(b)
		out, ok := tokens(got)
		if !ok || len(out) != len(in) {
			t.Fatalf("%s became %s, which does not parse alike", b, got)
		}
		diff := false
		for i := range in {
			n, isNum := in[i].(json.Number)
			if !isNum {
				if in[i] != out[i] {
					t.Fatalf("%s: token %v became %v", b, in[i], out[i])
				}
				continue
			}
			m := out[i].(json.Number)
			if n == m {
				continue
			}
			diff = true
			x, ok1 := new(big.Rat).SetString(string(n))
			y, ok2 := new(big.Rat).SetString(string(m))
			if !ok1 || !ok2 || x.Cmp(y) != 0 || !y.IsInt() || new(big.Rat).Abs(y).Cmp(limit) > 0 {
				t.Fatalf("%s: %s rewritten as %s", b, n, m)
			}
		}
		if diff != changed {
			t.Fatalf("%s: changed %v, but tokens differ: %v", b, changed, diff)
		}
	})
}
