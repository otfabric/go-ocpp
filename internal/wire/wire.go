// SPDX-License-Identifier: MIT

// Package wire is the runtime of the generated version packages (ocpp16,
// ocpp201, ocpp21): strict decoding, validation errors with the error codes
// of a version, and the checks around a typed call. Generated code calls
// it; applications do not.
package wire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// Codes are the CALLERROR codes for validation failures, spelt as one
// version spells them.
type Codes struct {
	Format, Occurrence, Type, Property ocpp.ErrorCode
}

// CodesFor returns the codes of version v.
func CodesFor(v ocpp.Version) Codes {
	return Codes{
		Format:     ocppj.FormatErrorCode(v),
		Occurrence: ocppj.OccurrenceErrorCode(v),
		Type:       ocpp.TypeConstraintViolation,
		Property:   ocpp.PropertyConstraintViolation,
	}
}

// Message is what generated messages implement.
type Message interface {
	Validate() error
}

// Invalid returns a validation error.
func Invalid(code ocpp.ErrorCode, field, reason string) *ocpp.ValidationError {
	return &ocpp.ValidationError{Code: code, Field: field, Reason: reason}
}

// Field joins a path and a field name.
func Field(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// Index joins a path and an array index.
func Index(path string, i int) string { return path + "[" + strconv.Itoa(i) + "]" }

// Object checks the keys of the JSON object b at path: every required key
// is present, no value is null (no OCPP schema allows null: an absent
// optional property is left out, not null), and, when strict
// (additionalProperties false), no key is unknown. It does not decode the
// values.
func Object(b []byte, path string, c Codes, known, required []string, strict bool) error {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Invalid(c.Type, path, "not a JSON object")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(b, &keys); err != nil {
		return Invalid(c.Format, path, err.Error())
	}
	if strict {
		for k := range keys {
			if !contains(known, k) {
				return Invalid(c.Format, Field(path, k), "not a property of this object")
			}
		}
	}
	for _, k := range required {
		if _, ok := keys[k]; !ok {
			return Invalid(c.Occurrence, Field(path, k), "required")
		}
	}
	for k, v := range keys {
		if string(bytes.TrimSpace(v)) == "null" && contains(known, k) {
			return Invalid(c.Type, Field(path, k), "null, which the schema does not allow")
		}
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Decode unmarshals payload into m strictly and validates it. Errors are
// *ocpp.ValidationError.
func Decode(payload json.RawMessage, m Message, c Codes) error {
	if err := json.Unmarshal(payload, m); err != nil {
		return Classify(err, c)
	}
	return m.Validate()
}

// Classify turns a decoding error into a validation error.
func Classify(err error, c Codes) *ocpp.ValidationError {
	var ve *ocpp.ValidationError
	var te *json.UnmarshalTypeError
	var pe *time.ParseError
	switch {
	case errors.As(err, &ve):
		return ve
	case errors.As(err, &te):
		return Invalid(c.Type, te.Field, "a "+te.Value+" where the schema wants "+te.Type.String())
	case errors.As(err, &pe):
		return Invalid(c.Type, "", "not a date-time (RFC 3339): "+strconv.Quote(pe.Value))
	default:
		return Invalid(c.Format, "", err.Error())
	}
}

// MaxLength checks a string's length in characters, as JSON Schema counts.
func MaxLength(c Codes, path, s string, n int) error {
	if l := utf8.RuneCountInString(s); l > n {
		return Invalid(c.Property, path, fmt.Sprintf("%d characters, at most %d allowed", l, n))
	}
	return nil
}

// URI checks a string of format "uri": an absolute URI.
func URI(c Codes, path, s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" {
		return Invalid(c.Property, path, "not an absolute URI")
	}
	return nil
}

// Minimum checks v >= min.
func Minimum(c Codes, path string, v, min float64) error {
	if v < min {
		return Invalid(c.Property, path, fmt.Sprintf("%v is below the minimum %v", v, min))
	}
	return nil
}

// Maximum checks v <= max.
func Maximum(c Codes, path string, v, max float64) error {
	if v > max {
		return Invalid(c.Property, path, fmt.Sprintf("%v is above the maximum %v", v, max))
	}
	return nil
}

// MultipleOf checks that v is a multiple of m, allowing for the rounding
// of binary floating point (0.3 is a multiple of 0.1).
func MultipleOf(c Codes, path string, v, m float64) error {
	q := v / m
	if math.Abs(q-math.Round(q)) > 1e-9*math.Max(1, math.Abs(q)) {
		return Invalid(c.Property, path, fmt.Sprintf("%v is not a multiple of %v", v, m))
	}
	return nil
}

// Items checks the number of elements of an array.
func Items(c Codes, path string, n, min, max int) error {
	if n < min {
		return Invalid(c.Occurrence, path, fmt.Sprintf("%d elements, at least %d required", n, min))
	}
	if max >= 0 && n > max {
		return Invalid(c.Occurrence, path, fmt.Sprintf("%d elements, at most %d allowed", n, max))
	}
	return nil
}

// Required reports a required field that is absent (a nil slice, which
// would be encoded as null).
func Required(c Codes, path string) error { return Invalid(c.Occurrence, path, "required") }

// Enum reports a value that is not one of an enumeration's.
func Enum(c Codes, path, value string) error {
	return Invalid(c.Property, path, strconv.Quote(value)+" is not one of the allowed values")
}

// roler is implemented by client.Client and server.Session.
type roler interface{ Role() ocpp.Role }

// Call is the body of a generated call function: it checks the version and
// the direction, validates the request, makes the call and validates the
// response. from is the role that sends the action; zero for both.
func Call(ctx context.Context, c ocpp.Caller, v ocpp.Version, from ocpp.Role, action string, req, resp Message) error {
	if p, ok := c.(ocpp.Peer); ok && p.Version() != 0 && p.Version() != v {
		return fmt.Errorf("%w: %s of OCPP %s on a connection of OCPP %s", ocpp.ErrUnsupportedVersion, action, v, p.Version())
	}
	if r, ok := c.(roler); ok && from != 0 && r.Role() != from {
		return fmt.Errorf("%w: %s is sent by the %s", ocpp.ErrWrongDirection, action, from)
	}
	if err := req.Validate(); err != nil {
		return err
	}
	if err := c.Call(ctx, action, req, resp); err != nil {
		return err
	}
	if err := resp.Validate(); err != nil {
		return fmt.Errorf("%w: %s response: %w", ocpp.ErrProtocol, action, err)
	}
	return nil
}

// Respond is the end of a generated dispatcher case: it checks the
// handler's response and encodes it. An invalid response is the
// application's error, sent as InternalError.
func Respond(action string, resp Message, isNil bool, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	if isNil {
		return nil, fmt.Errorf("handler for %s returned no response", action)
	}
	if err := resp.Validate(); err != nil {
		return nil, fmt.Errorf("handler for %s returned an invalid response: %w", action, err)
	}
	return json.Marshal(resp)
}

// Request decodes and validates an incoming request, returning the
// CALLERROR to answer with when it is invalid.
func Request(payload json.RawMessage, req Message, c Codes) error {
	if err := Decode(payload, req, c); err != nil {
		var ve *ocpp.ValidationError
		if errors.As(err, &ve) {
			return ve.CallError()
		}
		return ocpp.NewCallError(c.Format, err.Error())
	}
	return nil
}
