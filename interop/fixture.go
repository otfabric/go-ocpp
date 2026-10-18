//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
)

// fixture is the part of an ocpp-interop fixture the tests use. It is read
// from the image (print-fixture), so the expectations cannot drift from
// what the image serves.
type fixture struct {
	version  ocpp.Version
	Stations []struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	} `json:"stations"`
	IDTokens []struct {
		IDToken string `json:"idToken"`
		Type    string `json:"type"`
		Status  string `json:"status"`
	} `json:"idTokens"`
	Variables     []fixtureVariable `json:"variables"`
	Configuration []struct {
		Key      string `json:"key"`
		Value    string `json:"value"`
		Readonly bool   `json:"readonly"`
	} `json:"configuration"`
	Answers struct {
		CSMS    map[string]answer `json:"csms"`
		Station map[string]answer `json:"station"`
	} `json:"answers"`
	Samples map[string]sample `json:"samples"`
}

type answer struct {
	Payload   json.RawMessage `json:"payload"`
	Derived   string          `json:"derived"`
	CallError *struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	} `json:"callError"`
}

type sample struct {
	Action  string          `json:"action"`
	From    string          `json:"from"` // "station" or "csms"
	Payload json.RawMessage `json:"payload"`
}

var fixtures sync.Map // image + version -> *fixture

func (a adapter) fixture(t testing.TB, v ocpp.Version) *fixture {
	t.Helper()
	key := a.image + " " + v.String()
	if f, ok := fixtures.Load(key); ok {
		return f.(*fixture)
	}
	out, stderr, err := docker("run", "--rm", a.image, "print-fixture", "--version", v.String())
	if err != nil {
		t.Fatalf("print-fixture of %s: %v\n%s", a.image, err, stderr)
	}
	f := &fixture{version: v}
	if err := json.Unmarshal([]byte(out), f); err != nil {
		t.Fatalf("fixture of %s: %v", a.image, err)
	}
	fixtures.Store(key, f)
	return f
}

func (f *fixture) station() (id, password string) { return f.Stations[0].ID, f.Stations[0].Password }

// samplesFrom returns the names of the samples a role sends, sorted.
func (f *fixture) samplesFrom(from string) []string {
	var names []string
	for name, s := range f.Samples {
		if s.From == from {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// answerTable returns the answers of the role that receives CALLs from
// "from": the CSMS answers the station, and the station the CSMS.
func (f *fixture) answerTable(from string) map[string]answer {
	if from == "station" {
		return f.Answers.CSMS
	}
	return f.Answers.Station
}

const now = "${now}"

// answer computes the fixture's answer to a CALL, as docs/FIXTURES.md of
// ocpp-interop defines it. A nil payload with a nil error means the action
// has no answer: it is answered with NotImplemented.
//
// values holds the current value of each device model variable, by key;
// SetVariables changes it. Nil uses the fixture's values and changes nothing.
func (f *fixture) answer(table map[string]answer, action string, req json.RawMessage, values map[string]string) (map[string]any, *ocpp.CallError) {
	a, ok := table[action]
	if !ok {
		return nil, nil
	}
	if a.CallError != nil {
		return nil, ocpp.NewCallError(ocpp.ErrorCode(a.CallError.Code), a.CallError.Description)
	}
	out := map[string]any{}
	if len(a.Payload) > 0 {
		_ = json.Unmarshal(a.Payload, &out)
	}
	switch a.Derived {
	case "idTokens":
		var r struct {
			IDTag   string `json:"idTag"`
			IDToken struct {
				IDToken string `json:"idToken"`
				Type    string `json:"type"`
			} `json:"idToken"`
		}
		_ = json.Unmarshal(req, &r)
		status := "Invalid"
		for _, tok := range f.IDTokens {
			// In 2.x a token matches by value and type.
			if f.version == ocpp.V16 && tok.IDToken == r.IDTag ||
				f.version != ocpp.V16 && tok.IDToken == r.IDToken.IDToken && tok.Type == r.IDToken.Type {
				status = tok.Status
			}
		}
		field := "idTagInfo"
		if f.version != ocpp.V16 {
			field = "idTokenInfo"
		}
		out[field] = map[string]any{"status": status}
	case "variables":
		f.variables(action, req, values, out)
	case "configuration":
		var r struct {
			Key []string `json:"key"`
		}
		_ = json.Unmarshal(req, &r)
		var known []any
		var unknown []any
		if len(r.Key) == 0 {
			for _, c := range f.Configuration {
				known = append(known, map[string]any{"key": c.Key, "value": c.Value, "readonly": c.Readonly})
			}
		}
		for _, k := range r.Key {
			found := false
			for _, c := range f.Configuration {
				if strings.EqualFold(c.Key, k) {
					known = append(known, map[string]any{"key": c.Key, "value": c.Value, "readonly": c.Readonly})
					found = true
				}
			}
			if !found {
				unknown = append(unknown, k)
			}
		}
		if known != nil {
			out["configurationKey"] = known
		}
		if unknown != nil {
			out["unknownKey"] = unknown
		}
	}
	return out, nil
}

// dispatcher answers CALLs as the fixture's table for the receiving role
// says, with "${now}" replaced by the current time, and records every CALL
// it receives, with what go-ocpp's typed validation says about it.
type dispatcher struct {
	f       *fixture
	table   map[string]answer
	version ocpp.Version
	mu      sync.Mutex
	values  map[string]string            // device model values, changed by SetVariables
	got     map[string][]json.RawMessage // action -> payloads received
	invalid map[string][]error           // action -> validation errors
}

func newDispatcher(f *fixture, table map[string]answer, v ocpp.Version) *dispatcher {
	values := map[string]string{}
	for _, fv := range f.Variables {
		values[fv.key()] = fv.Value
	}
	return &dispatcher{f: f, table: table, version: v, values: values, got: map[string][]json.RawMessage{}, invalid: map[string][]error{}}
}

func (d *dispatcher) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	d.mu.Lock()
	d.got[action] = append(d.got[action], append(json.RawMessage(nil), payload...))
	if err := validateRequest(d.version, action, payload); err != nil {
		d.invalid[action] = append(d.invalid[action], err)
	}
	d.mu.Unlock()
	d.mu.Lock()
	out, cerr := d.f.answer(d.table, action, payload, d.values)
	d.mu.Unlock()
	if cerr != nil {
		return nil, cerr
	}
	if out == nil {
		return nil, ocpp.NewCallError(ocpp.NotImplemented, "")
	}
	b, err := json.Marshal(replaceNow(out))
	return b, err
}

// validateRequest and validateResponse are go-ocpp's typed validation of a
// version: what a reference sends must pass it.
func validateRequest(v ocpp.Version, action string, payload json.RawMessage) error {
	switch v {
	case ocpp.V21:
		return ocpp21.ValidateRequest(action, payload)
	case ocpp.V201:
		return ocpp201.ValidateRequest(action, payload)
	case ocpp.V16:
	}
	return ocpp16.ValidateRequest(action, payload)
}

func validateResponse(v ocpp.Version, action string, payload json.RawMessage) error {
	switch v {
	case ocpp.V21:
		return ocpp21.ValidateResponse(action, payload)
	case ocpp.V201:
		return ocpp201.ValidateResponse(action, payload)
	case ocpp.V16:
	}
	return ocpp16.ValidateResponse(action, payload)
}

// received returns the payloads of action that arrived.
func (d *dispatcher) received(action string) []json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]json.RawMessage(nil), d.got[action]...)
}

func replaceNow(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = replaceNow(e)
		}
	case []any:
		for i, e := range x {
			x[i] = replaceNow(e)
		}
	case string:
		if x == now {
			return time.Now().UTC().Format(time.RFC3339)
		}
	}
	return v
}

// contains checks that got has everything want has: every key of an object
// with an equal value, arrays element by element, numbers by value, and
// "${now}" as any RFC 3339 time within a few minutes of now. An upstream
// may add optional fields with their defaults (ocpp-interop contract,
// Events), so extra keys in got are allowed.
func contains(want any, got any) error {
	return containsAt("", want, got)
}

func containsAt(path string, want, got any) error {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: want an object, got %v", orRoot(path), got)
		}
		keys := make([]string, 0, len(w))
		for k := range w {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			gv, ok := g[k]
			if !ok {
				return fmt.Errorf("%s.%s: missing", orRoot(path), k)
			}
			if err := containsAt(path+"."+k, w[k], gv); err != nil {
				return err
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return fmt.Errorf("%s: want %d elements, got %v", orRoot(path), len(w), got)
		}
		for i := range w {
			if err := containsAt(fmt.Sprintf("%s[%d]", path, i), w[i], g[i]); err != nil {
				return err
			}
		}
	case float64:
		g, ok := got.(float64)
		if !ok || math.Abs(g-w) > 1e-9 {
			return fmt.Errorf("%s: want %v, got %v", orRoot(path), w, got)
		}
	case string:
		g, ok := got.(string)
		if w == now {
			ts, err := time.Parse(time.RFC3339, g)
			if !ok || err != nil || time.Since(ts).Abs() > 5*time.Minute {
				return fmt.Errorf("%s: want the current time, got %v", orRoot(path), got)
			}
			return nil
		}
		if ok && g != w {
			// Upstreams write timestamps differently (milliseconds, offsets):
			// two times are equal if they are the same instant.
			tw, errW := time.Parse(time.RFC3339Nano, w)
			tg, errG := time.Parse(time.RFC3339Nano, g)
			if errW == nil && errG == nil && tw.Equal(tg) {
				return nil
			}
		}
		if !ok || g != w {
			return fmt.Errorf("%s: want %q, got %v", orRoot(path), w, got)
		}
	default:
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return fmt.Errorf("%s: want %v, got %v", orRoot(path), want, got)
		}
	}
	return nil
}

func orRoot(p string) string {
	if p == "" {
		return "(root)"
	}
	return strings.TrimPrefix(p, ".")
}

// decode unmarshals JSON for contains.
func decode(t testing.TB, raw json.RawMessage) any {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, raw)
	}
	return v
}

// expected returns the answer the fixture defines for a sample, as JSON
// values for contains, or ok=false when the sample's action has no answer.
func (f *fixture) expected(t testing.TB, s sample) (want any, callErr *ocpp.CallError, ok bool) {
	t.Helper()
	out, cerr := f.answer(f.answerTable(s.From), s.Action, s.Payload, nil)
	if cerr != nil {
		return nil, cerr, true
	}
	if out == nil {
		return nil, nil, false
	}
	b, _ := json.Marshal(out)
	return decode(t, b), nil, true
}

// fixtureVariable is a device model variable of a 2.x fixture.
type fixtureVariable struct {
	Component  named  `json:"component"`
	Variable   named  `json:"variable"`
	Value      string `json:"value"`
	Mutability string `json:"mutability"`
}

type named struct {
	Name     string  `json:"name"`
	Instance *string `json:"instance"`
}

func (n named) key() string {
	k := strings.ToLower(n.Name)
	if n.Instance != nil {
		k += "/" + strings.ToLower(*n.Instance)
	}
	return k
}

func (v fixtureVariable) key() string { return v.Component.key() + "|" + v.Variable.key() }

// variables answers GetVariables and SetVariables as ocpp-interop's
// docs/FIXTURES.md defines: one result per requested item, in order, with
// the component, variable and attributeType of the request, and a status
// checked in this order: UnknownComponent, UnknownVariable,
// NotSupportedAttributeType (anything but Actual), Rejected (reading
// WriteOnly, writing ReadOnly), Accepted. Names compare case-insensitively.
func (f *fixture) variables(action string, req json.RawMessage, values map[string]string, out map[string]any) {
	type item struct {
		Component      json.RawMessage `json:"component"`
		Variable       json.RawMessage `json:"variable"`
		AttributeType  *string         `json:"attributeType"`
		AttributeValue string          `json:"attributeValue"`
	}
	var r struct {
		Get []item `json:"getVariableData"`
		Set []item `json:"setVariableData"`
	}
	_ = json.Unmarshal(req, &r)
	items, resultKey := r.Get, "getVariableResult"
	if action == "SetVariables" {
		items, resultKey = r.Set, "setVariableResult"
	}
	results := []any{}
	for _, it := range items {
		var c, v named
		_ = json.Unmarshal(it.Component, &c)
		_ = json.Unmarshal(it.Variable, &v)
		res := map[string]any{"component": rawValue(it.Component), "variable": rawValue(it.Variable)}
		if it.AttributeType != nil {
			res["attributeType"] = *it.AttributeType
		}
		var match *fixtureVariable
		componentKnown := false
		for i := range f.Variables {
			fv := &f.Variables[i]
			if fv.Component.key() == c.key() {
				componentKnown = true
				if fv.Variable.key() == v.key() {
					match = fv
				}
			}
		}
		switch {
		case !componentKnown:
			res["attributeStatus"] = "UnknownComponent"
		case match == nil:
			res["attributeStatus"] = "UnknownVariable"
		case it.AttributeType != nil && *it.AttributeType != "Actual":
			res["attributeStatus"] = "NotSupportedAttributeType"
		case action == "GetVariables" && match.Mutability == "WriteOnly",
			action == "SetVariables" && match.Mutability == "ReadOnly":
			res["attributeStatus"] = "Rejected"
		default:
			res["attributeStatus"] = "Accepted"
			value := match.Value
			if values != nil {
				value = values[match.key()]
			}
			if action == "GetVariables" {
				res["attributeValue"] = value
			} else if values != nil {
				values[match.key()] = it.AttributeValue
			}
		}
		results = append(results, res)
	}
	out[resultKey] = results
}

func rawValue(b json.RawMessage) any {
	var v any
	_ = json.Unmarshal(b, &v)
	return v
}
