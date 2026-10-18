// SPDX-License-Identifier: MIT

package ocpptest_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpptest"
	"github.com/otfabric/go-ocpp/server"
)

// myCSMS is the application under test: it accepts every boot.
type myCSMS struct {
	ocpp201.UnimplementedCSMSHandler
}

func (myCSMS) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

// Testing a CSMS handler with a real station, and the station side
// recorded.
func TestCSMSHandler(t *testing.T) {
	stationSide := ocpptest.NewRecorder().Answer("Reset", `{"status":"Accepted"}`)
	srv, url := ocpptest.CSMS(t, server.WithHandler(ocpp.V201, ocpp201.CSMS(myCSMS{})))
	c := ocpptest.Station(t, url+"CS1", client.WithHandler(ocpp.V201, stationSide))

	boot, err := ocpp201.BootNotification(context.Background(), c, &ocpp201.BootNotificationRequest{
		Reason: ocpp201.BootReasonPowerUp, ChargingStation: ocpp201.ChargingStationType{VendorName: "V", Model: "M"}})
	if err != nil || boot.Status != ocpp201.RegistrationStatusAccepted {
		t.Fatalf("%+v %v", boot, err)
	}

	sess := ocpptest.Session(t, srv, "CS1")
	reset, err := ocpp201.Reset(context.Background(), sess, &ocpp201.ResetRequest{Type: ocpp201.ResetImmediate})
	if err != nil || reset.Status != ocpp201.ResetStatusAccepted {
		t.Fatalf("%+v %v", reset, err)
	}
	var got ocpp201.ResetRequest
	if err := json.Unmarshal(stationSide.Wait(t, "Reset"), &got); err != nil || got.Type != ocpp201.ResetImmediate {
		t.Fatalf("%+v %v", got, err)
	}

	// No answer set: NotImplemented. A CALLERROR answer.
	var ce *ocpp.CallError
	if _, err := ocpp201.UnlockConnector(context.Background(), sess, &ocpp201.UnlockConnectorRequest{EVSEID: 1, ConnectorID: 1}); !errors.As(err, &ce) || ce.Code != ocpp.NotImplemented {
		t.Fatalf("unanswered: %v", err)
	}
	stationSide.AnswerError("UnlockConnector", ocpp.NewCallError(ocpp.NotSupported, "no lock"))
	if _, err := ocpp201.UnlockConnector(context.Background(), sess, &ocpp201.UnlockConnectorRequest{EVSEID: 1, ConnectorID: 1}); !errors.As(err, &ce) || ce.Code != ocpp.NotSupported {
		t.Fatalf("CALLERROR answer: %v", err)
	}
	if n := len(stationSide.Received("UnlockConnector")); n != 2 {
		t.Fatalf("%d UnlockConnector received", n)
	}
}

// Testing a station's SENDs (OCPP 2.1) with a recording CSMS.
func TestRecorderSend(t *testing.T) {
	csmsSide := ocpptest.NewRecorder()
	_, url := ocpptest.CSMS(t, server.WithHandler(ocpp.V21, csmsSide))
	c := ocpptest.Station(t, url+"CS21", client.WithVersions(ocpp.V21))
	if err := c.Send(context.Background(), "NotifyPeriodicEventStream", json.RawMessage(`{"id":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := csmsSide.Wait(t, "NotifyPeriodicEventStream"); string(got) != `{"id":1}` {
		t.Fatalf("%s", got)
	}
}

// fatalTB records a Fatalf and stops its goroutine, as testing.T does.
type fatalTB struct {
	testing.TB
	msg chan string
}

func (f *fatalTB) Helper() {}
func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg <- fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func fatalOf(t *testing.T, run func(tb testing.TB)) string {
	t.Helper()
	tb := &fatalTB{TB: t, msg: make(chan string, 1)}
	done := make(chan struct{})
	go func() { defer close(done); run(tb) }()
	<-done
	select {
	case m := <-tb.msg:
		return m
	default:
		return ""
	}
}

// The kit fails the test, with a message, when it cannot do its job.
func TestFailures(t *testing.T) {
	if m := fatalOf(t, func(tb testing.TB) { ocpptest.CSMS(tb) }); !strings.Contains(m, "server.New") {
		t.Errorf("CSMS without a handler: %q", m)
	}
	if m := fatalOf(t, func(tb testing.TB) { ocpptest.Station(tb, "http://not-a-websocket/CS1") }); !strings.Contains(m, "client.New") {
		t.Errorf("Station with a bad URL: %q", m)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens there now
	if m := fatalOf(t, func(tb testing.TB) {
		ocpptest.Station(tb, "ws://"+addr+"/ocpp/CS1", client.WithVersions(ocpp.V201))
	}); !strings.Contains(m, "connect") {
		t.Errorf("Station with nothing to connect to: %q", m)
	}
}
