// SPDX-License-Identifier: MIT

package schematest

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// IntegerField is an integer property of a request of a version package,
// for CheckIntegerRange.
type IntegerField struct {
	// Action is the action whose request holds the property.
	Action string
	// Field is the property's path, as ocpp.ValidationError names it.
	Field string
	// Payload is a valid request with %s in place of the property's value.
	Payload string
	// Draft06 is true when the request's schema is JSON Schema draft-06, in
	// which a number with a zero fractional part (1.0) is an integer.
	Draft06 bool
	// Validate is the package's ValidateRequest.
	Validate func(action string, payload json.RawMessage) error
	// Decode unmarshals a payload into the package's request type and
	// returns the property.
	Decode func(payload json.RawMessage) (int64, error)
}

// CheckIntegerRange runs the boundary table of a schema integer on f. The
// generated types hold a schema integer in a Go int, so a number decodes
// when it fits the int of the platform (strconv.IntSize: 64 bits on 64-bit
// builds, 32 bits on 32-bit builds) and is refused with
// TypeConstraintViolation, naming the field, when it does not. Each row
// gives the narrowest int that holds its number, and the test expects the
// result of the platform it runs on.
func CheckIntegerRange(t *testing.T, f IntegerField) {
	t.Helper()
	// bits is the narrowest int the number decodes into, 32 or 64, and 0
	// for none. fraction marks a number written with a zero fractional
	// part, an integer in draft-06 schemas only. Such a number is written
	// as an integer only up to ±2^53 (wire.IntegralNumbers), so the 64-bit
	// edges are refused in that form on every platform.
	table := []struct {
		text     string
		value    int64
		bits     int
		fraction bool
	}{
		{"0", 0, 32, false},
		{"1", 1, 32, false},
		{"-1", -1, 32, false},
		{"2147483646", math.MaxInt32 - 1, 32, false},
		{"2147483647", math.MaxInt32, 32, false},
		{"2147483648", math.MaxInt32 + 1, 64, false},
		{"-2147483648", math.MinInt32, 32, false},
		{"-2147483649", math.MinInt32 - 1, 64, false},
		{"9223372036854775806", math.MaxInt64 - 1, 64, false},
		{"9223372036854775807", math.MaxInt64, 64, false},
		{"9223372036854775808", 0, 0, false},
		{"-9223372036854775808", math.MinInt64, 64, false},
		{"-9223372036854775809", 0, 0, false},
		{"1.0", 1, 32, true},
		{"2147483647.0", math.MaxInt32, 32, true},
		{"2147483648.0", math.MaxInt32 + 1, 64, true},
		{"-2147483648.0", math.MinInt32, 32, true},
		{"-2147483649.0", math.MinInt32 - 1, 64, true},
		{"9223372036854775807.0", 0, 0, true},
		{"-9223372036854775808.0", 0, 0, true},
	}
	for _, n := range table {
		want := n.bits != 0 && n.bits <= strconv.IntSize && (f.Draft06 || !n.fraction)
		t.Run(f.Action+"."+f.Field+"="+n.text, func(t *testing.T) {
			payload := json.RawMessage(fmt.Sprintf(f.Payload, n.text))
			err := f.Validate(f.Action, payload)
			got, decodeErr := f.Decode(payload)
			if want {
				if err != nil || decodeErr != nil || got != n.value {
					t.Fatalf("%d-bit int: want %d, got %d (validate: %v; decode: %v)", strconv.IntSize, n.value, got, err, decodeErr)
				}
				return
			}
			var ve *ocpp.ValidationError
			if !errors.As(err, &ve) || ve.Code != ocpp.TypeConstraintViolation || ve.Field != f.Field {
				t.Fatalf("%d-bit int: want TypeConstraintViolation at %s, got %v", strconv.IntSize, f.Field, err)
			}
			if decodeErr == nil {
				t.Fatalf("%d-bit int: the request type decodes it to %d", strconv.IntSize, got)
			}
		})
	}
}
