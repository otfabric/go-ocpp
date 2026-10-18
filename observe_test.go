// SPDX-License-Identifier: MIT

package ocpp

import (
	"errors"
	"testing"
)

func TestCallError(t *testing.T) {
	if got := NewCallError(NotSupported, "no displays").Error(); got != "ocpp: CALLERROR NotSupported: no displays" {
		t.Errorf("Error() = %q", got)
	}
	if got := NewCallError(NotImplemented, "").Error(); got != "ocpp: CALLERROR NotImplemented" {
		t.Errorf("Error() = %q", got)
	}
	var ce *CallError
	if !errors.As(error(NewCallError(GenericError, "x")), &ce) || ce.Code != GenericError {
		t.Error("errors.As")
	}
}

func TestNames(t *testing.T) {
	for got, want := range map[string]string{
		ChargingStation.String(): "ChargingStation", CSMS.String(): "CSMS", Role(9).String(): "Role(9)",
		Disconnected.String(): "Disconnected", Connecting.String(): "Connecting", Connected.String(): "Connected",
		Closed.String(): "Closed", State(9).String(): "State(9)",
		Outgoing.String(): "outgoing", Incoming.String(): "incoming", Direction(9).String(): "Direction(9)",
	} {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestNopMetrics(t *testing.T) {
	var m Metrics = NopMetrics{}
	m.OnConnect(ConnInfo{})
	m.OnConnectFailed(ConnInfo{}, nil)
	m.OnDisconnect(ConnInfo{}, nil)
	m.OnReconnectAttempt(ConnInfo{}, 1)
	m.OnCallDone(ConnInfo{}, "", 0, nil)
	m.OnCallHandled(ConnInfo{}, "", 0, nil)
	m.OnProtocolError(ConnInfo{}, nil)
}
