//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

// The fixture of the references whose answers break their response schema on
// purpose (contract 0.6, invalidAnswers): the CSMS answers Heartbeat with a
// number for currentTime, the station GetTransactionStatus with a string for
// messagesInQueue.
const callResultErrorFixture = "/fixtures/v21/callresulterror/fixture.json"

// invalidAnswers answers one action with a payload that breaks its schema,
// as it is, and everything else as the fixture does: go-ocpp writes a raw
// result unchanged.
type invalidAnswers struct {
	*dispatcher
	action  string
	payload json.RawMessage
}

func (d invalidAnswers) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	if action == d.action {
		return d.payload, nil
	}
	return d.dispatcher.Dispatch(ctx, p, action, payload)
}

// resultErrors records the CALLRESULTERRORs go-ocpp receives: it reports each
// through Metrics.OnProtocolError with the peer's error as an *ocpp.CallError.
type resultErrors struct {
	ocpp.NopMetrics
	mu  sync.Mutex
	got []*ocpp.CallError
}

func (m *resultErrors) OnProtocolError(_ ocpp.ConnInfo, err error) {
	var ce *ocpp.CallError
	if errors.As(err, &ce) {
		m.mu.Lock()
		m.got = append(m.got, ce)
		m.mu.Unlock()
	}
}

func (m *resultErrors) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.got)
}

// callResultErrorCaps skips a reference that does not declare
// callResultError in OCPP 2.1.
func callResultErrorCaps(t *testing.T, a adapter, role string) {
	t.Helper()
	a.role(t, ocpp.V21, role)
	if !a.caps(t).Versions[ocpp.V21.String()].Features.CallResultError {
		t.Skipf("%s does not declare callResultError in OCPP 2.1", a.name)
	}
}

// checkRefCallResultError: the reference answered go-ocpp's invalid
// CALLRESULT with a CALLRESULTERROR, and go-ocpp received it.
func checkRefCallResultError(t *testing.T, r *result, m *resultErrors) {
	t.Helper()
	if r.exit != 1 || r.Error == nil || r.Error.Code != "invalid-response" || r.CallResultError == nil || r.CallResultError.Code == "" {
		t.Fatalf("want invalid-response with a CALLRESULTERROR, got exit %d, error %v, callResultError %v", r.exit, r.Error, r.CallResultError)
	}
	eventually(t, 10*time.Second, "go-ocpp receiving the CALLRESULTERROR", func() bool { return m.count() > 0 })
	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.got[0]; string(got.Code) != r.CallResultError.Code {
		t.Fatalf("go-ocpp received %s, the reference sent %s", got.Code, r.CallResultError.Code)
	}
}

// TestStationCallResultError: OCPP 2.1 CALLRESULTERROR between go-ocpp as the
// station and each reference CSMS that declares callResultError, both ways.
func TestStationCallResultError(t *testing.T) {
	for _, a := range adapters(t) {
		t.Run(a.name+"/go-ocpp-refuses", func(t *testing.T) {
			callResultErrorCaps(t, a, "server")
			f := a.fixture(t, ocpp.V21)
			id, _ := f.station()
			ref := startServer(t, a, ocpp.V21, "--fixture", callResultErrorFixture)
			c := newStation(t, ocpp.V21, fmt.Sprintf("ws://127.0.0.1:%d/ocpp/%s", ref.port, id),
				client.WithHandler(ocpp.V21, newDispatcher(f, f.Answers.Station, ocpp.V21)))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.Connect(ctx); err != nil {
				t.Fatalf("connect: %v", err)
			}
			var resp ocpp21.HeartbeatResponse
			if err := c.Call(ctx, "Heartbeat", json.RawMessage(`{}`), &resp); err == nil {
				t.Fatalf("an invalid Heartbeat response was accepted: %+v", resp)
			}
			if !ref.waitEvent("call-result-error", 1, 10*time.Second) {
				t.Fatalf("%s reported no call-result-error: go-ocpp sent no CALLRESULTERROR it could read", a.name)
			}
		})
		t.Run(a.name+"/reference-refuses", func(t *testing.T) {
			callResultErrorCaps(t, a, "server")
			f := a.fixture(t, ocpp.V21)
			id, _ := f.station()
			ref := startServer(t, a, ocpp.V21)
			m := &resultErrors{}
			d := invalidAnswers{newDispatcher(f, f.Answers.Station, ocpp.V21), "GetTransactionStatus",
				json.RawMessage(`{"messagesInQueue":"yes"}`)}
			c := newStation(t, ocpp.V21, fmt.Sprintf("ws://127.0.0.1:%d/ocpp/%s", ref.port, id),
				client.WithHandler(ocpp.V21, d), client.WithMetrics(m))
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := c.Connect(ctx); err != nil {
				t.Fatalf("connect: %v", err)
			}
			checkRefCallResultError(t, ref.call(t, id, "GetTransactionStatus", json.RawMessage(`{}`)), m)
		})
	}
}

// TestReferenceStationCallResultError: OCPP 2.1 CALLRESULTERROR between
// go-ocpp as the CSMS and each reference station that declares
// callResultError, both ways.
func TestReferenceStationCallResultError(t *testing.T) {
	for _, a := range adapters(t) {
		t.Run(a.name+"/go-ocpp-refuses", func(t *testing.T) {
			callResultErrorCaps(t, a, "client")
			f := a.fixture(t, ocpp.V21)
			id, _ := f.station()
			srv, port := newCSMS(t, server.WithHandler(ocpp.V21, newDispatcher(f, f.Answers.CSMS, ocpp.V21)),
				server.WithoutAuthentication())
			ref := startStation(t, a, ocpp.V21, port, "--fixture", callResultErrorFixture)
			sess := waitSession(t, srv, id)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			var resp ocpp21.GetTransactionStatusResponse
			if err := sess.Call(ctx, "GetTransactionStatus", json.RawMessage(`{}`), &resp); err == nil {
				t.Fatalf("an invalid GetTransactionStatus response was accepted: %+v", resp)
			}
			if !ref.waitEvent("call-result-error", 1, 10*time.Second) {
				t.Fatalf("%s reported no call-result-error: go-ocpp sent no CALLRESULTERROR it could read", a.name)
			}
		})
		t.Run(a.name+"/reference-refuses", func(t *testing.T) {
			callResultErrorCaps(t, a, "client")
			f := a.fixture(t, ocpp.V21)
			id, _ := f.station()
			m := &resultErrors{}
			d := invalidAnswers{newDispatcher(f, f.Answers.CSMS, ocpp.V21), "Heartbeat", json.RawMessage(`{"currentTime":1}`)}
			srv, port := newCSMS(t, server.WithHandler(ocpp.V21, d), server.WithoutAuthentication(), server.WithMetrics(m))
			ref := startStation(t, a, ocpp.V21, port)
			waitSession(t, srv, id)
			checkRefCallResultError(t, ref.call(t, "", "Heartbeat", json.RawMessage(`{}`)), m)
		})
	}
}
