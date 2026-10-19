// SPDX-License-Identifier: MIT

// Command smart-charging shows smart charging between a CSMS and a
// charging station in one process, on OCPP 2.0.1 (default) or 1.6
// (-version 1.6):
//
//   - the CSMS limits the whole station with a ChargingStationMaxProfile
//     (ChargePointMaxProfile in 1.6) in W, and sets a TxDefaultProfile in A
//     that lowers the current during an evening peak;
//   - a TxProfile without a transaction is rejected by the station;
//   - the CSMS asks for the composite schedule, which the station computes
//     from the profiles it stores;
//   - the CSMS clears the TxDefaultProfile and asks again.
//
// The station side keeps profiles in memory and computes a deliberately
// simple composite schedule (see compose). That is application logic, not
// library logic: go-ocpp carries and validates the messages; what a
// station does with a profile is the station's.
//
// For brevity it uses plain WebSocket on the loopback interface without
// authentication; see examples/security-profiles.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/server"
)

const (
	stationID = "SMART-0001"
	voltage   = 230.0 // V per phase, to convert between W and A
	phases    = 3
	hwMaxA    = 32.0 // what the hardware can do without any profile
)

// ---------------------------------------------------------------------------
// The station's profile store and composite schedule, independent of the
// OCPP version.

type period struct {
	start int     // seconds from the start of the schedule
	limit float64 // in the schedule's unit
}

type profile struct {
	id, stackLevel int
	evse           int    // 0: the whole station
	purpose        string // ChargingStationMaxProfile (1.6: ChargePointMaxProfile), TxDefaultProfile, TxProfile
	unit           string // "A" or "W"
	start          time.Time
	duration       int // seconds; 0: no end
	periods        []period
}

type store struct {
	mu       sync.Mutex
	profiles map[int]profile // by id: a profile with an existing id replaces it
}

func (s *store) set(p profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[p.id] = p
}

// clear removes the profiles matching every given criterion and says
// whether there were any.
func (s *store) clear(id *int, evse *int, purpose *string, stackLevel *int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for k, p := range s.profiles {
		if (id == nil || p.id == *id) && (evse == nil || p.evse == *evse) &&
			(purpose == nil || p.purpose == *purpose) && (stackLevel == nil || p.stackLevel == *stackLevel) {
			delete(s.profiles, k)
			found = true
		}
	}
	return found
}

func toUnit(v float64, from, to string) float64 {
	switch {
	case from == to:
		return v
	case to == "A":
		return v / (voltage * phases)
	default:
		return v * voltage * phases
	}
}

// limitAt returns the limit of p at t, in unit, if p has one then.
func (p profile) limitAt(t time.Time, unit string) (float64, bool) {
	off := int(t.Sub(p.start) / time.Second)
	if off < 0 || (p.duration > 0 && off >= p.duration) {
		return 0, false
	}
	limit, ok := 0.0, false
	for _, per := range p.periods {
		if per.start <= off {
			limit, ok = per.limit, true
		}
	}
	return toUnit(limit, p.unit, unit), ok
}

// compose computes the composite schedule of an EVSE for duration seconds
// from start, in unit. Deliberately simple: per purpose, the profile with
// the highest stack level that has a limit wins; the result is the lowest
// of the station maximum and the transaction limit (TxProfile, else
// TxDefaultProfile), and the hardware limit where no profile applies.
// A real station also handles recurring profiles, validity periods,
// phases, external limits and the minimum charging rate.
func (s *store) compose(evse int, start time.Time, duration int, unit string) []period {
	s.mu.Lock()
	defer s.mu.Unlock()
	var relevant []profile
	breaks := map[int]bool{0: true}
	for _, p := range s.profiles {
		if p.evse != 0 && p.evse != evse {
			continue
		}
		relevant = append(relevant, p)
		for _, per := range p.periods {
			if off := int(p.start.Sub(start)/time.Second) + per.start; off > 0 && off < duration {
				breaks[off] = true
			}
		}
		if p.duration > 0 {
			if end := int(p.start.Sub(start)/time.Second) + p.duration; end > 0 && end < duration {
				breaks[end] = true
			}
		}
	}
	offsets := make([]int, 0, len(breaks))
	for off := range breaks {
		offsets = append(offsets, off)
	}
	sort.Ints(offsets)

	var out []period
	for _, off := range offsets {
		t := start.Add(time.Duration(off) * time.Second)
		best := map[string]profile{} // per purpose, the highest stack level with a limit at t
		for _, p := range relevant {
			if _, ok := p.limitAt(t, unit); !ok {
				continue
			}
			if b, ok := best[p.purpose]; !ok || p.stackLevel > b.stackLevel {
				best[p.purpose] = p
			}
		}
		limit := toUnit(hwMaxA, "A", unit)
		apply := func(purpose string) bool {
			p, ok := best[purpose]
			if ok {
				l, _ := p.limitAt(t, unit)
				limit = math.Min(limit, l)
			}
			return ok
		}
		apply("ChargingStationMaxProfile")
		if !apply("TxProfile") {
			apply("TxDefaultProfile")
		}
		limit = math.Round(limit*10) / 10
		if len(out) == 0 || out[len(out)-1].limit != limit {
			out = append(out, period{start: off, limit: limit})
		}
	}
	return out
}

func printSchedule(who string, unit string, periods []period) {
	for _, p := range periods {
		fmt.Printf("%s:   from %5ds  %8.1f %s\n", who, p.start, p.limit, unit)
	}
}

// ---------------------------------------------------------------------------

func main() {
	version := flag.String("version", "2.0.1", "OCPP version: 2.0.1 or 1.6")
	flag.Parse()
	var err error
	switch *version {
	case "2.0.1":
		err = run201()
	case "1.6":
		err = run16()
	default:
		err = fmt.Errorf("unknown version %q", *version)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// connect starts a CSMS with csms and connects a station with station,
// both for version v, and returns the station's client and the CSMS's
// session.
func connect(ctx context.Context, v ocpp.Version, csms, station ocpp.Dispatcher) (*client.Client, *server.Session, func(), error) {
	// The server registers the session just after the station's Connect
	// returns: the session handler hands it over once it exists.
	sessions := make(chan *server.Session, 1)
	srv, err := server.New(server.WithHandler(v, csms), server.WithoutAuthentication(),
		server.WithSessionHandler(func(s *server.Session) { sessions <- s }, nil))
	if err != nil {
		return nil, nil, nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil, err
	}
	go func() { _ = srv.Serve(ln) }()
	c, err := client.New("ws://"+ln.Addr().String()+"/ocpp/"+stationID,
		client.WithVersions(v), client.WithHandler(v, station), client.WithInsecureTransport())
	if err != nil {
		return nil, nil, nil, err
	}
	if err := c.Connect(ctx); err != nil {
		return nil, nil, nil, err
	}
	cleanup := func() {
		_ = c.Close()
		_ = srv.Shutdown(ctx)
	}
	var session *server.Session
	select {
	case session = <-sessions:
	case <-ctx.Done():
		cleanup()
		return nil, nil, nil, fmt.Errorf("no session")
	}
	return c, session, cleanup, nil
}

// ---------------------------------------------------------------------------
// OCPP 2.0.1 (Part 2, functional block K: K01 SetChargingProfile, K08 Get
// Composite Schedule, K10 Clear Charging Profile).

type station201 struct {
	ocpp201.UnimplementedChargingStationHandler
	profiles *store
}

func (s station201) SetChargingProfile(ctx context.Context, p ocpp.Peer, req *ocpp201.SetChargingProfileRequest) (*ocpp201.SetChargingProfileResponse, error) {
	cp := req.ChargingProfile
	reject := func(reason string) (*ocpp201.SetChargingProfileResponse, error) {
		fmt.Printf("station: profile %d (%s) rejected: %s\n", cp.ID, cp.ChargingProfilePurpose, reason)
		return &ocpp201.SetChargingProfileResponse{Status: ocpp201.ChargingProfileStatusRejected,
			StatusInfo: &ocpp201.StatusInfoType{ReasonCode: reason}}, nil
	}
	switch {
	case cp.ChargingProfilePurpose == ocpp201.ChargingProfilePurposeTxProfile && cp.TransactionID == nil:
		return reject("TxNotFound") // a TxProfile belongs to a transaction
	case cp.ChargingProfileKind == ocpp201.ChargingProfileKindRecurring:
		return reject("UnsupportedKind") // this simple station does not do recurring profiles
	}
	sch := cp.ChargingSchedule[0] // a station may support up to three; this one uses the first
	start := time.Now()
	if sch.StartSchedule != nil {
		start = *sch.StartSchedule
	}
	periods := make([]period, len(sch.ChargingSchedulePeriod))
	for i, per := range sch.ChargingSchedulePeriod {
		periods[i] = period{start: per.StartPeriod, limit: per.Limit}
	}
	duration := 0
	if sch.Duration != nil {
		duration = *sch.Duration
	}
	s.profiles.set(profile{id: cp.ID, stackLevel: cp.StackLevel, evse: req.EVSEID, purpose: string(cp.ChargingProfilePurpose),
		unit: string(sch.ChargingRateUnit), start: start, duration: duration, periods: periods})
	fmt.Printf("station: stored profile %d (%s, stack level %d, EVSE %d, %d periods in %s)\n",
		cp.ID, cp.ChargingProfilePurpose, cp.StackLevel, req.EVSEID, len(periods), sch.ChargingRateUnit)
	return &ocpp201.SetChargingProfileResponse{Status: ocpp201.ChargingProfileStatusAccepted}, nil
}

func (s station201) GetCompositeSchedule(ctx context.Context, p ocpp.Peer, req *ocpp201.GetCompositeScheduleRequest) (*ocpp201.GetCompositeScheduleResponse, error) {
	unit := ocpp201.ChargingRateUnitA
	if req.ChargingRateUnit != nil {
		unit = *req.ChargingRateUnit
	}
	start := time.Now().UTC().Truncate(time.Second)
	periods := s.profiles.compose(req.EVSEID, start, req.Duration, string(unit))
	out := make([]ocpp201.ChargingSchedulePeriodType, len(periods))
	for i, per := range periods {
		out[i] = ocpp201.ChargingSchedulePeriodType{StartPeriod: per.start, Limit: per.limit}
	}
	return &ocpp201.GetCompositeScheduleResponse{Status: ocpp201.GenericStatusAccepted, Schedule: &ocpp201.CompositeScheduleType{
		EVSEID: req.EVSEID, Duration: req.Duration, ScheduleStart: start, ChargingRateUnit: unit, ChargingSchedulePeriod: out,
	}}, nil
}

func (s station201) ClearChargingProfile(ctx context.Context, p ocpp.Peer, req *ocpp201.ClearChargingProfileRequest) (*ocpp201.ClearChargingProfileResponse, error) {
	var evse, stackLevel *int
	var purpose *string
	if c := req.ChargingProfileCriteria; c != nil {
		evse, stackLevel = c.EVSEID, c.StackLevel
		if c.ChargingProfilePurpose != nil {
			purpose = ocpp.Ptr(string(*c.ChargingProfilePurpose))
		}
	}
	if s.profiles.clear(req.ChargingProfileID, evse, purpose, stackLevel) {
		return &ocpp201.ClearChargingProfileResponse{Status: ocpp201.ClearChargingProfileStatusAccepted}, nil
	}
	return &ocpp201.ClearChargingProfileResponse{Status: ocpp201.ClearChargingProfileStatusUnknown}, nil
}

type csms201 struct {
	ocpp201.UnimplementedCSMSHandler
}

func (csms201) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func run201() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, session, cleanup, err := connect(ctx, ocpp.V201, ocpp201.CSMS(csms201{}),
		ocpp201.ChargingStation(station201{profiles: &store{profiles: map[int]profile{}}}))
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{Reason: ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OTFabric", Model: "SmartStation"}}); err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Second)
	set := func(evse int, cp ocpp201.ChargingProfileType) error {
		resp, err := ocpp201.SetChargingProfile(ctx, session, &ocpp201.SetChargingProfileRequest{EVSEID: evse, ChargingProfile: cp})
		if err != nil {
			return err
		}
		fmt.Printf("CSMS: SetChargingProfile %d (%s): %s\n", cp.ID, cp.ChargingProfilePurpose, resp.Status)
		return nil
	}
	// The grid connection allows 15 kW for the whole station (EVSE 0).
	if err := set(0, ocpp201.ChargingProfileType{
		ID: 1, StackLevel: 0, ChargingProfilePurpose: ocpp201.ChargingProfilePurposeChargingStationMaxProfile,
		ChargingProfileKind: ocpp201.ChargingProfileKindAbsolute,
		ChargingSchedule: []ocpp201.ChargingScheduleType{{ID: 1, ChargingRateUnit: ocpp201.ChargingRateUnitW, StartSchedule: &now,
			ChargingSchedulePeriod: []ocpp201.ChargingSchedulePeriodType{{StartPeriod: 0, Limit: 15000}}}},
	}); err != nil {
		return err
	}
	// Transactions by default: 32 A, 10 A during the peak from 30 to 90
	// minutes from now, then 32 A again.
	if err := set(0, ocpp201.ChargingProfileType{
		ID: 2, StackLevel: 0, ChargingProfilePurpose: ocpp201.ChargingProfilePurposeTxDefaultProfile,
		ChargingProfileKind: ocpp201.ChargingProfileKindAbsolute,
		ChargingSchedule: []ocpp201.ChargingScheduleType{{ID: 2, ChargingRateUnit: ocpp201.ChargingRateUnitA, StartSchedule: &now, Duration: ocpp.Ptr(3 * 3600),
			ChargingSchedulePeriod: []ocpp201.ChargingSchedulePeriodType{
				{StartPeriod: 0, Limit: 32, NumberPhases: ocpp.Ptr(3)},
				{StartPeriod: 1800, Limit: 10, NumberPhases: ocpp.Ptr(3)},
				{StartPeriod: 5400, Limit: 32, NumberPhases: ocpp.Ptr(3)},
			}}},
	}); err != nil {
		return err
	}
	// A TxProfile must name a transaction: the station rejects this one.
	if err := set(1, ocpp201.ChargingProfileType{
		ID: 3, StackLevel: 1, ChargingProfilePurpose: ocpp201.ChargingProfilePurposeTxProfile,
		ChargingProfileKind: ocpp201.ChargingProfileKindRelative,
		ChargingSchedule: []ocpp201.ChargingScheduleType{{ID: 3, ChargingRateUnit: ocpp201.ChargingRateUnitA,
			ChargingSchedulePeriod: []ocpp201.ChargingSchedulePeriodType{{StartPeriod: 0, Limit: 6}}}},
	}); err != nil {
		return err
	}

	composite := func() error {
		resp, err := ocpp201.GetCompositeSchedule(ctx, session, &ocpp201.GetCompositeScheduleRequest{
			EVSEID: 1, Duration: 2 * 3600, ChargingRateUnit: ocpp.Ptr(ocpp201.ChargingRateUnitA)})
		if err != nil {
			return err
		}
		fmt.Printf("CSMS: composite schedule for EVSE 1: %s\n", resp.Status)
		if s := resp.Schedule; s != nil {
			periods := make([]period, len(s.ChargingSchedulePeriod))
			for i, p := range s.ChargingSchedulePeriod {
				periods[i] = period{start: p.StartPeriod, limit: p.Limit}
			}
			printSchedule("CSMS", string(s.ChargingRateUnit), periods)
		}
		return nil
	}
	if err := composite(); err != nil {
		return err
	}

	clr, err := ocpp201.ClearChargingProfile(ctx, session, &ocpp201.ClearChargingProfileRequest{
		ChargingProfileCriteria: &ocpp201.ClearChargingProfileType{ChargingProfilePurpose: ocpp.Ptr(ocpp201.ChargingProfilePurposeTxDefaultProfile)}})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: ClearChargingProfile TxDefaultProfile: %s\n", clr.Status)
	if err := composite(); err != nil {
		return err
	}
	clr, err = ocpp201.ClearChargingProfile(ctx, session, &ocpp201.ClearChargingProfileRequest{ChargingProfileID: ocpp.Ptr(99)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: ClearChargingProfile 99: %s\n", clr.Status)
	return nil
}

// ---------------------------------------------------------------------------
// OCPP 1.6 (§3.13 Smart Charging; §5.16 SetChargingProfile, §5.7
// GetCompositeSchedule, §5.5 ClearChargingProfile): the same flow, with
// connectors instead of EVSEs and one schedule per profile.

type chargePoint16 struct {
	ocpp16.UnimplementedChargePointHandler
	profiles *store
}

func purpose16(p ocpp16.ChargingProfilePurposeType) string {
	if p == ocpp16.ChargingProfilePurposeChargePointMaxProfile {
		return "ChargingStationMaxProfile" // the store uses the 2.0.1 name
	}
	return string(p)
}

func (s chargePoint16) SetChargingProfile(ctx context.Context, p ocpp.Peer, req *ocpp16.SetChargingProfileRequest) (*ocpp16.SetChargingProfileResponse, error) {
	cp := req.CsChargingProfiles
	if (cp.ChargingProfilePurpose == ocpp16.ChargingProfilePurposeTxProfile && cp.TransactionID == nil) ||
		cp.ChargingProfileKind == ocpp16.ChargingProfileKindRecurring {
		fmt.Printf("station: profile %d (%s) rejected\n", cp.ChargingProfileID, cp.ChargingProfilePurpose)
		return &ocpp16.SetChargingProfileResponse{Status: ocpp16.ChargingProfileStatusRejected}, nil
	}
	sch := cp.ChargingSchedule
	start := time.Now()
	if sch.StartSchedule != nil {
		start = *sch.StartSchedule
	}
	periods := make([]period, len(sch.ChargingSchedulePeriod))
	for i, per := range sch.ChargingSchedulePeriod {
		periods[i] = period{start: per.StartPeriod, limit: per.Limit}
	}
	duration := 0
	if sch.Duration != nil {
		duration = *sch.Duration
	}
	s.profiles.set(profile{id: cp.ChargingProfileID, stackLevel: cp.StackLevel, evse: req.ConnectorID, purpose: purpose16(cp.ChargingProfilePurpose),
		unit: string(sch.ChargingRateUnit), start: start, duration: duration, periods: periods})
	fmt.Printf("station: stored profile %d (%s, stack level %d, connector %d, %d periods in %s)\n",
		cp.ChargingProfileID, cp.ChargingProfilePurpose, cp.StackLevel, req.ConnectorID, len(periods), sch.ChargingRateUnit)
	return &ocpp16.SetChargingProfileResponse{Status: ocpp16.ChargingProfileStatusAccepted}, nil
}

func (s chargePoint16) GetCompositeSchedule(ctx context.Context, p ocpp.Peer, req *ocpp16.GetCompositeScheduleRequest) (*ocpp16.GetCompositeScheduleResponse, error) {
	unit := ocpp16.ChargingRateUnitA
	if req.ChargingRateUnit != nil {
		unit = *req.ChargingRateUnit
	}
	start := time.Now().UTC().Truncate(time.Second)
	periods := s.profiles.compose(req.ConnectorID, start, req.Duration, string(unit))
	out := make([]ocpp16.ChargingSchedulePeriod, len(periods))
	for i, per := range periods {
		out[i] = ocpp16.ChargingSchedulePeriod{StartPeriod: per.start, Limit: per.limit}
	}
	return &ocpp16.GetCompositeScheduleResponse{Status: ocpp16.GetCompositeScheduleStatusAccepted,
		ConnectorID: ocpp.Ptr(req.ConnectorID), ScheduleStart: &start,
		ChargingSchedule: &ocpp16.ChargingSchedule{ChargingRateUnit: unit, Duration: ocpp.Ptr(req.Duration), StartSchedule: &start, ChargingSchedulePeriod: out},
	}, nil
}

func (s chargePoint16) ClearChargingProfile(ctx context.Context, p ocpp.Peer, req *ocpp16.ClearChargingProfileRequest) (*ocpp16.ClearChargingProfileResponse, error) {
	var purpose *string
	if req.ChargingProfilePurpose != nil {
		purpose = ocpp.Ptr(purpose16(*req.ChargingProfilePurpose))
	}
	if s.profiles.clear(req.ID, req.ConnectorID, purpose, req.StackLevel) {
		return &ocpp16.ClearChargingProfileResponse{Status: ocpp16.ClearChargingProfileStatusAccepted}, nil
	}
	return &ocpp16.ClearChargingProfileResponse{Status: ocpp16.ClearChargingProfileStatusUnknown}, nil
}

type centralSystem16 struct {
	ocpp16.UnimplementedCentralSystemHandler
}

func (centralSystem16) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.BootNotificationRequest) (*ocpp16.BootNotificationResponse, error) {
	return &ocpp16.BootNotificationResponse{Status: ocpp16.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func run16() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, session, cleanup, err := connect(ctx, ocpp.V16, ocpp16.CentralSystem(centralSystem16{}),
		ocpp16.ChargePoint(chargePoint16{profiles: &store{profiles: map[int]profile{}}}))
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{ChargePointVendor: "OTFabric", ChargePointModel: "SmartStation"}); err != nil {
		return err
	}

	now := time.Now().UTC().Truncate(time.Second)
	set := func(connector int, cp ocpp16.ChargingProfile) error {
		resp, err := ocpp16.SetChargingProfile(ctx, session, &ocpp16.SetChargingProfileRequest{ConnectorID: connector, CsChargingProfiles: cp})
		if err != nil {
			return err
		}
		fmt.Printf("CSMS: SetChargingProfile %d (%s): %s\n", cp.ChargingProfileID, cp.ChargingProfilePurpose, resp.Status)
		return nil
	}
	if err := set(0, ocpp16.ChargingProfile{
		ChargingProfileID: 1, StackLevel: 0, ChargingProfilePurpose: ocpp16.ChargingProfilePurposeChargePointMaxProfile,
		ChargingProfileKind: ocpp16.ChargingProfileKindAbsolute,
		ChargingSchedule: ocpp16.ChargingSchedule{ChargingRateUnit: ocpp16.ChargingRateUnitW, StartSchedule: &now,
			ChargingSchedulePeriod: []ocpp16.ChargingSchedulePeriod{{StartPeriod: 0, Limit: 15000}}},
	}); err != nil {
		return err
	}
	if err := set(0, ocpp16.ChargingProfile{
		ChargingProfileID: 2, StackLevel: 0, ChargingProfilePurpose: ocpp16.ChargingProfilePurposeTxDefaultProfile,
		ChargingProfileKind: ocpp16.ChargingProfileKindAbsolute,
		ChargingSchedule: ocpp16.ChargingSchedule{ChargingRateUnit: ocpp16.ChargingRateUnitA, StartSchedule: &now, Duration: ocpp.Ptr(3 * 3600),
			ChargingSchedulePeriod: []ocpp16.ChargingSchedulePeriod{
				{StartPeriod: 0, Limit: 32, NumberPhases: ocpp.Ptr(3)},
				{StartPeriod: 1800, Limit: 10, NumberPhases: ocpp.Ptr(3)},
				{StartPeriod: 5400, Limit: 32, NumberPhases: ocpp.Ptr(3)},
			}},
	}); err != nil {
		return err
	}
	if err := set(1, ocpp16.ChargingProfile{
		ChargingProfileID: 3, StackLevel: 1, ChargingProfilePurpose: ocpp16.ChargingProfilePurposeTxProfile,
		ChargingProfileKind: ocpp16.ChargingProfileKindRelative,
		ChargingSchedule: ocpp16.ChargingSchedule{ChargingRateUnit: ocpp16.ChargingRateUnitA,
			ChargingSchedulePeriod: []ocpp16.ChargingSchedulePeriod{{StartPeriod: 0, Limit: 6}}},
	}); err != nil {
		return err
	}

	composite := func() error {
		resp, err := ocpp16.GetCompositeSchedule(ctx, session, &ocpp16.GetCompositeScheduleRequest{
			ConnectorID: 1, Duration: 2 * 3600, ChargingRateUnit: ocpp.Ptr(ocpp16.ChargingRateUnitA)})
		if err != nil {
			return err
		}
		fmt.Printf("CSMS: composite schedule for connector 1: %s\n", resp.Status)
		if s := resp.ChargingSchedule; s != nil {
			periods := make([]period, len(s.ChargingSchedulePeriod))
			for i, p := range s.ChargingSchedulePeriod {
				periods[i] = period{start: p.StartPeriod, limit: p.Limit}
			}
			printSchedule("CSMS", string(s.ChargingRateUnit), periods)
		}
		return nil
	}
	if err := composite(); err != nil {
		return err
	}
	clr, err := ocpp16.ClearChargingProfile(ctx, session, &ocpp16.ClearChargingProfileRequest{
		ChargingProfilePurpose: ocpp.Ptr(ocpp16.ChargingProfilePurposeTxDefaultProfile)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: ClearChargingProfile TxDefaultProfile: %s\n", clr.Status)
	if err := composite(); err != nil {
		return err
	}
	clr, err = ocpp16.ClearChargingProfile(ctx, session, &ocpp16.ClearChargingProfileRequest{ID: ocpp.Ptr(99)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: ClearChargingProfile 99: %s\n", clr.Status)
	return nil
}
