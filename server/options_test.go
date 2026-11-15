// SPDX-License-Identifier: MIT

package server

import (
	"errors"
	"math"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// The rate of WithIncomingRateLimit is a finite number that is not
// negative: New refuses anything else with ErrInvalidOption, and accepts
// every other float64, the smallest and the largest included.
func TestIncomingRateOption(t *testing.T) {
	for name, tt := range map[string]struct {
		rate float64
		ok   bool
	}{
		"zero":               {0, true},
		"negative zero":      {math.Copysign(0, -1), true},
		"smallest positive":  {math.SmallestNonzeroFloat64, true},
		"1e-300":             {1e-300, true},
		"one":                {1, true},
		"largest":            {math.MaxFloat64, true},
		"smallest negative":  {-math.SmallestNonzeroFloat64, false},
		"minus one":          {-1, false},
		"most negative":      {-math.MaxFloat64, false},
		"not a number":       {math.NaN(), false},
		"positive infinity":  {math.Inf(1), false},
		"negative infinity":  {math.Inf(-1), false},
		"not a number, sign": {math.Copysign(math.NaN(), -1), false},
	} {
		_, err := New(WithHandler(ocpp.V16, ocpp.DispatcherFunc(nil)), WithoutAuthentication(), WithIncomingRateLimit(tt.rate, 1))
		switch {
		case tt.ok && err != nil:
			t.Errorf("%s (%v): refused: %v", name, tt.rate, err)
		case !tt.ok && !errors.Is(err, ocpp.ErrInvalidOption):
			t.Errorf("%s (%v): New returned %v, want ErrInvalidOption", name, tt.rate, err)
		}
	}
}
