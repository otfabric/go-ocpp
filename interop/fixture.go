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
)

// fixture is the part of an ocpp-interop fixture the tests use. It is read
// from the image (print-fixture), so the expectations cannot drift from
// what the image serves.
type fixture struct {
	Stations []struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	} `json:"stations"`
	IDTokens []struct {
		IDToken string `json:"idToken"`
		Status  string `json:"status"`
	} `json:"idTokens"`
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

var fixtures sync.Map // image -> *fixture

func (a adapter) fixture(t testing.TB) *fixture {
	t.Helper()
	if f, ok := fixtures.Load(a.image); ok {
		return f.(*fixture)
	}
	out, stderr, err := docker("run", "--rm", a.image, "print-fixture", "--version", "1.6")
	if err != nil {
		t.Fatalf("print-fixture of %s: %v\n%s", a.image, err, stderr)
	}
	f := &fixture{}
	if err := json.Unmarshal([]byte(out), f); err != nil {
		t.Fatalf("fixture of %s: %v", a.image, err)
	}
	fixtures.Store(a.image, f)
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
func (f *fixture) answer(table map[string]answer, action string, req json.RawMessage) (map[string]any, *ocpp.CallError) {
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
			IDTag string `json:"idTag"`
		}
		_ = json.Unmarshal(req, &r)
		status := "Invalid"
		for _, tok := range f.IDTokens {
			if tok.IDToken == r.IDTag {
				status = tok.Status
			}
		}
		out["idTagInfo"] = map[string]any{"status": status}
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
				if c.Key == k {
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
// it receives.
type dispatcher struct {
	f     *fixture
	table map[string]answer
	mu    sync.Mutex
	got   map[string][]json.RawMessage // action -> payloads received
}

func newDispatcher(f *fixture, table map[string]answer) *dispatcher {
	return &dispatcher{f: f, table: table, got: map[string][]json.RawMessage{}}
}

func (d *dispatcher) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	d.mu.Lock()
	d.got[action] = append(d.got[action], append(json.RawMessage(nil), payload...))
	d.mu.Unlock()
	out, cerr := d.f.answer(d.table, action, payload)
	if cerr != nil {
		return nil, cerr
	}
	if out == nil {
		return nil, ocpp.NewCallError(ocpp.NotImplemented, "")
	}
	b, err := json.Marshal(replaceNow(out))
	return b, err
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
	out, cerr := f.answer(f.answerTable(s.From), s.Action, s.Payload)
	if cerr != nil {
		return nil, cerr, true
	}
	if out == nil {
		return nil, nil, false
	}
	b, _ := json.Marshal(out)
	return decode(t, b), nil, true
}
