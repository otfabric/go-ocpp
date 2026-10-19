// SPDX-License-Identifier: MIT

// Command ocpp21 runs an OCPP 2.1 CSMS and Charging Station in one process.
// The station offers OCPP 2.1 and 2.0.1 and the CSMS picks 2.1. The station
// opens a periodic event stream and sends its values as SEND messages,
// which are never answered.
//
// For brevity it uses plain WebSocket on the loopback interface without
// authentication; see examples/ocpp201 for TLS and Basic authentication.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

const stationID = "OT-FABRIC-TEST-021"

// csms answers the Charging Station's CALLs and receives its SENDs.
type csms struct {
	ocpp21.UnimplementedCSMSHandler
	received chan int // values received per stream message
}

func (csms) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.BootNotificationRequest) (*ocpp21.BootNotificationResponse, error) {
	fmt.Printf("CSMS: %s booted on OCPP %s (%s)\n", p.ID(), p.Version(), req.Reason)
	return &ocpp21.BootNotificationResponse{Status: ocpp21.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms) OpenPeriodicEventStream(ctx context.Context, p ocpp.Peer, req *ocpp21.OpenPeriodicEventStreamRequest) (*ocpp21.OpenPeriodicEventStreamResponse, error) {
	fmt.Printf("CSMS: stream %d opened for monitor %d\n", req.ConstantStreamData.ID, req.ConstantStreamData.VariableMonitoringID)
	return &ocpp21.OpenPeriodicEventStreamResponse{Status: ocpp21.GenericStatusAccepted}, nil
}

// NotifyPeriodicEventStream is a SEND: its error, if any, is only logged.
func (h csms) NotifyPeriodicEventStream(ctx context.Context, p ocpp.Peer, req *ocpp21.NotifyPeriodicEventStreamRequest) error {
	for _, d := range req.Data {
		at := req.Basetime.Add(time.Duration(d.T * float64(time.Second)))
		fmt.Printf("CSMS: stream %d at %s: %s\n", req.ID, at.Format("15:04:05.0"), d.V)
	}
	h.received <- len(req.Data)
	return nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h := csms{received: make(chan int, 4)}
	srv, err := server.New(
		server.WithHandler(ocpp.V21, ocpp21.CSMS(h)),
		server.WithHandler(ocpp.V201, ocpp201.CSMS(ocpp201.UnimplementedCSMSHandler{})),
		server.WithoutAuthentication(),
	)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(ctx) }()

	c, err := client.New("ws://"+ln.Addr().String()+"/ocpp/"+stationID,
		client.WithVersions(ocpp.V21, ocpp.V201),
		client.WithHandler(ocpp.V21, ocpp21.ChargingStation(ocpp21.UnimplementedChargingStationHandler{})),
		client.WithHandler(ocpp.V201, ocpp201.ChargingStation(ocpp201.UnimplementedChargingStationHandler{})),
		client.WithInsecureTransport(),
	)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if c.Version() != ocpp.V21 {
		return fmt.Errorf("negotiated OCPP %s", c.Version())
	}

	boot, err := ocpp21.BootNotification(ctx, c, &ocpp21.BootNotificationRequest{
		Reason:          ocpp21.BootReasonPowerUp,
		ChargingStation: ocpp21.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
	})
	if err != nil {
		return err
	}
	fmt.Printf("station: boot %s\n", boot.Status)

	open, err := ocpp21.OpenPeriodicEventStream(ctx, c, &ocpp21.OpenPeriodicEventStreamRequest{
		ConstantStreamData: ocpp21.ConstantStreamDataType{ID: 1, VariableMonitoringID: 12,
			Params: ocpp21.PeriodicEventStreamParamsType{Interval: ocpp.Ptr(1), Values: ocpp.Ptr(2)}},
	})
	if err != nil {
		return err
	}
	if open.Status != ocpp21.GenericStatusAccepted {
		return fmt.Errorf("stream %s", open.Status)
	}

	// Two SENDs of two values each. Send returns once the message is
	// queued: nothing comes back.
	base := time.Now().UTC().Truncate(time.Second)
	for i, values := range [][]string{{"230.1", "230.4"}, {"229.8", "230.0"}} {
		data := make([]ocpp21.StreamDataElementType, len(values))
		for j, v := range values {
			data[j] = ocpp21.StreamDataElementType{T: float64(2*i+j) * 0.5, V: v}
		}
		if err := ocpp21.NotifyPeriodicEventStream(ctx, c, &ocpp21.NotifyPeriodicEventStreamRequest{
			ID: 1, Pending: 2 * (1 - i), Basetime: base, Data: data,
		}); err != nil {
			return err
		}
	}
	for n := 0; n < 4; {
		select {
		case k := <-h.received:
			n += k
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fmt.Println("station: stream sent")
	return nil
}
