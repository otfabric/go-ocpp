// SPDX-License-Identifier: MIT

// Package schematest checks a generated version package against the OCA
// JSON schemas and an independent JSON Schema validator
// (github.com/santhosh-tekuri/jsonschema), as ADR 0003 requires. It is
// imported by the version packages' tests only.
//
// For every action, request and response, it builds instances from the
// schema (with every property, and with the required ones only), checks
// that the reference validator and the package both accept them and that
// they survive a decode and encode unchanged, then breaks them one
// constraint at a time and checks that both refuse each, the package with
// the CALLERROR code that fits.
package schematest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/otfabric/go-ocpp"
)

// Config describes a version package to the suite.
type Config struct {
	Version ocpp.Version
	// Files maps an action to its request and response schema files.
	Files func(action string) (request, response string)
	// Actions lists the actions of the package.
	Actions []string
	// Validate decodes and validates a payload (ValidateRequest or
	// ValidateResponse of the package).
	Validate func(action string, request bool, payload json.RawMessage) error
	// RoundTrip decodes a payload into the package's type and encodes it
	// again.
	RoundTrip func(action string, request bool, payload json.RawMessage) (json.RawMessage, error)
}

// Run runs the suite for every action.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	for _, action := range cfg.Actions {
		reqFile, respFile := cfg.Files(action)
		for _, part := range []struct {
			name    string
			file    string
			request bool
		}{{"request", reqFile, true}, {"response", respFile, false}} {
			t.Run(action+"/"+part.name, func(t *testing.T) {
				s := load(t, part.file)
				for _, mode := range []string{"full", "minimal"} {
					inst := s.instance(s.root, mode == "full")
					s.checkValid(t, cfg, action, part.request, mode, inst)
				}
				full := s.instance(s.root, true)
				for _, m := range s.mutations(s.root, full, "") {
					s.checkInvalid(t, cfg, action, part.request, m)
				}
			})
		}
	}
}

// Instance returns a valid instance of the schema in file: with every
// property (full) or the required ones only, encoded as JSON.
func Instance(t *testing.T, file string, full bool) json.RawMessage {
	t.Helper()
	s := load(t, file)
	b, err := json.Marshal(s.instance(s.root, full))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type suiteSchema struct {
	file string
	root map[string]any
	ref  *jsonschema.Schema
}

func load(t *testing.T, file string) *suiteSchema {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	url := "file:///" + filepath.ToSlash(filepath.Base(file))
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	ref, err := c.Compile(url)
	if err != nil {
		t.Fatalf("reference validator cannot compile %s: %v", file, err)
	}
	return &suiteSchema{file: file, root: root, ref: ref}
}

func (s *suiteSchema) resolve(n map[string]any) map[string]any {
	if r, ok := n["$ref"].(string); ok {
		name := strings.TrimPrefix(r, "#/definitions/")
		return s.root["definitions"].(map[string]any)[name].(map[string]any)
	}
	return n
}

// Sample values that satisfy the constraints the schemas use.
const (
	sampleTime = "2026-01-02T03:04:05Z"
	sampleURI  = "https://example.com/files/firmware.bin"
)

// instance builds a valid value of schema n: with every property (full) or
// with the required ones only.
func (s *suiteSchema) instance(n map[string]any, full bool) any {
	n = s.resolve(n)
	if e, ok := n["enum"].([]any); ok {
		return e[len(e)-1]
	}
	switch n["type"] {
	case "object":
		out := map[string]any{}
		props, _ := n["properties"].(map[string]any)
		req := required(n)
		for name, p := range props {
			if full || req[name] {
				out[name] = s.instance(p.(map[string]any), full)
			}
		}
		if len(props) == 0 && n["additionalProperties"] != false {
			return out
		}
		return out
	case "array":
		count := 1
		if v, ok := n["minItems"].(float64); ok && int(v) > count {
			count = int(v)
		}
		items := make([]any, count)
		for i := range items {
			items[i] = s.instance(n["items"].(map[string]any), full)
		}
		return items
	case "string":
		switch n["format"] {
		case "date-time":
			return sampleTime
		case "uri":
			return sampleURI
		}
		v := "OTFabric"
		if m, ok := n["maxLength"].(float64); ok && len(v) > int(m) {
			v = v[:int(m)]
		}
		return v
	case "integer":
		return float64(number(n, 3))
	case "number":
		return number(n, 1.5)
	case "boolean":
		return true
	case nil:
		return map[string]any{"vendorField": 1.0}
	}
	panic(fmt.Sprintf("%s: unexpected schema %v", s.file, n))
}

func number(n map[string]any, preferred float64) float64 {
	v := preferred
	if m, ok := n["multipleOf"].(float64); ok {
		v = m * 15
	}
	if min, ok := n["minimum"].(float64); ok && v < min {
		v = min
	}
	if max, ok := n["maximum"].(float64); ok && v > max {
		v = max
	}
	if _, ok := n["type"].(string); ok && n["type"] == "integer" {
		v = float64(int(v))
	}
	return v
}

func required(n map[string]any) map[string]bool {
	out := map[string]bool{}
	if r, ok := n["required"].([]any); ok {
		for _, x := range r {
			out[x.(string)] = true
		}
	}
	return out
}

func (s *suiteSchema) checkValid(t *testing.T, cfg Config, action string, request bool, mode string, inst any) {
	t.Helper()
	if err := s.ref.Validate(inst); err != nil {
		t.Fatalf("%s instance is invalid for the reference validator (suite bug): %v", mode, err)
	}
	payload, _ := json.Marshal(inst)
	if err := cfg.Validate(action, request, payload); err != nil {
		t.Fatalf("%s instance %s refused: %v", mode, payload, err)
	}
	out, err := cfg.RoundTrip(action, request, payload)
	if err != nil {
		t.Fatalf("%s instance %s: round trip: %v", mode, payload, err)
	}
	var back any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if !sameJSON(inst, back) {
		t.Fatalf("%s instance changed in a round trip:\n in: %s\nout: %s", mode, payload, out)
	}
	if err := s.ref.Validate(back); err != nil {
		t.Fatalf("%s instance encoded as %s, which the reference validator refuses: %v", mode, out, err)
	}
}

// sameJSON compares decoded JSON, treating date-times as instants.
func sameJSON(a, b any) bool {
	if as, ok := a.(string); ok {
		if bs, ok := b.(string); ok {
			ta, ea := time.Parse(time.RFC3339Nano, as)
			tb, eb := time.Parse(time.RFC3339Nano, bs)
			if ea == nil && eb == nil {
				return ta.Equal(tb)
			}
		}
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		if len(am) != len(bm) {
			return false
		}
		for k, v := range am {
			if !sameJSON(v, bm[k]) {
				return false
			}
		}
		return true
	}
	al, aok := a.([]any)
	bl, bok := b.([]any)
	if aok && bok {
		if len(al) != len(bl) {
			return false
		}
		for i := range al {
			if !sameJSON(al[i], bl[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// mutation is an invalid instance and the error code it should cause.
type mutation struct {
	what string
	inst any
	code string // "occurrence", "property", "type", "format"
}

// mutations breaks inst, built from schema n, one constraint at a time,
// at every level.
func (s *suiteSchema) mutations(n map[string]any, inst any, path string) []mutation {
	n = s.resolve(n)
	var out []mutation
	switch n["type"] {
	case "object":
		obj := inst.(map[string]any)
		req := required(n)
		props, _ := n["properties"].(map[string]any)
		names := make([]string, 0, len(props))
		for k := range props {
			names = append(names, k)
		}
		sort.Strings(names)
		if n["additionalProperties"] == false {
			out = append(out, mutation{path + ": an unknown property", with(obj, "notInTheSchema", 1), "format"})
		}
		for _, name := range names {
			p := props[name].(map[string]any)
			if req[name] {
				out = append(out, mutation{path + "." + name + ": missing", without(obj, name), "occurrence"})
			}
			if s.resolve(p)["type"] != nil {
				out = append(out, mutation{path + "." + name + ": null", with(obj, name, nil), "type"})
			}
			for _, m := range s.mutations(p, obj[name], path+"."+name) {
				m.inst = with(obj, name, m.inst)
				out = append(out, m)
			}
		}
		out = append(out, mutation{path + ": not an object", "text", "type"})
	case "array":
		list := inst.([]any)
		if max, ok := n["maxItems"].(float64); ok {
			long := make([]any, int(max)+1)
			for i := range long {
				long[i] = list[0]
			}
			out = append(out, mutation{path + ": too many elements", long, "occurrence"})
		}
		if min, ok := n["minItems"].(float64); ok && min > 0 {
			out = append(out, mutation{path + ": too few elements", []any{}, "occurrence"})
		}
		for _, m := range s.mutations(n["items"].(map[string]any), list[0], path+"[0]") {
			m.inst = append([]any{m.inst}, list[1:]...)
			out = append(out, m)
		}
		out = append(out, mutation{path + ": not an array", "text", "type"})
	case "string":
		if _, ok := n["enum"]; ok {
			out = append(out, mutation{path + ": not one of the enumeration", "NotAValueOfTheSchema", "property"})
		}
		if m, ok := n["maxLength"].(float64); ok {
			out = append(out, mutation{path + ": too long", strings.Repeat("x", int(m)+1), "property"})
		}
		switch n["format"] {
		case "date-time":
			out = append(out, mutation{path + ": not a date-time", "yesterday", "type"})
		}
		out = append(out, mutation{path + ": a number for a string", 42.0, "type"})
	case "integer", "number":
		if min, ok := n["minimum"].(float64); ok {
			out = append(out, mutation{path + ": below the minimum", min - 1, "property"})
		}
		if max, ok := n["maximum"].(float64); ok {
			out = append(out, mutation{path + ": above the maximum", max + 1, "property"})
		}
		if m, ok := n["multipleOf"].(float64); ok {
			out = append(out, mutation{path + ": not a multiple", m*15 + m/2, "property"})
		}
		out = append(out, mutation{path + ": a string for a number", "42", "type"})
	case "boolean":
		out = append(out, mutation{path + ": a string for a boolean", "true", "type"})
	}
	return out
}

func with(obj map[string]any, k string, v any) map[string]any {
	out := make(map[string]any, len(obj)+1)
	for kk, vv := range obj {
		out[kk] = vv
	}
	out[k] = v
	return out
}

func without(obj map[string]any, k string) map[string]any {
	out := make(map[string]any, len(obj))
	for kk, vv := range obj {
		if kk != k {
			out[kk] = vv
		}
	}
	return out
}

func (s *suiteSchema) checkInvalid(t *testing.T, cfg Config, action string, request bool, m mutation) {
	t.Helper()
	if err := s.ref.Validate(m.inst); err == nil {
		t.Errorf("%s: the reference validator accepts the mutation (suite bug)", m.what)
		return
	}
	payload, _ := json.Marshal(m.inst)
	err := cfg.Validate(action, request, payload)
	if err == nil {
		t.Errorf("%s: accepted %s", m.what, payload)
		return
	}
	var ve *ocpp.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("%s: error %v is not a *ocpp.ValidationError", m.what, err)
		return
	}
	if want := codeFor(cfg.Version, m.code); ve.Code != want {
		t.Errorf("%s: code %s, want %s (%v)", m.what, ve.Code, want, err)
	}
}

func codeFor(v ocpp.Version, kind string) ocpp.ErrorCode {
	switch kind {
	case "occurrence":
		if v == ocpp.V16 {
			return ocpp.OccurenceConstraintViolation
		}
		return ocpp.OccurrenceConstraintViolation
	case "format":
		if v == ocpp.V16 {
			return ocpp.FormationViolation
		}
		return ocpp.FormatViolation
	case "type":
		return ocpp.TypeConstraintViolation
	default:
		return ocpp.PropertyConstraintViolation
	}
}
