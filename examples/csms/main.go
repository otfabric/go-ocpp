// SPDX-License-Identifier: MIT

// Command csms is a small CSMS that serves OCPP 1.6, 2.0.1 and 2.1 on one
// endpoint. It authenticates stations with HTTP Basic authentication
// against a per-station password table, keeps a registry of stations and
// transactions in memory, authorizes id tokens against an allow-list, and
// starts and stops transactions remotely.
//
// By default it starts on 127.0.0.1 with a random port, connects one
// in-process station of each version, runs the flows, prints a summary and
// exits. With -listen it keeps running as a CSMS until interrupted, so real
// stations or examples/station-simulator can connect:
//
//	go run ./examples/csms -listen 127.0.0.1:8887
//	go run ./examples/station-simulator -url ws://127.0.0.1:8887/ocpp/CS201-0001 -password ...
//
// It uses security profile 1 (Basic authentication over plain WebSocket),
// which belongs on a trusted network only; see examples/security-profiles
// for profiles 2 and 3.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

// passwords is the per-station password table (security profiles 1 and 2).
// In production: random per station, stored hashed or in a secret store,
// and rotated with SetVariables / ChangeConfiguration.
var passwords = map[string]string{
	"CS16-0001":  "cs16-example-password-0001",
	"CS201-0001": "cs201-example-password-001",
	"CS21-0001":  "cs21-example-password-0001",
}

// allowList holds the id tokens this CSMS accepts.
var allowList = map[string]bool{"04A1B2C3D4E5F6": true, "04F0E1D2C3B4A5": true}

const (
	goodToken    = "04A1B2C3D4E5F6"
	unknownToken = "DEADBEEF"
	heartbeat    = 300 // seconds, sent in the BootNotification response
)

// ---------------------------------------------------------------------------
// Registry: the CSMS's state, in memory.

type stationRecord struct {
	ID         string
	Version    ocpp.Version
	Vendor     string
	Model      string
	Status     string // last connector status reported
	Connected  bool
	RemoteAddr string
	Booted     time.Time
}

type transaction struct {
	Station    string
	ID         string // 1.6: assigned by the CSMS; 2.x: by the station
	IDToken    string
	Remote     bool
	StartWh    float64
	LastWh     float64
	Started    time.Time
	Ended      time.Time
	Active     bool
	StopReason string
}

type registry struct {
	mu       sync.Mutex
	stations map[string]*stationRecord
	txs      []*transaction
	next16   int             // next OCPP 1.6 transaction id
	remote16 map[string]bool // OCPP 1.6 stations with a remote start pending
	changed  chan struct{}   // closed and replaced on every change
}

func newRegistry() *registry {
	return &registry{stations: map[string]*stationRecord{}, next16: 1, remote16: map[string]bool{}, changed: make(chan struct{})}
}

// update runs f under the lock and wakes everyone waiting for a change.
func (r *registry) update(f func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f()
	close(r.changed)
	r.changed = make(chan struct{})
}

// waitFor waits until cond, evaluated under the lock, holds.
func (r *registry) waitFor(ctx context.Context, cond func() bool) error {
	for {
		r.mu.Lock()
		ok, ch := cond(), r.changed
		r.mu.Unlock()
		if ok {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (r *registry) station(id string) *stationRecord {
	s, ok := r.stations[id]
	if !ok {
		s = &stationRecord{ID: id}
		r.stations[id] = s
	}
	return s
}

func (r *registry) connected(id string, v ocpp.Version, addr string) {
	r.update(func() {
		s := r.station(id)
		s.Version, s.Connected, s.RemoteAddr = v, true, addr
	})
}

func (r *registry) disconnected(id string) {
	r.update(func() { r.station(id).Connected = false })
}

func (r *registry) booted(id, vendor, model string) {
	r.update(func() {
		s := r.station(id)
		s.Vendor, s.Model, s.Booted = vendor, model, time.Now()
	})
}

func (r *registry) status(id, status string) {
	r.update(func() { r.station(id).Status = status })
}

// startTx records a transaction; id "" asks for a new OCPP 1.6 id.
func (r *registry) startTx(station, id, token string, wh float64, remote bool) string {
	r.update(func() {
		if id == "" {
			id = strconv.Itoa(r.next16)
			r.next16++
		}
		r.txs = append(r.txs, &transaction{Station: station, ID: id, IDToken: token, Remote: remote,
			StartWh: wh, LastWh: wh, Started: time.Now(), Active: true})
	})
	return id
}

func (r *registry) find(station, id string) *transaction {
	for _, t := range r.txs {
		if t.Station == station && t.ID == id {
			return t
		}
	}
	return nil
}

func (r *registry) meter(station, id string, wh float64) {
	r.update(func() {
		if t := r.find(station, id); t != nil {
			t.LastWh = wh
		}
	})
}

func (r *registry) stopTx(station, id string, wh float64, reason string) {
	r.update(func() {
		if t := r.find(station, id); t != nil {
			t.LastWh, t.Ended, t.Active, t.StopReason = wh, time.Now(), false, reason
		}
	})
}

// activeTx returns the id of the station's active transaction.
func (r *registry) activeTx(station string) (string, bool) {
	for _, t := range r.txs {
		if t.Station == station && t.Active {
			return t.ID, true
		}
	}
	return "", false
}

func (r *registry) print() {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.stations))
	for id := range r.stations {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Println("\nStations:")
	for _, id := range ids {
		s := r.stations[id]
		fmt.Printf("  %-11s OCPP %-5s %s %s, connector %s, connected=%v\n", s.ID, s.Version, s.Vendor, s.Model, s.Status, s.Connected)
	}
	fmt.Println("Transactions:")
	for _, t := range r.txs {
		fmt.Printf("  %-11s tx %-16s token %s remote=%-5v %6.0f Wh active=%v %s\n",
			t.Station, t.ID, t.IDToken, t.Remote, t.LastWh-t.StartWh, t.Active, t.StopReason)
	}
}

// ---------------------------------------------------------------------------
// OCPP 1.6 Central System handler.

type centralSystem16 struct {
	ocpp16.UnimplementedCentralSystemHandler
	reg *registry
}

func (h centralSystem16) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.BootNotificationRequest) (*ocpp16.BootNotificationResponse, error) {
	h.reg.booted(p.ID(), req.ChargePointVendor, req.ChargePointModel)
	fmt.Printf("CSMS: %s booted on OCPP 1.6: %s %s\n", p.ID(), req.ChargePointVendor, req.ChargePointModel)
	return &ocpp16.BootNotificationResponse{Status: ocpp16.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: heartbeat}, nil
}

func (h centralSystem16) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp16.HeartbeatRequest) (*ocpp16.HeartbeatResponse, error) {
	return &ocpp16.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (h centralSystem16) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.StatusNotificationRequest) (*ocpp16.StatusNotificationResponse, error) {
	h.reg.status(p.ID(), string(req.Status))
	fmt.Printf("CSMS: %s connector %d is %s\n", p.ID(), req.ConnectorID, req.Status)
	return &ocpp16.StatusNotificationResponse{}, nil
}

func idTagInfo16(token string) ocpp16.IDTagInfo {
	if allowList[token] {
		return ocpp16.IDTagInfo{Status: ocpp16.AuthorizationStatusAccepted}
	}
	return ocpp16.IDTagInfo{Status: ocpp16.AuthorizationStatusInvalid}
}

func (h centralSystem16) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp16.AuthorizeRequest) (*ocpp16.AuthorizeResponse, error) {
	info := idTagInfo16(req.IDTag)
	fmt.Printf("CSMS: %s authorize %s: %s\n", p.ID(), req.IDTag, info.Status)
	return &ocpp16.AuthorizeResponse{IDTagInfo: info}, nil
}

func (h centralSystem16) StartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.StartTransactionRequest) (*ocpp16.StartTransactionResponse, error) {
	info := idTagInfo16(req.IDTag)
	// The CSMS assigns the transaction id, also when it does not accept
	// the token: the station then stops the transaction.
	// 1.6 StartTransaction does not say what started it: the CSMS
	// remembers that it asked for a remote start.
	h.reg.mu.Lock()
	remote := h.reg.remote16[p.ID()]
	delete(h.reg.remote16, p.ID())
	h.reg.mu.Unlock()
	id := h.reg.startTx(p.ID(), "", req.IDTag, float64(req.MeterStart), remote)
	fmt.Printf("CSMS: %s started transaction %s on connector %d for %s (%s)\n", p.ID(), id, req.ConnectorID, req.IDTag, info.Status)
	n, _ := strconv.Atoi(id)
	return &ocpp16.StartTransactionResponse{IDTagInfo: info, TransactionID: n}, nil
}

func (h centralSystem16) MeterValues(ctx context.Context, p ocpp.Peer, req *ocpp16.MeterValuesRequest) (*ocpp16.MeterValuesResponse, error) {
	for _, mv := range req.MeterValue {
		for _, sv := range mv.SampledValue {
			if wh, ok := energy16(sv); ok && req.TransactionID != nil {
				h.reg.meter(p.ID(), strconv.Itoa(*req.TransactionID), wh)
				fmt.Printf("CSMS: %s transaction %d at %.0f Wh\n", p.ID(), *req.TransactionID, wh)
			}
		}
	}
	return &ocpp16.MeterValuesResponse{}, nil
}

func (h centralSystem16) StopTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.StopTransactionRequest) (*ocpp16.StopTransactionResponse, error) {
	reason := string(ocpp16.ReasonLocal) // the default when absent
	if req.Reason != nil {
		reason = string(*req.Reason)
	}
	h.reg.stopTx(p.ID(), strconv.Itoa(req.TransactionID), float64(req.MeterStop), reason)
	fmt.Printf("CSMS: %s stopped transaction %d at %d Wh (%s)\n", p.ID(), req.TransactionID, req.MeterStop, reason)
	return &ocpp16.StopTransactionResponse{}, nil
}

// energy16 returns an Energy.Active.Import.Register sample in Wh.
func energy16(sv ocpp16.SampledValue) (float64, bool) {
	if sv.Measurand != nil && *sv.Measurand != ocpp16.MeasurandEnergyActiveImportRegister {
		return 0, false
	}
	v, err := strconv.ParseFloat(sv.Value, 64)
	if err != nil {
		return 0, false
	}
	if sv.Unit != nil && *sv.Unit == ocpp16.UnitOfMeasureKWh {
		v *= 1000
	}
	return v, true
}

// ---------------------------------------------------------------------------
// OCPP 2.0.1 CSMS handler.

type csms201 struct {
	ocpp201.UnimplementedCSMSHandler
	reg *registry
}

func (h csms201) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	h.reg.booted(p.ID(), req.ChargingStation.VendorName, req.ChargingStation.Model)
	fmt.Printf("CSMS: %s booted on OCPP 2.0.1 (%s): %s %s\n", p.ID(), req.Reason, req.ChargingStation.VendorName, req.ChargingStation.Model)
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: heartbeat}, nil
}

func (h csms201) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp201.HeartbeatRequest) (*ocpp201.HeartbeatResponse, error) {
	return &ocpp201.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (h csms201) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.StatusNotificationRequest) (*ocpp201.StatusNotificationResponse, error) {
	h.reg.status(p.ID(), string(req.ConnectorStatus))
	fmt.Printf("CSMS: %s EVSE %d connector %d is %s\n", p.ID(), req.EVSEID, req.ConnectorID, req.ConnectorStatus)
	return &ocpp201.StatusNotificationResponse{}, nil
}

func idTokenInfo201(token string) ocpp201.IDTokenInfoType {
	if allowList[token] {
		return ocpp201.IDTokenInfoType{Status: ocpp201.AuthorizationStatusAccepted}
	}
	return ocpp201.IDTokenInfoType{Status: ocpp201.AuthorizationStatusUnknown}
}

func (h csms201) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp201.AuthorizeRequest) (*ocpp201.AuthorizeResponse, error) {
	info := idTokenInfo201(req.IDToken.IDToken)
	fmt.Printf("CSMS: %s authorize %s: %s\n", p.ID(), req.IDToken.IDToken, info.Status)
	return &ocpp201.AuthorizeResponse{IDTokenInfo: info}, nil
}

func (h csms201) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp201.TransactionEventRequest) (*ocpp201.TransactionEventResponse, error) {
	id, wh, hasWh := req.TransactionInfo.TransactionID, 0.0, false
	for _, mv := range req.MeterValue {
		for _, sv := range mv.SampledValue {
			var unit *string
			var mult *int
			if sv.UnitOfMeasure != nil {
				unit, mult = sv.UnitOfMeasure.Unit, sv.UnitOfMeasure.Multiplier
			}
			if v, ok := energy2x(sv.Measurand == nil || *sv.Measurand == ocpp201.MeasurandEnergyActiveImportRegister, sv.Value, unit, mult); ok {
				wh, hasWh = v, true
			}
		}
	}
	resp := &ocpp201.TransactionEventResponse{}
	if req.IDToken != nil { // a token in the event: answer with its status (E01, C01)
		info := idTokenInfo201(req.IDToken.IDToken)
		resp.IDTokenInfo = &info
	}
	h.reg.event2x(p.ID(), string(req.EventType), string(req.TriggerReason), id, tokenOf201(req.IDToken),
		req.TransactionInfo.RemoteStartID != nil, wh, hasWh, stoppedReason201(req.TransactionInfo.StoppedReason))
	return resp, nil
}

func tokenOf201(t *ocpp201.IDTokenType) string {
	if t == nil {
		return ""
	}
	return t.IDToken
}

func stoppedReason201(r *ocpp201.ReasonEnumType) string {
	if r == nil {
		return string(ocpp201.ReasonLocal)
	}
	return string(*r)
}

// event2x records a TransactionEvent of OCPP 2.0.1 or 2.1.
func (r *registry) event2x(station, eventType, trigger, id, token string, remote bool, wh float64, hasWh bool, reason string) {
	switch eventType {
	case "Started":
		r.startTx(station, id, token, wh, remote)
		fmt.Printf("CSMS: %s transaction %s started (%s) for %s\n", station, id, trigger, token)
	case "Updated":
		if hasWh {
			r.meter(station, id, wh)
			fmt.Printf("CSMS: %s transaction %s at %.0f Wh (%s)\n", station, id, wh, trigger)
		}
	case "Ended":
		r.mu.Lock()
		if !hasWh {
			if t := r.find(station, id); t != nil {
				wh = t.LastWh
			}
		}
		r.mu.Unlock()
		r.stopTx(station, id, wh, reason)
		fmt.Printf("CSMS: %s transaction %s ended at %.0f Wh (%s, %s)\n", station, id, wh, trigger, reason)
	}
}

// energy2x converts a 2.x energy sample to Wh: value × 10^multiplier, in
// Wh unless the unit is kWh.
func energy2x(isEnergy bool, value float64, unit *string, multiplier *int) (float64, bool) {
	if !isEnergy {
		return 0, false
	}
	if multiplier != nil {
		value *= math.Pow10(*multiplier)
	}
	if unit != nil && *unit == "kWh" {
		value *= 1000
	}
	return value, true
}

// ---------------------------------------------------------------------------
// OCPP 2.1 CSMS handler: the same flows with the ocpp21 types.

type csms21 struct {
	ocpp21.UnimplementedCSMSHandler
	reg *registry
}

func (h csms21) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.BootNotificationRequest) (*ocpp21.BootNotificationResponse, error) {
	h.reg.booted(p.ID(), req.ChargingStation.VendorName, req.ChargingStation.Model)
	fmt.Printf("CSMS: %s booted on OCPP 2.1 (%s): %s %s\n", p.ID(), req.Reason, req.ChargingStation.VendorName, req.ChargingStation.Model)
	return &ocpp21.BootNotificationResponse{Status: ocpp21.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: heartbeat}, nil
}

func (h csms21) Heartbeat(ctx context.Context, p ocpp.Peer, req *ocpp21.HeartbeatRequest) (*ocpp21.HeartbeatResponse, error) {
	return &ocpp21.HeartbeatResponse{CurrentTime: time.Now().UTC()}, nil
}

func (h csms21) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.StatusNotificationRequest) (*ocpp21.StatusNotificationResponse, error) {
	h.reg.status(p.ID(), string(req.ConnectorStatus))
	fmt.Printf("CSMS: %s EVSE %d connector %d is %s\n", p.ID(), req.EVSEID, req.ConnectorID, req.ConnectorStatus)
	return &ocpp21.StatusNotificationResponse{}, nil
}

func idTokenInfo21(token string) ocpp21.IDTokenInfoType {
	if allowList[token] {
		return ocpp21.IDTokenInfoType{Status: ocpp21.AuthorizationStatusAccepted}
	}
	return ocpp21.IDTokenInfoType{Status: ocpp21.AuthorizationStatusUnknown}
}

func (h csms21) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp21.AuthorizeRequest) (*ocpp21.AuthorizeResponse, error) {
	info := idTokenInfo21(req.IDToken.IDToken)
	fmt.Printf("CSMS: %s authorize %s: %s\n", p.ID(), req.IDToken.IDToken, info.Status)
	return &ocpp21.AuthorizeResponse{IDTokenInfo: info}, nil
}

func (h csms21) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp21.TransactionEventRequest) (*ocpp21.TransactionEventResponse, error) {
	id, wh, hasWh := req.TransactionInfo.TransactionID, 0.0, false
	for _, mv := range req.MeterValue {
		for _, sv := range mv.SampledValue {
			var unit *string
			var mult *int
			if sv.UnitOfMeasure != nil {
				unit, mult = sv.UnitOfMeasure.Unit, sv.UnitOfMeasure.Multiplier
			}
			if v, ok := energy2x(sv.Measurand == nil || *sv.Measurand == ocpp21.MeasurandEnergyActiveImportRegister, sv.Value, unit, mult); ok {
				wh, hasWh = v, true
			}
		}
	}
	resp := &ocpp21.TransactionEventResponse{}
	token := ""
	if req.IDToken != nil {
		token = req.IDToken.IDToken
		info := idTokenInfo21(token)
		resp.IDTokenInfo = &info
	}
	reason := string(ocpp21.ReasonLocal)
	if req.TransactionInfo.StoppedReason != nil {
		reason = string(*req.TransactionInfo.StoppedReason)
	}
	h.reg.event2x(p.ID(), string(req.EventType), string(req.TriggerReason), id, token,
		req.TransactionInfo.RemoteStartID != nil, wh, hasWh, reason)
	return resp, nil
}

// ---------------------------------------------------------------------------
// Remote start and stop, from the CSMS, for any version.

func remoteStart(ctx context.Context, reg *registry, s *server.Session, token string, remoteStartID int) error {
	var status string
	switch s.Version() {
	case ocpp.V16:
		reg.update(func() { reg.remote16[s.ID()] = true })
		resp, err := ocpp16.RemoteStartTransaction(ctx, s, &ocpp16.RemoteStartTransactionRequest{IDTag: token, ConnectorID: ocpp.Ptr(1)})
		if err != nil {
			return err
		}
		status = string(resp.Status)
	case ocpp.V201:
		resp, err := ocpp201.RequestStartTransaction(ctx, s, &ocpp201.RequestStartTransactionRequest{
			RemoteStartID: remoteStartID, EVSEID: ocpp.Ptr(1),
			IDToken: ocpp201.IDTokenType{IDToken: token, Type: ocpp201.IDTokenISO14443},
		})
		if err != nil {
			return err
		}
		status = string(resp.Status)
	case ocpp.V21:
		resp, err := ocpp21.RequestStartTransaction(ctx, s, &ocpp21.RequestStartTransactionRequest{
			RemoteStartID: remoteStartID, EVSEID: ocpp.Ptr(1),
			IDToken: ocpp21.IDTokenType{IDToken: token, Type: "ISO14443"}, // a string in 2.1, no enumeration
		})
		if err != nil {
			return err
		}
		status = string(resp.Status)
	}
	fmt.Printf("CSMS: remote start on %s: %s\n", s.ID(), status)
	return nil
}

func remoteStop(ctx context.Context, s *server.Session, txID string) error {
	var status string
	switch s.Version() {
	case ocpp.V16:
		n, err := strconv.Atoi(txID)
		if err != nil {
			return err
		}
		resp, err := ocpp16.RemoteStopTransaction(ctx, s, &ocpp16.RemoteStopTransactionRequest{TransactionID: n})
		if err != nil {
			return err
		}
		status = string(resp.Status)
	case ocpp.V201:
		resp, err := ocpp201.RequestStopTransaction(ctx, s, &ocpp201.RequestStopTransactionRequest{TransactionID: txID})
		if err != nil {
			return err
		}
		status = string(resp.Status)
	case ocpp.V21:
		resp, err := ocpp21.RequestStopTransaction(ctx, s, &ocpp21.RequestStopTransactionRequest{TransactionID: txID})
		if err != nil {
			return err
		}
		status = string(resp.Status)
	}
	fmt.Printf("CSMS: remote stop of transaction %s on %s: %s\n", txID, s.ID(), status)
	return nil
}

// ---------------------------------------------------------------------------

func main() {
	listen := flag.String("listen", "", "keep running as a CSMS on this address (e.g. 127.0.0.1:8887) until interrupted")
	devPassword := flag.String("password", "", "with -listen: also accept any other station identity with this password (development only)")
	flag.Parse()
	if err := run(*listen, *devPassword); err != nil {
		log.Fatal(err)
	}
}

func run(listen, devPassword string) error {
	reg := newRegistry()
	srv, err := server.New(
		// One server, three versions: the station's Sec-WebSocket-Protocol
		// picks the dispatcher.
		server.WithHandler(ocpp.V16, ocpp16.CentralSystem(centralSystem16{reg: reg})),
		server.WithHandler(ocpp.V201, ocpp201.CSMS(csms201{reg: reg})),
		server.WithHandler(ocpp.V21, ocpp21.CSMS(csms21{reg: reg})),
		server.WithAuthenticator(server.BasicAuth(func(ctx context.Context, id string) (string, bool) {
			if pw, ok := passwords[id]; ok {
				return pw, true
			}
			return devPassword, devPassword != "" // unknown identities: HTTP 404 unless -password is set
		})),
		server.WithSessionHandler(
			func(s *server.Session) {
				reg.connected(s.ID(), s.Version(), s.RemoteAddr())
				fmt.Printf("CSMS: %s connected on OCPP %s from %s\n", s.ID(), s.Version(), s.RemoteAddr())
			},
			func(s *server.Session, err error) {
				reg.disconnected(s.ID())
				fmt.Printf("CSMS: %s disconnected (%v)\n", s.ID(), err)
			},
		),
		server.WithPingInterval(30*time.Second), // detect dead connections
	)
	if err != nil {
		return err
	}

	addr := listen
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	go func() { _ = srv.Serve(ln) }()
	base := "ws://" + ln.Addr().String() + "/ocpp/"
	fmt.Printf("CSMS: listening on %s<station identity> (OCPP 1.6, 2.0.1, 2.1; Basic authentication)\n", base)

	if listen != "" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
		fmt.Println("CSMS: shutting down")
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
		reg.print()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	defer func() { _ = srv.Shutdown(ctx) }()

	for i, d := range []struct {
		id      string
		version ocpp.Version
	}{{"CS16-0001", ocpp.V16}, {"CS201-0001", ocpp.V201}, {"CS21-0001", ocpp.V21}} {
		fmt.Printf("\n--- OCPP %s station %s ---\n", d.version, d.id)
		if err := demo(ctx, srv, reg, base, d.id, d.version, 100+i); err != nil {
			return fmt.Errorf("%s: %w", d.id, err)
		}
	}
	reg.print()
	return nil
}

// demo connects one in-process station, lets it run a local transaction,
// then starts and stops a second one remotely from the CSMS.
func demo(ctx context.Context, srv *server.Server, reg *registry, base, id string, v ocpp.Version, remoteStartID int) error {
	st := &demoStation{cmds: make(chan command, 2)}
	opts := []client.Option{
		client.WithVersions(v),
		client.WithBasicAuth(id, passwords[id]),
		client.WithInsecureTransport(), // ws:// with Basic auth is profile 1: loopback or a trusted network only
	}
	switch v {
	case ocpp.V16:
		opts = append(opts, client.WithHandler(v, ocpp16.ChargePoint(station16{st: st})))
	case ocpp.V201:
		opts = append(opts, client.WithHandler(v, ocpp201.ChargingStation(station201{st: st})))
	case ocpp.V21:
		opts = append(opts, client.WithHandler(v, ocpp21.ChargingStation(station21{st: st})))
	}
	c, err := client.New(base+id, opts...)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	switch v {
	case ocpp.V16:
		st.ops = &ops16{c: c}
	case ocpp.V201:
		st.ops = &ops201{c: c}
	case ocpp.V21:
		st.ops = &ops21{c: c}
	}

	errc := make(chan error, 1)
	ready := make(chan struct{})
	go func() { errc <- st.run(ctx, ready) }()

	// The CSMS side: once the station is idle again, start a transaction
	// remotely, wait for it, and stop it.
	select {
	case <-ready:
	case err := <-errc:
		return err
	}
	session, ok := srv.Station(id)
	if !ok {
		return errors.New("not connected")
	}
	if err := remoteStart(ctx, reg, session, goodToken, remoteStartID); err != nil {
		return err
	}
	var txID string
	if err := reg.waitFor(ctx, func() bool { txID, ok = reg.activeTx(id); return ok }); err != nil {
		return err
	}
	if err := remoteStop(ctx, session, txID); err != nil {
		return err
	}
	if err := <-errc; err != nil {
		return err
	}
	return reg.waitFor(ctx, func() bool { _, active := reg.activeTx(id); return !active })
}

// ---------------------------------------------------------------------------
// The in-process stations: just enough of a station to exercise the CSMS.
// examples/station-simulator is a fuller one.

type command struct {
	start         bool
	token         string
	remoteStartID *int
}

// stationOps are the station's messages, per version.
type stationOps interface {
	boot(ctx context.Context) error
	available(ctx context.Context) error
	authorize(ctx context.Context, token string) (bool, error)
	start(ctx context.Context, token string, remoteStartID *int, wh float64) (string, error)
	meter(ctx context.Context, txID string, wh float64) error
	stop(ctx context.Context, txID string, wh float64, remote bool) error
}

type demoStation struct {
	ops  stationOps
	cmds chan command // from the CSMS's remote start and stop
}

func (s *demoStation) run(ctx context.Context, ready chan<- struct{}) error {
	if err := s.ops.boot(ctx); err != nil {
		return err
	}
	if err := s.ops.available(ctx); err != nil {
		return err
	}
	if ok, err := s.ops.authorize(ctx, unknownToken); err != nil || ok {
		return fmt.Errorf("unknown token: accepted=%v, %v", ok, err)
	}
	if ok, err := s.ops.authorize(ctx, goodToken); err != nil || !ok {
		return fmt.Errorf("token: accepted=%v, %v", ok, err)
	}
	// A local transaction: the driver presented a card.
	wh := 10000.0
	tx, err := s.ops.start(ctx, goodToken, nil, wh)
	if err != nil {
		return err
	}
	for range 2 {
		wh += 1600
		if err := s.ops.meter(ctx, tx, wh); err != nil {
			return err
		}
	}
	if err := s.ops.stop(ctx, tx, wh, false); err != nil {
		return err
	}
	close(ready)

	// A remote transaction: wait for the CSMS.
	for {
		var cmd command
		select {
		case cmd = <-s.cmds:
		case <-ctx.Done():
			return ctx.Err()
		}
		if cmd.start {
			if tx, err = s.ops.start(ctx, cmd.token, cmd.remoteStartID, wh); err != nil {
				return err
			}
			wh += 2400
			if err := s.ops.meter(ctx, tx, wh); err != nil {
				return err
			}
			continue
		}
		return s.ops.stop(ctx, tx, wh, true)
	}
}

// post queues a command without blocking the handler.
func (s *demoStation) post(c command) bool {
	select {
	case s.cmds <- c:
		return true
	default:
		return false
	}
}

type station16 struct {
	ocpp16.UnimplementedChargePointHandler
	st *demoStation
}

func (h station16) RemoteStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.RemoteStartTransactionRequest) (*ocpp16.RemoteStartTransactionResponse, error) {
	if h.st.post(command{start: true, token: req.IDTag}) {
		return &ocpp16.RemoteStartTransactionResponse{Status: ocpp16.RemoteStartStopStatusAccepted}, nil
	}
	return &ocpp16.RemoteStartTransactionResponse{Status: ocpp16.RemoteStartStopStatusRejected}, nil
}

func (h station16) RemoteStopTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.RemoteStopTransactionRequest) (*ocpp16.RemoteStopTransactionResponse, error) {
	if h.st.post(command{}) {
		return &ocpp16.RemoteStopTransactionResponse{Status: ocpp16.RemoteStartStopStatusAccepted}, nil
	}
	return &ocpp16.RemoteStopTransactionResponse{Status: ocpp16.RemoteStartStopStatusRejected}, nil
}

type station201 struct {
	ocpp201.UnimplementedChargingStationHandler
	st *demoStation
}

func (h station201) RequestStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp201.RequestStartTransactionRequest) (*ocpp201.RequestStartTransactionResponse, error) {
	if h.st.post(command{start: true, token: req.IDToken.IDToken, remoteStartID: ocpp.Ptr(req.RemoteStartID)}) {
		return &ocpp201.RequestStartTransactionResponse{Status: ocpp201.RequestStartStopStatusAccepted}, nil
	}
	return &ocpp201.RequestStartTransactionResponse{Status: ocpp201.RequestStartStopStatusRejected}, nil
}

func (h station201) RequestStopTransaction(ctx context.Context, p ocpp.Peer, req *ocpp201.RequestStopTransactionRequest) (*ocpp201.RequestStopTransactionResponse, error) {
	if h.st.post(command{}) {
		return &ocpp201.RequestStopTransactionResponse{Status: ocpp201.RequestStartStopStatusAccepted}, nil
	}
	return &ocpp201.RequestStopTransactionResponse{Status: ocpp201.RequestStartStopStatusRejected}, nil
}

type station21 struct {
	ocpp21.UnimplementedChargingStationHandler
	st *demoStation
}

func (h station21) RequestStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp21.RequestStartTransactionRequest) (*ocpp21.RequestStartTransactionResponse, error) {
	if h.st.post(command{start: true, token: req.IDToken.IDToken, remoteStartID: ocpp.Ptr(req.RemoteStartID)}) {
		return &ocpp21.RequestStartTransactionResponse{Status: ocpp21.RequestStartStopStatusAccepted}, nil
	}
	return &ocpp21.RequestStartTransactionResponse{Status: ocpp21.RequestStartStopStatusRejected}, nil
}

func (h station21) RequestStopTransaction(ctx context.Context, p ocpp.Peer, req *ocpp21.RequestStopTransactionRequest) (*ocpp21.RequestStopTransactionResponse, error) {
	if h.st.post(command{}) {
		return &ocpp21.RequestStopTransactionResponse{Status: ocpp21.RequestStartStopStatusAccepted}, nil
	}
	return &ocpp21.RequestStopTransactionResponse{Status: ocpp21.RequestStartStopStatusRejected}, nil
}

// ops16 sends the OCPP 1.6 messages.
type ops16 struct{ c *client.Client }

func (o *ops16) boot(ctx context.Context) error {
	_, err := ocpp16.BootNotification(ctx, o.c, &ocpp16.BootNotificationRequest{ChargePointVendor: "OT Fabric", ChargePointModel: "Demo16"})
	return err
}

func (o *ops16) status(ctx context.Context, s ocpp16.ChargePointStatus) error {
	_, err := ocpp16.StatusNotification(ctx, o.c, &ocpp16.StatusNotificationRequest{ConnectorID: 1, ErrorCode: ocpp16.ChargePointErrorCodeNoError, Status: s})
	return err
}

func (o *ops16) available(ctx context.Context) error {
	return o.status(ctx, ocpp16.ChargePointStatusAvailable)
}

func (o *ops16) authorize(ctx context.Context, token string) (bool, error) {
	resp, err := ocpp16.Authorize(ctx, o.c, &ocpp16.AuthorizeRequest{IDTag: token})
	if err != nil {
		return false, err
	}
	return resp.IDTagInfo.Status == ocpp16.AuthorizationStatusAccepted, nil
}

func (o *ops16) start(ctx context.Context, token string, _ *int, wh float64) (string, error) {
	if err := o.status(ctx, ocpp16.ChargePointStatusCharging); err != nil {
		return "", err
	}
	resp, err := ocpp16.StartTransaction(ctx, o.c, &ocpp16.StartTransactionRequest{ConnectorID: 1, IDTag: token, MeterStart: int(wh), Timestamp: time.Now().UTC()})
	if err != nil {
		return "", err
	}
	return strconv.Itoa(resp.TransactionID), nil
}

func (o *ops16) meter(ctx context.Context, txID string, wh float64) error {
	n, _ := strconv.Atoi(txID)
	_, err := ocpp16.MeterValues(ctx, o.c, &ocpp16.MeterValuesRequest{ConnectorID: 1, TransactionID: &n, MeterValue: []ocpp16.MeterValue{{
		Timestamp: time.Now().UTC(),
		SampledValue: []ocpp16.SampledValue{{Value: strconv.FormatFloat(wh, 'f', 0, 64),
			Measurand: ocpp.Ptr(ocpp16.MeasurandEnergyActiveImportRegister), Unit: ocpp.Ptr(ocpp16.UnitOfMeasureWh)}},
	}}})
	return err
}

func (o *ops16) stop(ctx context.Context, txID string, wh float64, remote bool) error {
	n, _ := strconv.Atoi(txID)
	reason := ocpp16.ReasonLocal
	if remote {
		reason = ocpp16.ReasonRemote
	}
	if _, err := ocpp16.StopTransaction(ctx, o.c, &ocpp16.StopTransactionRequest{TransactionID: n, Timestamp: time.Now().UTC(), MeterStop: int(wh), Reason: &reason}); err != nil {
		return err
	}
	return o.available(ctx)
}

// ops201 sends the OCPP 2.0.1 messages.
type ops201 struct {
	c     *client.Client
	seqNo int
	n     int
}

func (o *ops201) boot(ctx context.Context) error {
	_, err := ocpp201.BootNotification(ctx, o.c, &ocpp201.BootNotificationRequest{Reason: ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "Demo201"}})
	return err
}

func (o *ops201) status(ctx context.Context, s ocpp201.ConnectorStatusEnumType) error {
	_, err := ocpp201.StatusNotification(ctx, o.c, &ocpp201.StatusNotificationRequest{Timestamp: time.Now().UTC(), ConnectorStatus: s, EVSEID: 1, ConnectorID: 1})
	return err
}

func (o *ops201) available(ctx context.Context) error {
	return o.status(ctx, ocpp201.ConnectorStatusAvailable)
}

func (o *ops201) authorize(ctx context.Context, token string) (bool, error) {
	resp, err := ocpp201.Authorize(ctx, o.c, &ocpp201.AuthorizeRequest{IDToken: ocpp201.IDTokenType{IDToken: token, Type: ocpp201.IDTokenISO14443}})
	if err != nil {
		return false, err
	}
	return resp.IDTokenInfo.Status == ocpp201.AuthorizationStatusAccepted, nil
}

func meter201(wh float64, rc ocpp201.ReadingContextEnumType) []ocpp201.MeterValueType {
	return []ocpp201.MeterValueType{{Timestamp: time.Now().UTC(), SampledValue: []ocpp201.SampledValueType{{
		Value: wh, Context: &rc, Measurand: ocpp.Ptr(ocpp201.MeasurandEnergyActiveImportRegister),
		UnitOfMeasure: &ocpp201.UnitOfMeasureType{Unit: ocpp.Ptr("Wh")},
	}}}}
}

func (o *ops201) event(ctx context.Context, req ocpp201.TransactionEventRequest) (*ocpp201.TransactionEventResponse, error) {
	req.Timestamp, req.SeqNo = time.Now().UTC(), o.seqNo
	o.seqNo++
	return ocpp201.TransactionEvent(ctx, o.c, &req)
}

func (o *ops201) start(ctx context.Context, token string, remoteStartID *int, wh float64) (string, error) {
	if err := o.status(ctx, ocpp201.ConnectorStatusOccupied); err != nil {
		return "", err
	}
	o.n++
	id := fmt.Sprintf("%s-TX%d", o.c.ID(), o.n) // the station chooses the id in 2.x
	trigger := ocpp201.TriggerReasonAuthorized
	if remoteStartID != nil {
		trigger = ocpp201.TriggerReasonRemoteStart
	}
	_, err := o.event(ctx, ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventStarted, TriggerReason: trigger,
		TransactionInfo: ocpp201.TransactionType{TransactionID: id, ChargingState: ocpp.Ptr(ocpp201.ChargingStateCharging), RemoteStartID: remoteStartID},
		EVSE:            &ocpp201.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)},
		IDToken:         &ocpp201.IDTokenType{IDToken: token, Type: ocpp201.IDTokenISO14443},
		MeterValue:      meter201(wh, ocpp201.ReadingContextTransactionBegin),
	})
	return id, err
}

func (o *ops201) meter(ctx context.Context, txID string, wh float64) error {
	// In 2.x, transaction meter values go in TransactionEvent(Updated).
	_, err := o.event(ctx, ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventUpdated, TriggerReason: ocpp201.TriggerReasonMeterValuePeriodic,
		TransactionInfo: ocpp201.TransactionType{TransactionID: txID},
		MeterValue:      meter201(wh, ocpp201.ReadingContextSamplePeriodic),
	})
	return err
}

func (o *ops201) stop(ctx context.Context, txID string, wh float64, remote bool) error {
	trigger, reason := ocpp201.TriggerReasonStopAuthorized, ocpp201.ReasonLocal
	if remote {
		trigger, reason = ocpp201.TriggerReasonRemoteStop, ocpp201.ReasonRemote
	}
	if _, err := o.event(ctx, ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventEnded, TriggerReason: trigger,
		TransactionInfo: ocpp201.TransactionType{TransactionID: txID, ChargingState: ocpp.Ptr(ocpp201.ChargingStateIdle), StoppedReason: &reason},
		MeterValue:      meter201(wh, ocpp201.ReadingContextTransactionEnd),
	}); err != nil {
		return err
	}
	return o.available(ctx)
}

// ops21 sends the OCPP 2.1 messages: the 2.0.1 flow with the ocpp21 types.
type ops21 struct {
	c     *client.Client
	seqNo int
	n     int
}

func (o *ops21) boot(ctx context.Context) error {
	_, err := ocpp21.BootNotification(ctx, o.c, &ocpp21.BootNotificationRequest{Reason: ocpp21.BootReasonPowerUp,
		ChargingStation: ocpp21.ChargingStationType{VendorName: "OT Fabric", Model: "Demo21"}})
	return err
}

func (o *ops21) status(ctx context.Context, s ocpp21.ConnectorStatusEnumType) error {
	_, err := ocpp21.StatusNotification(ctx, o.c, &ocpp21.StatusNotificationRequest{Timestamp: time.Now().UTC(), ConnectorStatus: s, EVSEID: 1, ConnectorID: 1})
	return err
}

func (o *ops21) available(ctx context.Context) error {
	return o.status(ctx, ocpp21.ConnectorStatusAvailable)
}

func (o *ops21) authorize(ctx context.Context, token string) (bool, error) {
	resp, err := ocpp21.Authorize(ctx, o.c, &ocpp21.AuthorizeRequest{IDToken: ocpp21.IDTokenType{IDToken: token, Type: "ISO14443"}})
	if err != nil {
		return false, err
	}
	return resp.IDTokenInfo.Status == ocpp21.AuthorizationStatusAccepted, nil
}

func meter21(wh float64, rc ocpp21.ReadingContextEnumType) []ocpp21.MeterValueType {
	return []ocpp21.MeterValueType{{Timestamp: time.Now().UTC(), SampledValue: []ocpp21.SampledValueType{{
		Value: wh, Context: &rc, Measurand: ocpp.Ptr(ocpp21.MeasurandEnergyActiveImportRegister),
		UnitOfMeasure: &ocpp21.UnitOfMeasureType{Unit: ocpp.Ptr("Wh")},
	}}}}
}

func (o *ops21) event(ctx context.Context, req ocpp21.TransactionEventRequest) (*ocpp21.TransactionEventResponse, error) {
	req.Timestamp, req.SeqNo = time.Now().UTC(), o.seqNo
	o.seqNo++
	return ocpp21.TransactionEvent(ctx, o.c, &req)
}

func (o *ops21) start(ctx context.Context, token string, remoteStartID *int, wh float64) (string, error) {
	if err := o.status(ctx, ocpp21.ConnectorStatusOccupied); err != nil {
		return "", err
	}
	o.n++
	id := fmt.Sprintf("%s-TX%d", o.c.ID(), o.n)
	trigger := ocpp21.TriggerReasonAuthorized
	if remoteStartID != nil {
		trigger = ocpp21.TriggerReasonRemoteStart
	}
	_, err := o.event(ctx, ocpp21.TransactionEventRequest{
		EventType: ocpp21.TransactionEventStarted, TriggerReason: trigger,
		TransactionInfo: ocpp21.TransactionType{TransactionID: id, ChargingState: ocpp.Ptr(ocpp21.ChargingStateCharging), RemoteStartID: remoteStartID},
		EVSE:            &ocpp21.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)},
		IDToken:         &ocpp21.IDTokenType{IDToken: token, Type: "ISO14443"},
		MeterValue:      meter21(wh, ocpp21.ReadingContextTransactionBegin),
	})
	return id, err
}

func (o *ops21) meter(ctx context.Context, txID string, wh float64) error {
	_, err := o.event(ctx, ocpp21.TransactionEventRequest{
		EventType: ocpp21.TransactionEventUpdated, TriggerReason: ocpp21.TriggerReasonMeterValuePeriodic,
		TransactionInfo: ocpp21.TransactionType{TransactionID: txID},
		MeterValue:      meter21(wh, ocpp21.ReadingContextSamplePeriodic),
	})
	return err
}

func (o *ops21) stop(ctx context.Context, txID string, wh float64, remote bool) error {
	trigger, reason := ocpp21.TriggerReasonStopAuthorized, ocpp21.ReasonLocal
	if remote {
		trigger, reason = ocpp21.TriggerReasonRemoteStop, ocpp21.ReasonRemote
	}
	if _, err := o.event(ctx, ocpp21.TransactionEventRequest{
		EventType: ocpp21.TransactionEventEnded, TriggerReason: trigger,
		TransactionInfo: ocpp21.TransactionType{TransactionID: txID, ChargingState: ocpp.Ptr(ocpp21.ChargingStateIdle), StoppedReason: &reason},
		MeterValue:      meter21(wh, ocpp21.ReadingContextTransactionEnd),
	}); err != nil {
		return err
	}
	return o.available(ctx)
}
