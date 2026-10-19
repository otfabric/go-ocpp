// SPDX-License-Identifier: MIT

// Command station-simulator is a charging station simulator for testing a
// CSMS, on OCPP 1.6, 2.0.1 or 2.1. It boots, reports its connector
// Available, sends heartbeats at the interval the CSMS sets, and runs
// charging sessions: Authorize, start a transaction, meter values with an
// increasing energy register, stop the transaction, Available again. It
// accepts a remote start and a reset from the CSMS.
//
// Without -url it starts an embedded go-ocpp CSMS that accepts everything,
// asks for one remote start and a reset, so the example runs and exits by
// itself. With -url it connects to your CSMS:
//
//	go run ./examples/station-simulator -version 2.0.1 -url ws://127.0.0.1:8887/ocpp/CS201-0001 -password cs201-example-password-001
//
// Plain ws:// with a password is security profile 1, for a trusted network;
// wss:// URLs use TLS with the system roots (profile 2).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

const token = "04A1B2C3D4E5F6"

func main() {
	version := flag.String("version", "2.0.1", "OCPP version: 1.6, 2.0.1 or 2.1")
	url := flag.String("url", "", "CSMS URL ending in the station identity, e.g. ws://host:port/ocpp/CS-0001 (default: an embedded CSMS)")
	password := flag.String("password", "", "HTTP Basic password; the user name is the station identity")
	sessions := flag.Int("sessions", 1, "number of charging sessions to run")
	linger := flag.Duration("linger", 0, "with -url: stay connected this long after the sessions for a remote start or reset")
	flag.Parse()
	v, ok := ocpp.ParseVersion(*version)
	if !ok {
		log.Fatalf("unknown OCPP version %q", *version)
	}
	if err := run(v, *url, *password, *sessions, *linger); err != nil {
		log.Fatal(err)
	}
}

func run(v ocpp.Version, url, password string, sessions int, linger time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute+linger)
	defer cancel()

	embedded := url == ""
	var csms *embeddedCSMS
	maxHeartbeat := time.Duration(0)
	if embedded {
		var err error
		if csms, err = startCSMS(sessions); err != nil {
			return err
		}
		defer csms.close()
		url, password = csms.url+"SIM-0001", ""
		maxHeartbeat = time.Second // a short interval, so the demo shows some
		linger = 10 * time.Second  // ends early with the reset
	}

	st := &station{cmds: make(chan command, 4)}
	opts := []client.Option{
		client.WithVersions(v),
		// A station SHALL reconnect after losing the connection (2.x
		// Part 4 §5.4). Nothing is resent: a call that was pending fails.
		client.WithReconnect(client.DefaultReconnect()),
		client.WithStateHandler(func(s ocpp.State, err error) {
			if err != nil {
				fmt.Printf("station: connection %s (%v)\n", s, err)
			}
		}),
	}
	if strings.HasPrefix(url, "ws://") {
		opts = append(opts, client.WithInsecureTransport())
	}
	if password != "" {
		opts = append(opts, client.WithBasicAuth(url[strings.LastIndex(url, "/")+1:], password))
	}
	switch v {
	case ocpp.V16:
		opts = append(opts, client.WithHandler(v, ocpp16.ChargePoint(handler16{st: st})))
	case ocpp.V201:
		opts = append(opts, client.WithHandler(v, ocpp201.ChargingStation(handler201{st: st})))
	case ocpp.V21:
		opts = append(opts, client.WithHandler(v, ocpp21.ChargingStation(handler21{st: st})))
	}
	c, err := client.New(url, opts...)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	fmt.Printf("station: %s connected to %s on OCPP %s\n", c.ID(), url, c.Version())
	switch v {
	case ocpp.V16:
		st.ops = &ops16{c: c}
	case ocpp.V201:
		st.ops = &ops201{c: c}
	case ocpp.V21:
		st.ops = &ops21{c: c}
	}

	// Boot until accepted (2.x B01-B03; 1.6 §4.2): Pending and Rejected
	// mean "try again after interval".
	var interval time.Duration
	for {
		status, secs, err := st.ops.boot(ctx)
		if err != nil {
			return err
		}
		interval = time.Duration(secs) * time.Second
		fmt.Printf("station: boot %s, heartbeat interval %s\n", status, interval)
		if status == "Accepted" {
			break
		}
		if interval <= 0 {
			interval = 10 * time.Second
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if maxHeartbeat > 0 && interval > maxHeartbeat {
		fmt.Printf("station: heartbeat capped to %s for this demo\n", maxHeartbeat)
		interval = maxHeartbeat
	}
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	if interval > 0 {
		go st.heartbeat(hbCtx, interval)
	}

	if err := st.ops.status(ctx, false); err != nil {
		return err
	}
	for i := 1; i <= sessions; i++ {
		if err := st.session(ctx, i, nil); err != nil {
			return err
		}
	}

	// Stay for what the CSMS asks.
	deadline := time.After(linger)
	for n := sessions + 1; ; n++ {
		select {
		case cmd := <-st.cmds:
			if cmd.reset != "" {
				// A real station would now stop its transactions, close the
				// connection and reboot; examples/remote-management shows
				// the reboot.
				fmt.Printf("station: %s reset, shutting down\n", cmd.reset)
				return nil
			}
			if err := st.session(ctx, n, cmd.remoteStartID); err != nil {
				return err
			}
		case <-deadline:
			fmt.Println("station: done")
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// The station.

type command struct {
	remoteStartID *int   // a remote start; 1.6 has no id: 0
	reset         string // a reset of this type
}

// ops are the station's messages in one OCPP version.
type ops interface {
	boot(ctx context.Context) (status string, interval int, err error)
	heartbeat(ctx context.Context) error
	status(ctx context.Context, occupied bool) error
	authorize(ctx context.Context) (bool, error)
	start(ctx context.Context, remoteStartID *int, wh float64) (txID string, err error)
	meter(ctx context.Context, txID string, wh float64) error
	stop(ctx context.Context, txID string, wh float64) error
}

type station struct {
	ops      ops
	cmds     chan command
	charging atomic.Bool
	energyWh float64 // the meter register: it only increases
}

func (s *station) heartbeat(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := s.ops.heartbeat(ctx); err != nil && ctx.Err() == nil {
				fmt.Printf("station: heartbeat failed: %v\n", err)
			} else if err == nil {
				fmt.Println("station: heartbeat")
			}
		case <-ctx.Done():
			return
		}
	}
}

// session runs one charging session; remoteStartID is set for a session
// the CSMS started.
func (s *station) session(ctx context.Context, n int, remoteStartID *int) error {
	if remoteStartID == nil {
		ok, err := s.ops.authorize(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("station: session %d: token %s accepted=%v\n", n, token, ok)
		if !ok {
			return nil
		}
	} else {
		fmt.Printf("station: session %d: started by the CSMS\n", n)
	}
	s.charging.Store(true)
	if err := s.ops.status(ctx, true); err != nil {
		return err
	}
	tx, err := s.ops.start(ctx, remoteStartID, s.energyWh)
	if err != nil {
		return err
	}
	fmt.Printf("station: session %d: transaction %s started at %.0f Wh\n", n, tx, s.energyWh)
	for range 3 {
		time.Sleep(300 * time.Millisecond) // 11 kW for a while, sped up
		s.energyWh += 920
		if err := s.ops.meter(ctx, tx, s.energyWh); err != nil {
			return err
		}
		fmt.Printf("station: session %d: %.0f Wh\n", n, s.energyWh)
	}
	if err := s.ops.stop(ctx, tx, s.energyWh); err != nil {
		return err
	}
	fmt.Printf("station: session %d: transaction %s stopped at %.0f Wh\n", n, tx, s.energyWh)
	s.charging.Store(false)
	return s.ops.status(ctx, false)
}

// post hands a command to the station loop without blocking the handler.
func (s *station) post(c command) bool {
	select {
	case s.cmds <- c:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Handlers for the CSMS's CALLs, per version.

type handler16 struct {
	ocpp16.UnimplementedChargePointHandler
	st *station
}

func (h handler16) RemoteStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.RemoteStartTransactionRequest) (*ocpp16.RemoteStartTransactionResponse, error) {
	fmt.Printf("station: remote start for %s\n", req.IDTag)
	if h.st.charging.Load() || !h.st.post(command{remoteStartID: ocpp.Ptr(0)}) {
		return &ocpp16.RemoteStartTransactionResponse{Status: ocpp16.RemoteStartStopStatusRejected}, nil
	}
	return &ocpp16.RemoteStartTransactionResponse{Status: ocpp16.RemoteStartStopStatusAccepted}, nil
}

func (h handler16) Reset(ctx context.Context, p ocpp.Peer, req *ocpp16.ResetRequest) (*ocpp16.ResetResponse, error) {
	fmt.Printf("station: reset %s requested\n", req.Type)
	// Shut down once the Reset response is on its way.
	ocpp.AfterResponse(ctx, func() { h.st.post(command{reset: string(req.Type)}) })
	return &ocpp16.ResetResponse{Status: ocpp16.ResetStatusAccepted}, nil
}

type handler201 struct {
	ocpp201.UnimplementedChargingStationHandler
	st *station
}

func (h handler201) RequestStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp201.RequestStartTransactionRequest) (*ocpp201.RequestStartTransactionResponse, error) {
	fmt.Printf("station: remote start %d for %s\n", req.RemoteStartID, req.IDToken.IDToken)
	if h.st.charging.Load() || !h.st.post(command{remoteStartID: ocpp.Ptr(req.RemoteStartID)}) {
		return &ocpp201.RequestStartTransactionResponse{Status: ocpp201.RequestStartStopStatusRejected}, nil
	}
	return &ocpp201.RequestStartTransactionResponse{Status: ocpp201.RequestStartStopStatusAccepted}, nil
}

func (h handler201) Reset(ctx context.Context, p ocpp.Peer, req *ocpp201.ResetRequest) (*ocpp201.ResetResponse, error) {
	fmt.Printf("station: reset %s requested\n", req.Type)
	// Shut down once the Reset response is on its way.
	ocpp.AfterResponse(ctx, func() { h.st.post(command{reset: string(req.Type)}) })
	// OnIdle during a transaction: Scheduled, after it ends (B12).
	if req.Type == ocpp201.ResetOnIdle && h.st.charging.Load() {
		return &ocpp201.ResetResponse{Status: ocpp201.ResetStatusScheduled}, nil
	}
	return &ocpp201.ResetResponse{Status: ocpp201.ResetStatusAccepted}, nil
}

type handler21 struct {
	ocpp21.UnimplementedChargingStationHandler
	st *station
}

func (h handler21) RequestStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp21.RequestStartTransactionRequest) (*ocpp21.RequestStartTransactionResponse, error) {
	fmt.Printf("station: remote start %d for %s\n", req.RemoteStartID, req.IDToken.IDToken)
	if h.st.charging.Load() || !h.st.post(command{remoteStartID: ocpp.Ptr(req.RemoteStartID)}) {
		return &ocpp21.RequestStartTransactionResponse{Status: ocpp21.RequestStartStopStatusRejected}, nil
	}
	return &ocpp21.RequestStartTransactionResponse{Status: ocpp21.RequestStartStopStatusAccepted}, nil
}

func (h handler21) Reset(ctx context.Context, p ocpp.Peer, req *ocpp21.ResetRequest) (*ocpp21.ResetResponse, error) {
	fmt.Printf("station: reset %s requested\n", req.Type)
	// Shut down once the Reset response is on its way.
	ocpp.AfterResponse(ctx, func() { h.st.post(command{reset: string(req.Type)}) })
	if req.Type == ocpp21.ResetOnIdle && h.st.charging.Load() {
		return &ocpp21.ResetResponse{Status: ocpp21.ResetStatusScheduled}, nil
	}
	return &ocpp21.ResetResponse{Status: ocpp21.ResetStatusAccepted}, nil
}

// ---------------------------------------------------------------------------
// OCPP 1.6 messages.

type ops16 struct{ c *client.Client }

func (o *ops16) boot(ctx context.Context) (string, int, error) {
	resp, err := ocpp16.BootNotification(ctx, o.c, &ocpp16.BootNotificationRequest{
		ChargePointVendor: "OT Fabric", ChargePointModel: "Simulator", FirmwareVersion: ocpp.Ptr("0.1.0"),
	})
	if err != nil {
		return "", 0, err
	}
	return string(resp.Status), resp.Interval, nil
}

func (o *ops16) heartbeat(ctx context.Context) error {
	_, err := ocpp16.Heartbeat(ctx, o.c, &ocpp16.HeartbeatRequest{})
	return err
}

func (o *ops16) status(ctx context.Context, occupied bool) error {
	s := ocpp16.ChargePointStatusAvailable
	if occupied {
		s = ocpp16.ChargePointStatusCharging
	}
	_, err := ocpp16.StatusNotification(ctx, o.c, &ocpp16.StatusNotificationRequest{ConnectorID: 1, ErrorCode: ocpp16.ChargePointErrorCodeNoError, Status: s, Timestamp: ocpp.Ptr(time.Now().UTC())})
	return err
}

func (o *ops16) authorize(ctx context.Context) (bool, error) {
	resp, err := ocpp16.Authorize(ctx, o.c, &ocpp16.AuthorizeRequest{IDTag: token})
	if err != nil {
		return false, err
	}
	return resp.IDTagInfo.Status == ocpp16.AuthorizationStatusAccepted, nil
}

func (o *ops16) start(ctx context.Context, _ *int, wh float64) (string, error) {
	resp, err := ocpp16.StartTransaction(ctx, o.c, &ocpp16.StartTransactionRequest{ConnectorID: 1, IDTag: token, MeterStart: int(wh), Timestamp: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	if resp.IDTagInfo.Status != ocpp16.AuthorizationStatusAccepted {
		fmt.Printf("station: the CSMS did not accept the token (%s); a real station stops the transaction\n", resp.IDTagInfo.Status)
	}
	return strconv.Itoa(resp.TransactionID), nil
}

func (o *ops16) meter(ctx context.Context, txID string, wh float64) error {
	id, _ := strconv.Atoi(txID)
	_, err := ocpp16.MeterValues(ctx, o.c, &ocpp16.MeterValuesRequest{ConnectorID: 1, TransactionID: &id, MeterValue: []ocpp16.MeterValue{{
		Timestamp: time.Now().UTC(),
		SampledValue: []ocpp16.SampledValue{{
			Value:     strconv.FormatFloat(wh, 'f', 0, 64),
			Context:   ocpp.Ptr(ocpp16.ReadingContextSamplePeriodic),
			Measurand: ocpp.Ptr(ocpp16.MeasurandEnergyActiveImportRegister),
			Unit:      ocpp.Ptr(ocpp16.UnitOfMeasureWh),
		}},
	}}})
	return err
}

func (o *ops16) stop(ctx context.Context, txID string, wh float64) error {
	id, _ := strconv.Atoi(txID)
	_, err := ocpp16.StopTransaction(ctx, o.c, &ocpp16.StopTransactionRequest{
		TransactionID: id, Timestamp: time.Now().UTC(), MeterStop: int(wh), IDTag: ocpp.Ptr(token), Reason: ocpp.Ptr(ocpp16.ReasonLocal),
	})
	return err
}

// ---------------------------------------------------------------------------
// OCPP 2.0.1 messages. Transaction meter values go in TransactionEvent
// (2.0.1 Part 2 J02); MeterValues is for values outside a transaction.

type ops201 struct {
	c     *client.Client
	seqNo int
	n     int
}

func (o *ops201) boot(ctx context.Context) (string, int, error) {
	resp, err := ocpp201.BootNotification(ctx, o.c, &ocpp201.BootNotificationRequest{Reason: ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "Simulator", FirmwareVersion: ocpp.Ptr("0.1.0")}})
	if err != nil {
		return "", 0, err
	}
	return string(resp.Status), resp.Interval, nil
}

func (o *ops201) heartbeat(ctx context.Context) error {
	_, err := ocpp201.Heartbeat(ctx, o.c, &ocpp201.HeartbeatRequest{})
	return err
}

func (o *ops201) status(ctx context.Context, occupied bool) error {
	s := ocpp201.ConnectorStatusAvailable
	if occupied {
		s = ocpp201.ConnectorStatusOccupied
	}
	_, err := ocpp201.StatusNotification(ctx, o.c, &ocpp201.StatusNotificationRequest{Timestamp: time.Now().UTC(), ConnectorStatus: s, EVSEID: 1, ConnectorID: 1})
	return err
}

func (o *ops201) idToken() ocpp201.IDTokenType {
	return ocpp201.IDTokenType{IDToken: token, Type: ocpp201.IDTokenISO14443}
}

func (o *ops201) authorize(ctx context.Context) (bool, error) {
	resp, err := ocpp201.Authorize(ctx, o.c, &ocpp201.AuthorizeRequest{IDToken: o.idToken()})
	if err != nil {
		return false, err
	}
	return resp.IDTokenInfo.Status == ocpp201.AuthorizationStatusAccepted, nil
}

func (o *ops201) event(ctx context.Context, req ocpp201.TransactionEventRequest, wh float64, rc ocpp201.ReadingContextEnumType) error {
	req.Timestamp, req.SeqNo = time.Now().UTC(), o.seqNo
	o.seqNo++
	req.MeterValue = []ocpp201.MeterValueType{{Timestamp: req.Timestamp, SampledValue: []ocpp201.SampledValueType{{
		Value: wh, Context: &rc, Measurand: ocpp.Ptr(ocpp201.MeasurandEnergyActiveImportRegister),
		UnitOfMeasure: &ocpp201.UnitOfMeasureType{Unit: ocpp.Ptr("Wh")},
	}}}}
	_, err := ocpp201.TransactionEvent(ctx, o.c, &req)
	return err
}

func (o *ops201) start(ctx context.Context, remoteStartID *int, wh float64) (string, error) {
	o.n++
	id := fmt.Sprintf("SIM-%d-%d", time.Now().Unix(), o.n)
	trigger := ocpp201.TriggerReasonAuthorized
	if remoteStartID != nil {
		trigger = ocpp201.TriggerReasonRemoteStart
	}
	tok := o.idToken()
	return id, o.event(ctx, ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventStarted, TriggerReason: trigger,
		TransactionInfo: ocpp201.TransactionType{TransactionID: id, ChargingState: ocpp.Ptr(ocpp201.ChargingStateCharging), RemoteStartID: remoteStartID},
		EVSE:            &ocpp201.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)}, IDToken: &tok,
	}, wh, ocpp201.ReadingContextTransactionBegin)
}

func (o *ops201) meter(ctx context.Context, txID string, wh float64) error {
	return o.event(ctx, ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventUpdated, TriggerReason: ocpp201.TriggerReasonMeterValuePeriodic,
		TransactionInfo: ocpp201.TransactionType{TransactionID: txID},
	}, wh, ocpp201.ReadingContextSamplePeriodic)
}

func (o *ops201) stop(ctx context.Context, txID string, wh float64) error {
	return o.event(ctx, ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventEnded, TriggerReason: ocpp201.TriggerReasonEVDeparted,
		TransactionInfo: ocpp201.TransactionType{TransactionID: txID, ChargingState: ocpp.Ptr(ocpp201.ChargingStateIdle),
			StoppedReason: ocpp.Ptr(ocpp201.ReasonEVDisconnected)},
	}, wh, ocpp201.ReadingContextTransactionEnd)
}

// ---------------------------------------------------------------------------
// OCPP 2.1 messages: as 2.0.1, with the ocpp21 types.

type ops21 struct {
	c     *client.Client
	seqNo int
	n     int
}

func (o *ops21) boot(ctx context.Context) (string, int, error) {
	resp, err := ocpp21.BootNotification(ctx, o.c, &ocpp21.BootNotificationRequest{Reason: ocpp21.BootReasonPowerUp,
		ChargingStation: ocpp21.ChargingStationType{VendorName: "OT Fabric", Model: "Simulator", FirmwareVersion: ocpp.Ptr("0.1.0")}})
	if err != nil {
		return "", 0, err
	}
	return string(resp.Status), resp.Interval, nil
}

func (o *ops21) heartbeat(ctx context.Context) error {
	_, err := ocpp21.Heartbeat(ctx, o.c, &ocpp21.HeartbeatRequest{})
	return err
}

func (o *ops21) status(ctx context.Context, occupied bool) error {
	s := ocpp21.ConnectorStatusAvailable
	if occupied {
		s = ocpp21.ConnectorStatusOccupied
	}
	_, err := ocpp21.StatusNotification(ctx, o.c, &ocpp21.StatusNotificationRequest{Timestamp: time.Now().UTC(), ConnectorStatus: s, EVSEID: 1, ConnectorID: 1})
	return err
}

func (o *ops21) idToken() ocpp21.IDTokenType {
	return ocpp21.IDTokenType{IDToken: token, Type: "ISO14443"} // a string in 2.1
}

func (o *ops21) authorize(ctx context.Context) (bool, error) {
	resp, err := ocpp21.Authorize(ctx, o.c, &ocpp21.AuthorizeRequest{IDToken: o.idToken()})
	if err != nil {
		return false, err
	}
	return resp.IDTokenInfo.Status == ocpp21.AuthorizationStatusAccepted, nil
}

func (o *ops21) event(ctx context.Context, req ocpp21.TransactionEventRequest, wh float64, rc ocpp21.ReadingContextEnumType) error {
	req.Timestamp, req.SeqNo = time.Now().UTC(), o.seqNo
	o.seqNo++
	req.MeterValue = []ocpp21.MeterValueType{{Timestamp: req.Timestamp, SampledValue: []ocpp21.SampledValueType{{
		Value: wh, Context: &rc, Measurand: ocpp.Ptr(ocpp21.MeasurandEnergyActiveImportRegister),
		UnitOfMeasure: &ocpp21.UnitOfMeasureType{Unit: ocpp.Ptr("Wh")},
	}}}}
	_, err := ocpp21.TransactionEvent(ctx, o.c, &req)
	return err
}

func (o *ops21) start(ctx context.Context, remoteStartID *int, wh float64) (string, error) {
	o.n++
	id := fmt.Sprintf("SIM-%d-%d", time.Now().Unix(), o.n)
	trigger := ocpp21.TriggerReasonAuthorized
	if remoteStartID != nil {
		trigger = ocpp21.TriggerReasonRemoteStart
	}
	tok := o.idToken()
	return id, o.event(ctx, ocpp21.TransactionEventRequest{
		EventType: ocpp21.TransactionEventStarted, TriggerReason: trigger,
		TransactionInfo: ocpp21.TransactionType{TransactionID: id, ChargingState: ocpp.Ptr(ocpp21.ChargingStateCharging), RemoteStartID: remoteStartID},
		EVSE:            &ocpp21.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)}, IDToken: &tok,
	}, wh, ocpp21.ReadingContextTransactionBegin)
}

func (o *ops21) meter(ctx context.Context, txID string, wh float64) error {
	return o.event(ctx, ocpp21.TransactionEventRequest{
		EventType: ocpp21.TransactionEventUpdated, TriggerReason: ocpp21.TriggerReasonMeterValuePeriodic,
		TransactionInfo: ocpp21.TransactionType{TransactionID: txID},
	}, wh, ocpp21.ReadingContextSamplePeriodic)
}

func (o *ops21) stop(ctx context.Context, txID string, wh float64) error {
	return o.event(ctx, ocpp21.TransactionEventRequest{
		EventType: ocpp21.TransactionEventEnded, TriggerReason: ocpp21.TriggerReasonEVDeparted,
		TransactionInfo: ocpp21.TransactionType{TransactionID: txID, ChargingState: ocpp.Ptr(ocpp21.ChargingStateIdle),
			StoppedReason: ocpp.Ptr(ocpp21.ReasonEVDisconnected)},
	}, wh, ocpp21.ReadingContextTransactionEnd)
}

// ---------------------------------------------------------------------------
// The embedded CSMS: accepts everything and, once the station has finished
// its sessions, asks for a remote start and then a reset.

type embeddedCSMS struct {
	srv      *server.Server
	url      string
	sessions int         // the station's own sessions
	idle     chan string // station identities, one per Available status
	done     chan struct{}
}

func startCSMS(sessions int) (*embeddedCSMS, error) {
	e := &embeddedCSMS{sessions: sessions, idle: make(chan string, 16), done: make(chan struct{})}
	var err error
	e.srv, err = server.New(
		server.WithHandler(ocpp.V16, ocpp16.CentralSystem(csms16{e: e})),
		server.WithHandler(ocpp.V201, ocpp201.CSMS(csms201{e: e})),
		server.WithHandler(ocpp.V21, ocpp21.CSMS(csms21{e: e})),
		server.WithoutAuthentication(), // the embedded CSMS only; see examples/csms
	)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	go func() { _ = e.srv.Serve(ln) }()
	e.url = "ws://" + ln.Addr().String() + "/ocpp/"
	go e.drive()
	return e, nil
}

// drive asks for a remote start once the station is Available after its
// own sessions, and for a reset when it is Available after that one.
func (e *embeddedCSMS) drive() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-e.done
		cancel()
	}()
	for n := 1; ; n++ {
		var id string
		select {
		case id = <-e.idle:
		case <-ctx.Done():
			return
		}
		s, ok := e.srv.Station(id)
		if !ok {
			continue
		}
		var err error
		switch n {
		case e.sessions + 1: // the boot, then one per session
			err = e.remoteStart(ctx, s)
		case e.sessions + 2:
			err = e.reset(ctx, s)
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Printf("CSMS: %v\n", err)
		}
	}
}

func (e *embeddedCSMS) remoteStart(ctx context.Context, s *server.Session) error {
	var status any
	switch s.Version() {
	case ocpp.V16:
		resp, err := ocpp16.RemoteStartTransaction(ctx, s, &ocpp16.RemoteStartTransactionRequest{IDTag: token, ConnectorID: ocpp.Ptr(1)})
		if err != nil {
			return err
		}
		status = resp.Status
	case ocpp.V201:
		resp, err := ocpp201.RequestStartTransaction(ctx, s, &ocpp201.RequestStartTransactionRequest{RemoteStartID: 42, EVSEID: ocpp.Ptr(1),
			IDToken: ocpp201.IDTokenType{IDToken: token, Type: ocpp201.IDTokenISO14443}})
		if err != nil {
			return err
		}
		status = resp.Status
	case ocpp.V21:
		resp, err := ocpp21.RequestStartTransaction(ctx, s, &ocpp21.RequestStartTransactionRequest{RemoteStartID: 42, EVSEID: ocpp.Ptr(1),
			IDToken: ocpp21.IDTokenType{IDToken: token, Type: "ISO14443"}})
		if err != nil {
			return err
		}
		status = resp.Status
	}
	fmt.Printf("CSMS: remote start: %s\n", status)
	return nil
}

func (e *embeddedCSMS) reset(ctx context.Context, s *server.Session) error {
	var status any
	switch s.Version() {
	case ocpp.V16:
		resp, err := ocpp16.Reset(ctx, s, &ocpp16.ResetRequest{Type: ocpp16.ResetSoft})
		if err != nil {
			return err
		}
		status = resp.Status
	case ocpp.V201:
		resp, err := ocpp201.Reset(ctx, s, &ocpp201.ResetRequest{Type: ocpp201.ResetOnIdle})
		if err != nil {
			return err
		}
		status = resp.Status
	case ocpp.V21:
		resp, err := ocpp21.Reset(ctx, s, &ocpp21.ResetRequest{Type: ocpp21.ResetOnIdle})
		if err != nil {
			return err
		}
		status = resp.Status
	}
	fmt.Printf("CSMS: reset: %s\n", status)
	return nil
}

func (e *embeddedCSMS) close() {
	close(e.done)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = e.srv.Shutdown(ctx)
}

func (e *embeddedCSMS) available(id string) {
	select {
	case e.idle <- id:
	default:
	}
}

type csms16 struct {
	ocpp16.UnimplementedCentralSystemHandler
	e *embeddedCSMS
}

func (csms16) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.BootNotificationRequest) (*ocpp16.BootNotificationResponse, error) {
	return &ocpp16.BootNotificationResponse{Status: ocpp16.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms16) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp16.HeartbeatRequest) (*ocpp16.HeartbeatResponse, error) {
	return &ocpp16.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (h csms16) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.StatusNotificationRequest) (*ocpp16.StatusNotificationResponse, error) {
	fmt.Printf("CSMS: connector %d %s\n", req.ConnectorID, req.Status)
	if req.Status == ocpp16.ChargePointStatusAvailable {
		h.e.available(p.ID())
	}
	return &ocpp16.StatusNotificationResponse{}, nil
}

func (csms16) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp16.AuthorizeRequest) (*ocpp16.AuthorizeResponse, error) {
	return &ocpp16.AuthorizeResponse{IDTagInfo: ocpp16.IDTagInfo{Status: ocpp16.AuthorizationStatusAccepted}}, nil
}

func (csms16) StartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.StartTransactionRequest) (*ocpp16.StartTransactionResponse, error) {
	return &ocpp16.StartTransactionResponse{IDTagInfo: ocpp16.IDTagInfo{Status: ocpp16.AuthorizationStatusAccepted}, TransactionID: int(time.Now().Unix() % 100000)}, nil
}

func (csms16) MeterValues(ctx context.Context, p ocpp.Peer, req *ocpp16.MeterValuesRequest) (*ocpp16.MeterValuesResponse, error) {
	return &ocpp16.MeterValuesResponse{}, nil
}

func (csms16) StopTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.StopTransactionRequest) (*ocpp16.StopTransactionResponse, error) {
	fmt.Printf("CSMS: transaction %d stopped at %d Wh\n", req.TransactionID, req.MeterStop)
	return &ocpp16.StopTransactionResponse{}, nil
}

type csms201 struct {
	ocpp201.UnimplementedCSMSHandler
	e *embeddedCSMS
}

func (csms201) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms201) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp201.HeartbeatRequest) (*ocpp201.HeartbeatResponse, error) {
	return &ocpp201.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (h csms201) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.StatusNotificationRequest) (*ocpp201.StatusNotificationResponse, error) {
	fmt.Printf("CSMS: EVSE %d %s\n", req.EVSEID, req.ConnectorStatus)
	if req.ConnectorStatus == ocpp201.ConnectorStatusAvailable {
		h.e.available(p.ID())
	}
	return &ocpp201.StatusNotificationResponse{}, nil
}

func (csms201) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp201.AuthorizeRequest) (*ocpp201.AuthorizeResponse, error) {
	return &ocpp201.AuthorizeResponse{IDTokenInfo: ocpp201.IDTokenInfoType{Status: ocpp201.AuthorizationStatusAccepted}}, nil
}

func (csms201) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp201.TransactionEventRequest) (*ocpp201.TransactionEventResponse, error) {
	if req.EventType == ocpp201.TransactionEventEnded {
		fmt.Printf("CSMS: transaction %s ended (%s)\n", req.TransactionInfo.TransactionID, req.TriggerReason)
	}
	return &ocpp201.TransactionEventResponse{}, nil
}

type csms21 struct {
	ocpp21.UnimplementedCSMSHandler
	e *embeddedCSMS
}

func (csms21) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.BootNotificationRequest) (*ocpp21.BootNotificationResponse, error) {
	return &ocpp21.BootNotificationResponse{Status: ocpp21.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms21) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp21.HeartbeatRequest) (*ocpp21.HeartbeatResponse, error) {
	return &ocpp21.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (h csms21) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.StatusNotificationRequest) (*ocpp21.StatusNotificationResponse, error) {
	fmt.Printf("CSMS: EVSE %d %s\n", req.EVSEID, req.ConnectorStatus)
	if req.ConnectorStatus == ocpp21.ConnectorStatusAvailable {
		h.e.available(p.ID())
	}
	return &ocpp21.StatusNotificationResponse{}, nil
}

func (csms21) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp21.AuthorizeRequest) (*ocpp21.AuthorizeResponse, error) {
	return &ocpp21.AuthorizeResponse{IDTokenInfo: ocpp21.IDTokenInfoType{Status: ocpp21.AuthorizationStatusAccepted}}, nil
}

func (csms21) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp21.TransactionEventRequest) (*ocpp21.TransactionEventResponse, error) {
	if req.EventType == ocpp21.TransactionEventEnded {
		fmt.Printf("CSMS: transaction %s ended (%s)\n", req.TransactionInfo.TransactionID, req.TriggerReason)
	}
	return &ocpp21.TransactionEventResponse{}, nil
}
