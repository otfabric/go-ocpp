// SPDX-License-Identifier: MIT

// Command remote-management shows the remote operations of an OCPP 2.0.1
// CSMS on a charging station, in one process:
//
//   - TriggerMessage: the station answers, then sends the message asked
//     for (StatusNotification, MeterValues, LogStatusNotification);
//   - UpdateFirmware: the station reports its progress with
//     FirmwareStatusNotification (Downloading, Downloaded, Installing,
//     Installed);
//   - GetLog: the station uploads a log and reports it with
//     LogStatusNotification (Uploading, Uploaded);
//   - Reset OnIdle during a transaction: Scheduled, and the station resets
//     when the transaction ends; Reset Immediate: at once. Each time the
//     station "reboots": it closes its connection, connects again and sends
//     BootNotification with reason RemoteReset.
//
// Nothing is downloaded or uploaded: timers stand in for the work.
//
// For brevity it uses plain WebSocket on the loopback interface without
// authentication; see examples/security-profiles.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/server"
)

const stationID = "RM-0001"

// step is how long each simulated piece of work takes.
const step = 150 * time.Millisecond

// later runs f after the response to the CALL being handled has gone out
// (ocpp.AfterResponse), and after d more, which stands for the work the
// station does: the messages f sends always follow the response.
func later(ctx context.Context, d time.Duration, f func()) {
	ocpp.AfterResponse(ctx, func() {
		time.Sleep(d)
		f()
	})
}

// ---------------------------------------------------------------------------
// The charging station.

type station struct {
	ocpp201.UnimplementedChargingStationHandler
	reboot       chan struct{} // to the station's main loop
	txActive     atomic.Bool
	resetPending atomic.Bool
	firmware     atomic.Value // string: the installed version
	uploading    atomic.Bool
}

func (s *station) requestReboot() {
	select {
	case s.reboot <- struct{}{}:
	default:
	}
}

// TriggerMessage (F06): answer first, then send the message.
func (s *station) TriggerMessage(ctx context.Context, p ocpp.Peer, req *ocpp201.TriggerMessageRequest) (*ocpp201.TriggerMessageResponse, error) {
	var send func() error
	switch req.RequestedMessage {
	case ocpp201.MessageTriggerStatusNotification:
		if req.EVSE == nil || req.EVSE.ConnectorID == nil {
			return &ocpp201.TriggerMessageResponse{Status: ocpp201.TriggerMessageStatusRejected}, nil
		}
		evse, connector := req.EVSE.ID, *req.EVSE.ConnectorID
		send = func() error {
			status := ocpp201.ConnectorStatusAvailable
			if s.txActive.Load() {
				status = ocpp201.ConnectorStatusOccupied
			}
			_, err := ocpp201.StatusNotification(ctx, p, &ocpp201.StatusNotificationRequest{Timestamp: time.Now().UTC(),
				ConnectorStatus: status, EVSEID: evse, ConnectorID: connector})
			return err
		}
	case ocpp201.MessageTriggerMeterValues:
		send = func() error {
			_, err := ocpp201.MeterValues(ctx, p, &ocpp201.MeterValuesRequest{EVSEID: 1, MeterValue: []ocpp201.MeterValueType{{
				Timestamp: time.Now().UTC(), SampledValue: []ocpp201.SampledValueType{{
					Value: 12345, Context: ocpp.Ptr(ocpp201.ReadingContextTrigger),
					Measurand: ocpp.Ptr(ocpp201.MeasurandEnergyActiveImportRegister),
				}},
			}}})
			return err
		}
	case ocpp201.MessageTriggerLogStatusNotification:
		send = func() error {
			status := ocpp201.UploadLogStatusIdle // no upload in progress
			if s.uploading.Load() {
				status = ocpp201.UploadLogStatusUploading
			}
			_, err := ocpp201.LogStatusNotification(ctx, p, &ocpp201.LogStatusNotificationRequest{Status: status})
			return err
		}
	default:
		fmt.Printf("station: trigger %s not implemented\n", req.RequestedMessage)
		return &ocpp201.TriggerMessageResponse{Status: ocpp201.TriggerMessageStatusNotImplemented}, nil
	}
	fmt.Printf("station: trigger %s accepted\n", req.RequestedMessage)
	later(ctx, step, func() {
		if err := send(); err != nil {
			fmt.Printf("station: %v\n", err)
		}
	})
	return &ocpp201.TriggerMessageResponse{Status: ocpp201.TriggerMessageStatusAccepted}, nil
}

// UpdateFirmware (L01/L02): accept, then report each stage.
func (s *station) UpdateFirmware(ctx context.Context, p ocpp.Peer, req *ocpp201.UpdateFirmwareRequest) (*ocpp201.UpdateFirmwareResponse, error) {
	fmt.Printf("station: firmware update %d from %s\n", req.RequestID, req.Firmware.Location)
	stages := []ocpp201.FirmwareStatusEnumType{
		ocpp201.FirmwareStatusDownloading, ocpp201.FirmwareStatusDownloaded,
		ocpp201.FirmwareStatusInstalling, ocpp201.FirmwareStatusInstalled,
	}
	later(ctx, step, func() {
		for _, st := range stages {
			if st == ocpp201.FirmwareStatusInstalled {
				// A station whose new firmware needs a reboot reports
				// InstallRebooting, reboots, boots with reason
				// FirmwareUpdate and then reports Installed.
				s.firmware.Store("1.2.0")
			}
			if _, err := ocpp201.FirmwareStatusNotification(ctx, p, &ocpp201.FirmwareStatusNotificationRequest{
				Status: st, RequestID: ocpp.Ptr(req.RequestID)}); err != nil {
				fmt.Printf("station: %v\n", err)
				return
			}
			time.Sleep(step)
		}
	})
	return &ocpp201.UpdateFirmwareResponse{Status: ocpp201.UpdateFirmwareStatusAccepted}, nil
}

// GetLog (N01): accept with the file name, then upload and report.
func (s *station) GetLog(ctx context.Context, p ocpp.Peer, req *ocpp201.GetLogRequest) (*ocpp201.GetLogResponse, error) {
	name := fmt.Sprintf("%s-%s-%d.log", stationID, req.LogType, req.RequestID)
	fmt.Printf("station: uploading %s to %s\n", name, req.Log.RemoteLocation)
	s.uploading.Store(true)
	later(ctx, step, func() {
		defer s.uploading.Store(false)
		for _, st := range []ocpp201.UploadLogStatusEnumType{ocpp201.UploadLogStatusUploading, ocpp201.UploadLogStatusUploaded} {
			if _, err := ocpp201.LogStatusNotification(ctx, p, &ocpp201.LogStatusNotificationRequest{
				Status: st, RequestID: ocpp.Ptr(req.RequestID)}); err != nil {
				fmt.Printf("station: %v\n", err)
				return
			}
			time.Sleep(step)
		}
	})
	return &ocpp201.GetLogResponse{Status: ocpp201.LogStatusAccepted, Filename: ocpp.Ptr(name)}, nil
}

// Reset (B11, B12): OnIdle waits for the transaction to end; Immediate
// does not (a real station first ends its transactions).
func (s *station) Reset(ctx context.Context, p ocpp.Peer, req *ocpp201.ResetRequest) (*ocpp201.ResetResponse, error) {
	if req.Type == ocpp201.ResetOnIdle && s.txActive.Load() {
		s.resetPending.Store(true)
		fmt.Println("station: reset OnIdle scheduled after the transaction")
		return &ocpp201.ResetResponse{Status: ocpp201.ResetStatusScheduled}, nil
	}
	fmt.Printf("station: reset %s accepted\n", req.Type)
	later(ctx, step, s.requestReboot)
	return &ocpp201.ResetResponse{Status: ocpp201.ResetStatusAccepted}, nil
}

// transaction simulates a driver charging: it starts a transaction and
// returns a function that ends it.
func (s *station) transaction(ctx context.Context, c ocpp.Caller) (func() error, error) {
	const id = "RM-TX-1"
	s.txActive.Store(true)
	if _, err := ocpp201.TransactionEvent(ctx, c, &ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventStarted, Timestamp: time.Now().UTC(), TriggerReason: ocpp201.TriggerReasonAuthorized, SeqNo: 0,
		TransactionInfo: ocpp201.TransactionType{TransactionID: id, ChargingState: ocpp.Ptr(ocpp201.ChargingStateCharging)},
		EVSE:            &ocpp201.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)},
		IDToken:         &ocpp201.IDTokenType{IDToken: "04A1B2C3D4E5F6", Type: ocpp201.IDTokenISO14443},
	}); err != nil {
		return nil, err
	}
	return func() error {
		if _, err := ocpp201.TransactionEvent(ctx, c, &ocpp201.TransactionEventRequest{
			EventType: ocpp201.TransactionEventEnded, Timestamp: time.Now().UTC(), TriggerReason: ocpp201.TriggerReasonEVDeparted, SeqNo: 1,
			TransactionInfo: ocpp201.TransactionType{TransactionID: id, StoppedReason: ocpp.Ptr(ocpp201.ReasonEVDisconnected)},
		}); err != nil {
			return err
		}
		s.txActive.Store(false)
		if s.resetPending.CompareAndSwap(true, false) {
			fmt.Println("station: idle now, performing the scheduled reset")
			s.requestReboot()
		}
		return nil
	}, nil
}

// run is the station's life: connect, boot, work until asked to reboot,
// repeat. Each boot is a new client: a closed client stays closed, like a
// station process that restarts.
func (s *station) run(ctx context.Context, url string, current chan<- *client.Client) error {
	reason := ocpp201.BootReasonPowerUp
	for {
		c, err := client.New(url, client.WithVersions(ocpp.V201), client.WithInsecureTransport(),
			client.WithHandler(ocpp.V201, ocpp201.ChargingStation(s)),
			client.WithReconnect(client.DefaultReconnect()))
		if err != nil {
			return err
		}
		if err := c.Connect(ctx); err != nil {
			return err
		}
		fw, _ := s.firmware.Load().(string)
		if _, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{Reason: reason,
			ChargingStation: ocpp201.ChargingStationType{VendorName: "OTFabric", Model: "ManagedStation", FirmwareVersion: ocpp.Ptr(fw)}}); err != nil {
			return err
		}
		current <- c
		select {
		case <-s.reboot:
			fmt.Println("station: rebooting")
			_ = c.Close()
			time.Sleep(2 * step) // the reboot
			reason = ocpp201.BootReasonRemoteReset
		case <-ctx.Done():
			_ = c.Close()
			return nil
		}
	}
}

// ---------------------------------------------------------------------------
// The CSMS.

// csms prints what stations send and passes it on as events, so the
// operator script below can wait for them.
type csms struct {
	ocpp201.UnimplementedCSMSHandler
	events chan string
}

func (h csms) event(format string, args ...any) {
	e := fmt.Sprintf(format, args...)
	fmt.Println("CSMS: " + e)
	h.events <- e
}

func (h csms) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	fw := ""
	if req.ChargingStation.FirmwareVersion != nil {
		fw = *req.ChargingStation.FirmwareVersion
	}
	h.event("boot %s firmware %s", req.Reason, fw)
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (h csms) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.StatusNotificationRequest) (*ocpp201.StatusNotificationResponse, error) {
	h.event("status EVSE %d connector %d %s", req.EVSEID, req.ConnectorID, req.ConnectorStatus)
	return &ocpp201.StatusNotificationResponse{}, nil
}

func (h csms) MeterValues(ctx context.Context, p ocpp.Peer, req *ocpp201.MeterValuesRequest) (*ocpp201.MeterValuesResponse, error) {
	h.event("meter EVSE %d %.0f Wh", req.EVSEID, req.MeterValue[0].SampledValue[0].Value)
	return &ocpp201.MeterValuesResponse{}, nil
}

func (h csms) FirmwareStatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.FirmwareStatusNotificationRequest) (*ocpp201.FirmwareStatusNotificationResponse, error) {
	h.event("firmware %s", req.Status)
	return &ocpp201.FirmwareStatusNotificationResponse{}, nil
}

func (h csms) LogStatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.LogStatusNotificationRequest) (*ocpp201.LogStatusNotificationResponse, error) {
	h.event("log %s", req.Status)
	return &ocpp201.LogStatusNotificationResponse{}, nil
}

func (h csms) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp201.TransactionEventRequest) (*ocpp201.TransactionEventResponse, error) {
	h.event("transaction %s %s", req.TransactionInfo.TransactionID, req.EventType)
	return &ocpp201.TransactionEventResponse{}, nil
}

// waitEvent waits for an event that starts with prefix.
func waitEvent(ctx context.Context, events <-chan string, prefix string) error {
	for {
		select {
		case e := <-events:
			if strings.HasPrefix(e, prefix) {
				return nil
			}
		case <-ctx.Done():
			return fmt.Errorf("waiting for %q: %w", prefix, ctx.Err())
		}
	}
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := csms{events: make(chan string, 64)}
	var connects atomic.Int32
	srv, err := server.New(
		server.WithHandler(ocpp.V201, ocpp201.CSMS(h)),
		server.WithoutAuthentication(),
		server.WithSessionHandler(
			func(s *server.Session) { fmt.Printf("CSMS: %s connected (connection %d)\n", s.ID(), connects.Add(1)) },
			func(s *server.Session, err error) { fmt.Printf("CSMS: %s disconnected\n", s.ID()) },
		),
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

	st := &station{reboot: make(chan struct{}, 1)}
	st.firmware.Store("1.1.0")
	clients := make(chan *client.Client, 4)
	var wg sync.WaitGroup
	wg.Add(1)
	stationCtx, stopStation := context.WithCancel(ctx)
	go func() {
		defer wg.Done()
		if err := st.run(stationCtx, "ws://"+ln.Addr().String()+"/ocpp/"+stationID, clients); err != nil {
			fmt.Printf("station: %v\n", err)
		}
	}()
	defer func() { stopStation(); wg.Wait() }()

	// The operator's script.
	if err := waitEvent(ctx, h.events, "boot PowerUp"); err != nil {
		return err
	}
	c := <-clients
	session, ok := srv.Station(stationID)
	if !ok {
		return fmt.Errorf("no session")
	}

	fmt.Println("\n--- TriggerMessage ---")
	for _, t := range []struct {
		msg  ocpp201.MessageTriggerEnumType
		evse *ocpp201.EVSEType
		want string
	}{
		{ocpp201.MessageTriggerStatusNotification, &ocpp201.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)}, "status"},
		{ocpp201.MessageTriggerMeterValues, nil, "meter"},
		{ocpp201.MessageTriggerLogStatusNotification, nil, "log Idle"},
		{ocpp201.MessageTriggerSignV2GCertificate, nil, ""},
	} {
		resp, err := ocpp201.TriggerMessage(ctx, session, &ocpp201.TriggerMessageRequest{RequestedMessage: t.msg, EVSE: t.evse})
		if err != nil {
			return err
		}
		fmt.Printf("CSMS: TriggerMessage %s: %s\n", t.msg, resp.Status)
		if resp.Status == ocpp201.TriggerMessageStatusAccepted {
			if err := waitEvent(ctx, h.events, t.want); err != nil {
				return err
			}
		}
	}

	fmt.Println("\n--- UpdateFirmware ---")
	fw, err := ocpp201.UpdateFirmware(ctx, session, &ocpp201.UpdateFirmwareRequest{RequestID: 7, Firmware: ocpp201.FirmwareType{
		Location: "https://firmware.example.com/managed-station-1.2.0.bin", RetrieveDateTime: time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: UpdateFirmware: %s\n", fw.Status)
	if err := waitEvent(ctx, h.events, "firmware Installed"); err != nil {
		return err
	}

	fmt.Println("\n--- GetLog ---")
	gl, err := ocpp201.GetLog(ctx, session, &ocpp201.GetLogRequest{LogType: ocpp201.LogDiagnosticsLog, RequestID: 8,
		Log: ocpp201.LogParametersType{RemoteLocation: "https://logs.example.com/upload"}})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: GetLog: %s, file %s\n", gl.Status, *gl.Filename)
	if err := waitEvent(ctx, h.events, "log Uploaded"); err != nil {
		return err
	}

	fmt.Println("\n--- Reset OnIdle during a transaction ---")
	endTx, err := st.transaction(ctx, c) // a driver starts charging
	if err != nil {
		return err
	}
	reset, err := ocpp201.Reset(ctx, session, &ocpp201.ResetRequest{Type: ocpp201.ResetOnIdle})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: Reset OnIdle: %s\n", reset.Status)
	time.Sleep(2 * step)
	if err := endTx(); err != nil { // the driver leaves
		return err
	}
	if err := waitEvent(ctx, h.events, "boot RemoteReset"); err != nil {
		return err
	}
	<-clients
	if session, ok = srv.Station(stationID); !ok {
		return fmt.Errorf("no session after the reset")
	}

	fmt.Println("\n--- Reset Immediate ---")
	reset, err = ocpp201.Reset(ctx, session, &ocpp201.ResetRequest{Type: ocpp201.ResetImmediate})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: Reset Immediate: %s\n", reset.Status)
	if err := waitEvent(ctx, h.events, "boot RemoteReset"); err != nil {
		return err
	}
	<-clients
	fmt.Printf("\nCSMS: done; the station connected %d times\n", connects.Load())
	return nil
}
