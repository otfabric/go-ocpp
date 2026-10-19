// SPDX-License-Identifier: MIT

// Command vendor-extensions shows the ways OCPP leaves room for
// vendor-specific data, on OCPP 1.6, 2.0.1 and 2.1, with one CSMS and one
// station per version in one process:
//
//   - DataTransfer, in both directions, with a vendorId and a messageId
//     (all versions); in 1.6 its data is a string, in 2.x any JSON value;
//   - customData on a standard message (2.x): vendor properties next to
//     the standard ones, kept by go-ocpp as raw JSON;
//   - a raw call: an action that is not modelled, sent with
//     json.RawMessage through Call and answered by a dispatcher that
//     handles it before the generated one.
//
// Validation still applies to standard messages: go-ocpp refuses to send
// an invalid one, and the receiving side refuses one that was built by
// hand.
//
// For brevity it uses plain WebSocket on the loopback interface without
// authentication; see examples/security-profiles.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

// vendorID identifies the vendor, by convention a reversed DNS name.
const vendorID = "com.otfabric"

// csmsDataTransfer is the CSMS's vendor logic, shared by all versions: the
// station asks for its tariff.
func csmsDataTransfer(vendor, messageID string, data []byte) (status string, reply []byte) {
	switch {
	case vendor != vendorID:
		return "UnknownVendorId", nil
	case messageID != "GetTariff":
		return "UnknownMessageId", nil
	}
	var req struct {
		ConnectorID int `json:"connectorId"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return "Rejected", nil
	}
	return "Accepted", []byte(fmt.Sprintf(`{"connectorId":%d,"pricePerKWh":0.35,"currency":"EUR"}`, req.ConnectorID))
}

// stationDataTransfer is the station's vendor logic: the CSMS sets the
// colour of its LED ring.
func stationDataTransfer(vendor, messageID string, data []byte) string {
	var req struct {
		Color string `json:"color"`
	}
	switch {
	case vendor != vendorID:
		return "UnknownVendorId"
	case messageID != "SetLedColor":
		return "UnknownMessageId"
	case json.Unmarshal(data, &req) != nil || req.Color == "":
		return "Rejected"
	}
	fmt.Printf("station: LED ring is now %s\n", req.Color)
	return "Accepted"
}

// withVendorActions handles the vendor's own action VendorStatus and passes
// every other action to the generated dispatcher d. (Wrapping an ocpp21
// dispatcher this way would hide its DispatchSend, for SEND messages.)
func withVendorActions(d ocpp.Dispatcher) ocpp.Dispatcher {
	return ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		if action == "VendorStatus" {
			fmt.Printf("CSMS: %s sent VendorStatus %s\n", p.ID(), payload)
			return json.RawMessage(`{"ack":true}`), nil
		}
		return d.Dispatch(ctx, p, action, payload)
	})
}

// ---------------------------------------------------------------------------
// CSMS handlers.

type csms16 struct {
	ocpp16.UnimplementedCentralSystemHandler
}

func (csms16) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.BootNotificationRequest) (*ocpp16.BootNotificationResponse, error) {
	return &ocpp16.BootNotificationResponse{Status: ocpp16.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms16) DataTransfer(ctx context.Context, p ocpp.Peer, req *ocpp16.DataTransferRequest) (*ocpp16.DataTransferResponse, error) {
	var data []byte
	if req.Data != nil {
		data = []byte(*req.Data) // 1.6: a string, here holding JSON
	}
	status, reply := csmsDataTransfer(req.VendorID, deref(req.MessageID), data)
	resp := &ocpp16.DataTransferResponse{Status: ocpp16.DataTransferStatus(status)}
	if reply != nil {
		resp.Data = ocpp.Ptr(string(reply))
	}
	return resp, nil
}

type csms201 struct {
	ocpp201.UnimplementedCSMSHandler
}

func (csms201) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	if cd := req.CustomData; cd != nil {
		fmt.Printf("CSMS: BootNotification customData from %s: hardwareRevision=%s cabinetTemperature=%s\n",
			cd.VendorID, cd.Fields["hardwareRevision"], cd.Fields["cabinetTemperature"])
	}
	// customData in a response works the same way.
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300,
		CustomData: &ocpp201.CustomData{VendorID: vendorID, Fields: map[string]json.RawMessage{"siteId": json.RawMessage(`"AMS-01"`)}}}, nil
}

func (csms201) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp201.HeartbeatRequest) (*ocpp201.HeartbeatResponse, error) {
	return &ocpp201.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (csms201) DataTransfer(ctx context.Context, p ocpp.Peer, req *ocpp201.DataTransferRequest) (*ocpp201.DataTransferResponse, error) {
	status, reply := csmsDataTransfer(req.VendorID, deref(req.MessageID), req.Data)
	return &ocpp201.DataTransferResponse{Status: ocpp201.DataTransferStatusEnumType(status), Data: reply}, nil
}

type csms21 struct {
	ocpp21.UnimplementedCSMSHandler
}

func (csms21) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.BootNotificationRequest) (*ocpp21.BootNotificationResponse, error) {
	return &ocpp21.BootNotificationResponse{Status: ocpp21.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms21) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.StatusNotificationRequest) (*ocpp21.StatusNotificationResponse, error) {
	fmt.Printf("CSMS: connector %d %s", req.ConnectorID, req.ConnectorStatus)
	if cd := req.CustomData; cd != nil {
		fmt.Printf(", customData from %s: %d fields, lockState=%s", cd.VendorID, len(cd.Fields), cd.Fields["lockState"])
	}
	fmt.Println()
	return &ocpp21.StatusNotificationResponse{}, nil
}

func (csms21) DataTransfer(ctx context.Context, p ocpp.Peer, req *ocpp21.DataTransferRequest) (*ocpp21.DataTransferResponse, error) {
	status, reply := csmsDataTransfer(req.VendorID, deref(req.MessageID), req.Data)
	return &ocpp21.DataTransferResponse{Status: ocpp21.DataTransferStatusEnumType(status), Data: reply}, nil
}

// ---------------------------------------------------------------------------
// Station handlers: the CSMS's DataTransfer.

type station16 struct {
	ocpp16.UnimplementedChargePointHandler
}

func (station16) DataTransfer(ctx context.Context, p ocpp.Peer, req *ocpp16.DataTransferRequest) (*ocpp16.DataTransferResponse, error) {
	var data []byte
	if req.Data != nil {
		data = []byte(*req.Data)
	}
	return &ocpp16.DataTransferResponse{Status: ocpp16.DataTransferStatus(stationDataTransfer(req.VendorID, deref(req.MessageID), data))}, nil
}

type station201 struct {
	ocpp201.UnimplementedChargingStationHandler
}

func (station201) DataTransfer(ctx context.Context, p ocpp.Peer, req *ocpp201.DataTransferRequest) (*ocpp201.DataTransferResponse, error) {
	return &ocpp201.DataTransferResponse{Status: ocpp201.DataTransferStatusEnumType(stationDataTransfer(req.VendorID, deref(req.MessageID), req.Data))}, nil
}

type station21 struct {
	ocpp21.UnimplementedChargingStationHandler
}

func (station21) DataTransfer(ctx context.Context, p ocpp.Peer, req *ocpp21.DataTransferRequest) (*ocpp21.DataTransferResponse, error) {
	return &ocpp21.DataTransferResponse{Status: ocpp21.DataTransferStatusEnumType(stationDataTransfer(req.VendorID, deref(req.MessageID), req.Data))}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------------------

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The server registers a session just after the station's Connect returns:
	// the session handler hands it over once it exists.
	sessions := make(chan *server.Session, 4)
	srv, err := server.New(
		server.WithSessionHandler(func(s *server.Session) { sessions <- s }, nil),
		server.WithHandler(ocpp.V16, ocpp16.CentralSystem(csms16{})),
		server.WithHandler(ocpp.V201, withVendorActions(ocpp201.CSMS(csms201{}))),
		server.WithHandler(ocpp.V21, ocpp21.CSMS(csms21{})),
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
	base := "ws://" + ln.Addr().String() + "/ocpp/"

	connect := func(id string, v ocpp.Version, d ocpp.Dispatcher) (*client.Client, *server.Session, error) {
		c, err := client.New(base+id, client.WithVersions(v), client.WithHandler(v, d), client.WithInsecureTransport())
		if err != nil {
			return nil, nil, err
		}
		if err := c.Connect(ctx); err != nil {
			return nil, nil, err
		}
		for {
			select {
			case s := <-sessions:
				if s.ID() == id {
					return c, s, nil
				}
			case <-ctx.Done():
				_ = c.Close()
				return nil, nil, errors.New("no session")
			}
		}
	}

	fmt.Println("--- OCPP 1.6 ---")
	c16, s16, err := connect("VX16-0001", ocpp.V16, ocpp16.ChargePoint(station16{}))
	if err != nil {
		return err
	}
	defer func() { _ = c16.Close() }()
	if err := demo16(ctx, c16, s16); err != nil {
		return err
	}

	fmt.Println("\n--- OCPP 2.0.1 ---")
	c201, s201, err := connect("VX201-0001", ocpp.V201, ocpp201.ChargingStation(station201{}))
	if err != nil {
		return err
	}
	defer func() { _ = c201.Close() }()
	if err := demo201(ctx, c201, s201); err != nil {
		return err
	}

	fmt.Println("\n--- OCPP 2.1 ---")
	c21, s21, err := connect("VX21-0001", ocpp.V21, ocpp21.ChargingStation(station21{}))
	if err != nil {
		return err
	}
	defer func() { _ = c21.Close() }()
	return demo21(ctx, c21, s21)
}

func demo16(ctx context.Context, c *client.Client, s *server.Session) error {
	if _, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{ChargePointVendor: "OT Fabric", ChargePointModel: "VendorStation"}); err != nil {
		return err
	}
	// Station to CSMS: data is a string; JSON inside it is a convention.
	for _, msg := range []struct{ vendor, id string }{{vendorID, "GetTariff"}, {vendorID, "GetWeather"}, {"com.example", "GetTariff"}} {
		resp, err := ocpp16.DataTransfer(ctx, c, &ocpp16.DataTransferRequest{VendorID: msg.vendor, MessageID: ocpp.Ptr(msg.id), Data: ocpp.Ptr(`{"connectorId":1}`)})
		if err != nil {
			return err
		}
		fmt.Printf("station: DataTransfer %s/%s: %s %s\n", msg.vendor, msg.id, resp.Status, deref(resp.Data))
	}
	// CSMS to station.
	resp, err := ocpp16.DataTransfer(ctx, s, &ocpp16.DataTransferRequest{VendorID: vendorID, MessageID: ocpp.Ptr("SetLedColor"), Data: ocpp.Ptr(`{"color":"green"}`)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: DataTransfer %s/SetLedColor: %s\n", vendorID, resp.Status)

	// Validation: messageId is at most 50 characters in 1.6. The request
	// is refused before it is sent.
	_, err = ocpp16.DataTransfer(ctx, c, &ocpp16.DataTransferRequest{VendorID: vendorID, MessageID: ocpp.Ptr(strings.Repeat("x", 51))})
	var ve *ocpp.ValidationError
	if !errors.As(err, &ve) {
		return fmt.Errorf("expected a validation error, got %v", err)
	}
	fmt.Printf("station: not sent: %v\n", err)

	// A raw call of an action this CSMS does not handle (no vendor
	// dispatcher on 1.6 here): the CSMS answers NotImplemented.
	err = c.Call(ctx, "VendorStatus", json.RawMessage(`{"fan":"ok"}`), nil)
	var ce *ocpp.CallError
	if !errors.As(err, &ce) {
		return fmt.Errorf("expected a CALLERROR, got %v", err)
	}
	fmt.Printf("station: raw VendorStatus: CALLERROR %s\n", ce.Code)
	return nil
}

func demo201(ctx context.Context, c *client.Client, s *server.Session) error {
	// customData on a standard message: vendor properties under a vendorId.
	boot, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
		Reason:          ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "VendorStation"},
		CustomData: &ocpp201.CustomData{VendorID: vendorID, Fields: map[string]json.RawMessage{
			"hardwareRevision":   json.RawMessage(`"B2"`),
			"cabinetTemperature": json.RawMessage(`41.5`),
		}},
	})
	if err != nil {
		return err
	}
	if boot.CustomData != nil {
		fmt.Printf("station: boot %s, customData siteId=%s\n", boot.Status, boot.CustomData.Fields["siteId"])
	}

	// DataTransfer: data is any JSON value in 2.x.
	for _, msg := range []struct{ vendor, id string }{{vendorID, "GetTariff"}, {vendorID, "GetWeather"}, {"com.example", "GetTariff"}} {
		resp, err := ocpp201.DataTransfer(ctx, c, &ocpp201.DataTransferRequest{VendorID: msg.vendor, MessageID: ocpp.Ptr(msg.id), Data: json.RawMessage(`{"connectorId":1}`)})
		if err != nil {
			return err
		}
		fmt.Printf("station: DataTransfer %s/%s: %s %s\n", msg.vendor, msg.id, resp.Status, string(resp.Data))
	}
	resp, err := ocpp201.DataTransfer(ctx, s, &ocpp201.DataTransferRequest{VendorID: vendorID, MessageID: ocpp.Ptr("SetLedColor"), Data: json.RawMessage(`{"color":"blue"}`)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: DataTransfer %s/SetLedColor: %s\n", vendorID, resp.Status)

	// A raw call: an action go-ocpp does not model, handled by
	// withVendorActions on the CSMS. The payload goes out as is.
	var reply json.RawMessage
	if err := c.Call(ctx, "VendorStatus", json.RawMessage(`{"fan":"ok","filterChangeDue":"2026-12-01"}`), &reply); err != nil {
		return err
	}
	fmt.Printf("station: raw VendorStatus answered %s\n", reply)

	// Validation still applies. A typed call refuses an invalid message
	// (customData.vendorId is at most 255 characters) ...
	_, err = ocpp201.Heartbeat(ctx, c, &ocpp201.HeartbeatRequest{CustomData: &ocpp201.CustomData{VendorID: strings.Repeat("v", 256)}})
	var ve *ocpp.ValidationError
	if !errors.As(err, &ve) {
		return fmt.Errorf("expected a validation error, got %v", err)
	}
	fmt.Printf("station: not sent: %v\n", err)
	// ... and a standard action built by hand is validated by the
	// receiver: Heartbeat has no property "uptime".
	err = c.Call(ctx, "Heartbeat", json.RawMessage(`{"uptime":3600}`), nil)
	var ce *ocpp.CallError
	if !errors.As(err, &ce) {
		return fmt.Errorf("expected a CALLERROR, got %v", err)
	}
	fmt.Printf("station: raw Heartbeat refused by the CSMS: %s (%s)\n", ce.Code, ce.Description)
	return nil
}

func demo21(ctx context.Context, c *client.Client, s *server.Session) error {
	if _, err := ocpp21.BootNotification(ctx, c, &ocpp21.BootNotificationRequest{Reason: ocpp21.BootReasonPowerUp,
		ChargingStation: ocpp21.ChargingStationType{VendorName: "OT Fabric", Model: "VendorStation"}}); err != nil {
		return err
	}
	if _, err := ocpp21.StatusNotification(ctx, c, &ocpp21.StatusNotificationRequest{
		Timestamp: time.Now().UTC(), ConnectorStatus: ocpp21.ConnectorStatusAvailable, EVSEID: 1, ConnectorID: 1,
		CustomData: &ocpp21.CustomData{VendorID: vendorID, Fields: map[string]json.RawMessage{
			"lockState": json.RawMessage(`"Unlocked"`), "cableTemperature": json.RawMessage(`22`),
		}},
	}); err != nil {
		return err
	}
	resp, err := ocpp21.DataTransfer(ctx, c, &ocpp21.DataTransferRequest{VendorID: vendorID, MessageID: ocpp.Ptr("GetTariff"), Data: json.RawMessage(`{"connectorId":2}`)})
	if err != nil {
		return err
	}
	fmt.Printf("station: DataTransfer %s/GetTariff: %s %s\n", vendorID, resp.Status, string(resp.Data))
	back, err := ocpp21.DataTransfer(ctx, s, &ocpp21.DataTransferRequest{VendorID: vendorID, MessageID: ocpp.Ptr("SetLedColor"), Data: json.RawMessage(`{"color":"amber"}`)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: DataTransfer %s/SetLedColor: %s\n", vendorID, back.Status)
	return nil
}
