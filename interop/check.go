//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// noAnswerCodes are the CALLERRORs an implementation may answer a CALL with
// when it has no answer for the action: NotImplemented for an action of the
// version, NotSupported for one it recognises but does not support.
var noAnswerCodes = map[ocpp.ErrorCode]bool{ocpp.NotImplemented: true, ocpp.NotSupported: true}

// checkGoCall checks the outcome of a CALL go-ocpp sent (resp, err) against
// the answer the fixture defines for the sample.
func checkGoCall(t *testing.T, f *fixture, s sample, resp json.RawMessage, err error) {
	t.Helper()
	if problem := goCallProblem(t, f, s, resp, err); problem != nil {
		t.Fatal(problem)
	}
}

// checkUndeclaredGoCall checks a CALL go-ocpp sent with an action the
// reference does not declare handling. An exchange that works as the
// fixture says is a pass: it did work. One that does not is skipped as
// unsupported, never failed and never passed.
func checkUndeclaredGoCall(t *testing.T, a adapter, f *fixture, s sample, resp json.RawMessage, err error) {
	t.Helper()
	if problem := goCallProblem(t, f, s, resp, err); problem != nil {
		t.Skipf("%s does not declare handling %s, and it does not answer it as the fixture says: %v", a.name, s.Action, problem)
	}
}

// goCallProblem compares the outcome of a CALL go-ocpp sent (resp, err)
// with the answer the fixture defines for the sample, and returns what
// differs, or nil.
func goCallProblem(t *testing.T, f *fixture, s sample, resp json.RawMessage, err error) error {
	t.Helper()
	want, wantErr, answered := f.expected(t, s)
	var ce *ocpp.CallError
	switch {
	case !answered:
		if !errors.As(err, &ce) || !noAnswerCodes[ce.Code] {
			return fmt.Errorf("%s has no answer in the fixture: want CALLERROR NotImplemented or NotSupported, got %v (%s)", s.Action, err, resp)
		}
	case wantErr != nil:
		if !errors.As(err, &ce) || ce.Code != wantErr.Code {
			return fmt.Errorf("want CALLERROR %s, got %v", wantErr.Code, err)
		}
	default:
		if err != nil {
			return fmt.Errorf("%s: %w", s.Action, err)
		}
		if err := contains(want, decode(t, resp)); err != nil {
			return fmt.Errorf("%s answered %s: %w", s.Action, resp, err)
		}
		// The reference's answer conforms to the schema, by go-ocpp's typed
		// validation.
		if err := validateResponse(f.version, s.Action, resp); err != nil {
			return fmt.Errorf("%s answer %s fails go-ocpp's validation: %w", s.Action, resp, err)
		}
	}
	return nil
}

// checkRefCall checks the result document of a CALL the reference sent
// against the answer the fixture defines for the sample.
func checkRefCall(t *testing.T, f *fixture, s sample, r *result) {
	t.Helper()
	want, wantErr, answered := f.expected(t, s)
	switch {
	case !answered:
		if r.CallError == nil || !noAnswerCodes[ocpp.ErrorCode(r.CallError.Code)] {
			t.Fatalf("%s has no answer in the fixture: want CALLERROR NotImplemented or NotSupported, got %+v (exit %d)", s.Action, r, r.exit)
		}
	case wantErr != nil:
		if r.CallError == nil || r.CallError.Code != string(wantErr.Code) {
			t.Fatalf("want CALLERROR %s, got %+v", wantErr.Code, r)
		}
	default:
		if !r.OK || r.exit != 0 {
			t.Fatalf("%s failed: exit %d, error %+v, callError %+v", s.Action, r.exit, r.Error, r.CallError)
		}
		if err := contains(want, decode(t, r.Response)); err != nil {
			t.Fatalf("%s answered %s: %v", s.Action, r.Response, err)
		}
	}
}

// checkReceived checks that the request go-ocpp's handler received has
// every field of the sample: nothing was lost on the way.
func checkReceived(t *testing.T, d *dispatcher, s sample) {
	t.Helper()
	got := d.received(s.Action)
	if len(got) == 0 {
		t.Fatalf("go-ocpp received no %s", s.Action)
	}
	if err := contains(decode(t, s.Payload), decode(t, got[len(got)-1])); err != nil {
		t.Fatalf("%s arrived as %s: %v", s.Action, got[len(got)-1], err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if errs := d.invalid[s.Action]; len(errs) > 0 {
		t.Fatalf("%s as the reference sent it fails go-ocpp's validation: %v", s.Action, errs[len(errs)-1])
	}
}
