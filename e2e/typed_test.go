// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

type centralSystem16 struct {
	ocpp16.UnimplementedCentralSystemHandler
}

func (centralSystem16) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.BootNotificationRequest) (*ocpp16.BootNotificationResponse, error) {
	// The CSMS asks for the configuration while it handles the boot.
	conf, err := ocpp16.GetConfiguration(ctx, p, &ocpp16.GetConfigurationRequest{Key: []string{"HeartbeatInterval"}})
	if err != nil {
		return nil, err
	}
	interval := 0
	if len(conf.ConfigurationKey) == 1 && conf.ConfigurationKey[0].Value != nil && *conf.ConfigurationKey[0].Value == "300" {
		interval = 300
	}
	return &ocpp16.BootNotificationResponse{Status: ocpp16.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: interval}, nil
}

type chargePoint16 struct {
	ocpp16.UnimplementedChargePointHandler
}

func (chargePoint16) GetConfiguration(ctx context.Context, p ocpp.Peer, req *ocpp16.GetConfigurationRequest) (*ocpp16.GetConfigurationResponse, error) {
	return &ocpp16.GetConfigurationResponse{ConfigurationKey: []ocpp16.KeyValue{{Key: "HeartbeatInterval", Value: ocpp.Ptr("300")}}}, nil
}

func (chargePoint16) Reset(ctx context.Context, p ocpp.Peer, req *ocpp16.ResetRequest) (*ocpp16.ResetResponse, error) {
	return &ocpp16.ResetResponse{Status: ocpp16.ResetStatusAccepted}, nil
}

// OCPP 1.6J with typed messages, both directions.
func TestTyped16(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp16.CentralSystem(centralSystem16{})), server.WithoutAuthentication())
	c := station(t, base+"T16", client.WithInsecureTransport(), client.WithHandler(ocpp.V16, ocpp16.ChargePoint(chargePoint16{})))
	connect(t, c)
	ctx := context.Background()

	boot, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{ChargePointVendor: "OTFabric", ChargePointModel: "TestStation"})
	if err != nil {
		t.Fatal(err)
	}
	if boot.Status != ocpp16.RegistrationStatusAccepted || boot.Interval != 300 {
		t.Fatalf("boot %+v", boot)
	}
	reset, err := ocpp16.Reset(ctx, waitStation(t, srv, "T16"), &ocpp16.ResetRequest{Type: ocpp16.ResetSoft})
	if err != nil || reset.Status != ocpp16.ResetStatusAccepted {
		t.Fatalf("reset %+v %v", reset, err)
	}
	// Not handled by the CSMS: NotSupported.
	_, err = ocpp16.Heartbeat(ctx, c, &ocpp16.HeartbeatRequest{})
	var ce *ocpp.CallError
	if !errors.As(err, &ce) || ce.Code != ocpp.NotSupported {
		t.Fatalf("heartbeat: %v", err)
	}
	// A station cannot send Reset, a request must be valid, and a 1.6
	// connection carries no 2.0.1 messages.
	if _, err := ocpp16.Reset(ctx, c, &ocpp16.ResetRequest{Type: ocpp16.ResetSoft}); !errors.Is(err, ocpp.ErrWrongDirection) {
		t.Fatalf("Reset from the station: %v", err)
	}
	if _, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{ChargePointVendor: "a vendor name of more than twenty characters", ChargePointModel: "M"}); !errors.Is(err, ocpp.ErrInvalidMessage) {
		t.Fatalf("invalid request: %v", err)
	}
	if _, err := ocpp201.Heartbeat(ctx, c, &ocpp201.HeartbeatRequest{}); !errors.Is(err, ocpp.ErrUnsupportedVersion) {
		t.Fatalf("2.0.1 on 1.6: %v", err)
	}
}

type csms201 struct {
	ocpp201.UnimplementedCSMSHandler
	started chan string
}

func (h csms201) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 60}, nil
}

func (h csms201) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp201.TransactionEventRequest) (*ocpp201.TransactionEventResponse, error) {
	h.started <- req.TransactionInfo.TransactionID
	return &ocpp201.TransactionEventResponse{}, nil
}

type station201 struct {
	ocpp201.UnimplementedChargingStationHandler
}

func (station201) Reset(ctx context.Context, p ocpp.Peer, req *ocpp201.ResetRequest) (*ocpp201.ResetResponse, error) {
	return &ocpp201.ResetResponse{Status: ocpp201.ResetStatusScheduled}, nil
}

// OCPP 2.0.1 with typed messages, both directions, over a compressed
// connection.
func TestTyped201(t *testing.T) {
	noLeak(t)
	started := make(chan string, 1)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, ocpp201.CSMS(csms201{started: started})), server.WithoutAuthentication())
	c := station(t, base+"T201", client.WithInsecureTransport(), client.WithCompression(),
		client.WithHandler(ocpp.V201, ocpp201.ChargingStation(station201{})))
	connect(t, c)
	ctx := context.Background()

	boot, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
		Reason:          ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
	})
	if err != nil || boot.Status != ocpp201.RegistrationStatusAccepted {
		t.Fatalf("boot %+v %v", boot, err)
	}
	if _, err := ocpp201.TransactionEvent(ctx, c, &ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventStarted, Timestamp: time.Now().UTC(), TriggerReason: ocpp201.TriggerReasonAuthorized,
		SeqNo: 0, TransactionInfo: ocpp201.TransactionType{TransactionID: "TX-1"},
	}); err != nil {
		t.Fatal(err)
	}
	if id := <-started; id != "TX-1" {
		t.Fatalf("transaction %q", id)
	}
	reset, err := ocpp201.Reset(ctx, waitStation(t, srv, "T201"), &ocpp201.ResetRequest{Type: ocpp201.ResetOnIdle})
	if err != nil || reset.Status != ocpp201.ResetStatusScheduled {
		t.Fatalf("reset %+v %v", reset, err)
	}
}

type csms21 struct {
	ocpp21.UnimplementedCSMSHandler
	stream chan *ocpp21.NotifyPeriodicEventStreamRequest
	sent   chan struct{} // closed when a SEND frame has been received
}

// BootNotification answers only once a SEND frame has been received: the
// station sends it while its BootNotification CALL is outstanding, which a
// SEND may do (Part 4 §4.2.4). Its handler runs after this one: a session
// handles incoming messages one at a time.
func (h csms21) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.BootNotificationRequest) (*ocpp21.BootNotificationResponse, error) {
	select {
	case <-h.sent:
	case <-time.After(5 * time.Second):
		return nil, errors.New("no SEND while the boot was outstanding")
	}
	return &ocpp21.BootNotificationResponse{Status: ocpp21.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 60}, nil
}

func (h csms21) NotifyPeriodicEventStream(ctx context.Context, p ocpp.Peer, req *ocpp21.NotifyPeriodicEventStreamRequest) error {
	h.stream <- req
	return nil
}

type station21 struct {
	ocpp21.UnimplementedChargingStationHandler
}

func (station21) Reset(ctx context.Context, p ocpp.Peer, req *ocpp21.ResetRequest) (*ocpp21.ResetResponse, error) {
	return &ocpp21.ResetResponse{Status: ocpp21.ResetStatusAccepted}, nil
}

// OCPP 2.1 with typed messages, both directions, and a SEND sent while a
// CALL is outstanding.
func TestTyped21(t *testing.T) {
	noLeak(t)
	stream := make(chan *ocpp21.NotifyPeriodicEventStreamRequest, 4)
	sent := make(chan struct{})
	var once sync.Once
	observe := func(info ocpp.ConnInfo, dir ocpp.Direction, frame []byte) {
		if dir == ocpp.Incoming && strings.HasPrefix(string(frame), "[6,") {
			once.Do(func() { close(sent) })
		}
	}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V21, ocpp21.CSMS(csms21{stream: stream, sent: sent})),
		server.WithoutAuthentication(), server.WithFrameObserver(observe))
	c := station(t, base+"T21", client.WithInsecureTransport(), client.WithCompression(),
		client.WithHandler(ocpp.V21, ocpp21.ChargingStation(station21{})))
	connect(t, c)
	ctx := context.Background()
	if c.Version() != ocpp.V21 {
		t.Fatalf("negotiated %s", c.Version())
	}

	event := &ocpp21.NotifyPeriodicEventStreamRequest{ID: 7, Pending: 0, Basetime: time.Now().UTC().Truncate(time.Second),
		Data: []ocpp21.StreamDataElementType{{T: 0, V: "230.1"}, {T: 0.5, V: "230.4"}}}
	type result struct {
		boot *ocpp21.BootNotificationResponse
		err  error
	}
	booted := make(chan result, 1)
	go func() {
		boot, err := ocpp21.BootNotification(ctx, c, &ocpp21.BootNotificationRequest{
			Reason:          ocpp21.BootReasonPowerUp,
			ChargingStation: ocpp21.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
		})
		booted <- result{boot, err}
	}()
	waitStation(t, srv, "T21")
	if err := ocpp21.NotifyPeriodicEventStream(ctx, c, event); err != nil {
		t.Fatal(err)
	}
	r := <-booted
	if r.err != nil || r.boot.Status != ocpp21.RegistrationStatusAccepted {
		t.Fatalf("boot %+v %v", r.boot, r.err)
	}
	got := <-stream
	if got.ID != 7 || len(got.Data) != 2 || got.Data[1].V != "230.4" || !got.Basetime.Equal(event.Basetime) {
		t.Fatalf("stream %+v", got)
	}

	reset, err := ocpp21.Reset(ctx, waitStation(t, srv, "T21"), &ocpp21.ResetRequest{Type: ocpp21.ResetOnIdle})
	if err != nil || reset.Status != ocpp21.ResetStatusAccepted {
		t.Fatalf("reset %+v %v", reset, err)
	}
	// The CSMS does not send NotifyPeriodicEventStream; an invalid SEND is
	// not sent; a 2.0.1 action is not sent on 2.1.
	if err := ocpp21.NotifyPeriodicEventStream(ctx, waitStation(t, srv, "T21"), event); !errors.Is(err, ocpp.ErrWrongDirection) {
		t.Fatalf("SEND from the CSMS: %v", err)
	}
	if err := ocpp21.NotifyPeriodicEventStream(ctx, c, &ocpp21.NotifyPeriodicEventStreamRequest{}); !errors.Is(err, ocpp.ErrInvalidMessage) {
		t.Fatalf("invalid SEND: %v", err)
	}
	if _, err := ocpp201.Heartbeat(ctx, c, &ocpp201.HeartbeatRequest{}); !errors.Is(err, ocpp.ErrUnsupportedVersion) {
		t.Fatalf("2.0.1 on 2.1: %v", err)
	}
}

// A response that fails validation is answered with a CALLRESULTERROR on
// OCPP 2.1 (Part 4 §4.2.3), and the call fails.
func TestCallResultError21(t *testing.T) {
	noLeak(t)
	var mu sync.Mutex
	var frames []string
	observe := func(info ocpp.ConnInfo, dir ocpp.Direction, frame []byte) {
		mu.Lock()
		frames = append(frames, dir.String()+" "+string(frame))
		mu.Unlock()
	}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V21, ocpp21.CSMS(ocpp21.UnimplementedCSMSHandler{})),
		server.WithoutAuthentication(), server.WithFrameObserver(observe))
	// A station whose Reset answer is not a ResetResponse.
	bad := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"Sometime"}`), nil
	})
	c := station(t, base+"T21E", client.WithInsecureTransport(), client.WithHandler(ocpp.V21, bad))
	connect(t, c)
	_, err := ocpp21.Reset(context.Background(), waitStation(t, srv, "T21E"), &ocpp21.ResetRequest{Type: ocpp21.ResetImmediate})
	if !errors.Is(err, ocpp.ErrInvalidMessage) {
		t.Fatalf("invalid response: %v", err)
	}
	eventually(t, "a CALLRESULTERROR", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, f := range frames {
			if strings.HasPrefix(f, ocpp.Outgoing.String()+" [5,") {
				return true
			}
		}
		return false
	})
}
