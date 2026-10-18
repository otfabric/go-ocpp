// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
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
