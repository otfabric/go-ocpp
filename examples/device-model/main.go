// SPDX-License-Identifier: MIT

// Command device-model shows device model management, OCPP 2.0.1 (default)
// or 2.1 (-version 2.1), with a CSMS and a charging station in one process:
//
//   - the CSMS asks for a base report (GetBaseReport); the station
//     answers Accepted and then sends its device model in NotifyReport
//     messages, split by its ItemsPerMessage with tbc ("to be continued");
//   - the CSMS reads variables (GetVariables), including unknown ones;
//   - the CSMS writes variables (SetVariables): a read-write variable is
//     accepted, a read-only one and an invalid value are rejected;
//   - the CSMS sets a variable monitor (SetVariableMonitoring).
//
// The station's device model is an in-memory table: which components and
// variables a station has, and what it does when one changes, is the
// station's own. go-ocpp carries and validates the messages.
//
// For brevity it uses plain WebSocket on the loopback interface without
// authentication; see examples/security-profiles.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

const stationID = "DM-0001"

// ---------------------------------------------------------------------------
// The station's device model, independent of the OCPP version.

type variable struct {
	component, componentInstance string
	evse                         int // 0: not on an EVSE
	name, instance               string
	value                        string
	readOnly                     bool
	dataType                     string // as DataEnumType: integer, decimal, boolean, string
	unit                         string
	maxLimit                     *float64
	monitorable                  bool
}

type deviceModel struct {
	mu        sync.Mutex
	vars      []*variable
	monitorID int
}

func newDeviceModel() *deviceModel {
	return &deviceModel{vars: []*variable{
		{component: "ChargingStation", name: "AvailabilityState", value: "Available", readOnly: true, dataType: "OptionList"},
		{component: "OCPPCommCtrlr", name: "HeartbeatInterval", value: "300", dataType: "integer", unit: "s"},
		{component: "OCPPCommCtrlr", name: "RetryBackOffWaitMinimum", value: "5", dataType: "integer", unit: "s"},
		{component: "OCPPCommCtrlr", name: "WebSocketPingInterval", value: "30", dataType: "integer", unit: "s"},
		{component: "SecurityCtrlr", name: "SecurityProfile", value: "1", readOnly: true, dataType: "integer"},
		{component: "SecurityCtrlr", name: "Identity", value: stationID, readOnly: true, dataType: "string"},
		{component: "AuthCtrlr", name: "AuthorizeRemoteStart", value: "true", dataType: "boolean"},
		{component: "DeviceDataCtrlr", name: "ItemsPerMessage", instance: "GetReport", value: "4", readOnly: true, dataType: "integer"},
		{component: "EVSE", evse: 1, name: "AvailabilityState", value: "Available", readOnly: true, dataType: "OptionList"},
		{component: "EVSE", evse: 1, name: "Power", value: "0", readOnly: true, dataType: "decimal", unit: "W", maxLimit: ocpp.Ptr(22000.0), monitorable: true},
	}}
}

// find returns the variable, or "UnknownComponent" / "UnknownVariable".
func (m *deviceModel) find(component string, evse int, name, instance string) (*variable, string) {
	knownComponent := false
	for _, v := range m.vars {
		if v.component != component || v.evse != evse {
			continue
		}
		knownComponent = true
		if v.name == name && v.instance == instance {
			return v, "Accepted"
		}
	}
	if knownComponent {
		return nil, "UnknownVariable"
	}
	return nil, "UnknownComponent"
}

func (m *deviceModel) get(component string, evse int, name, instance string) (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, status := m.find(component, evse, name, instance)
	if v == nil {
		return "", status
	}
	return v.value, status
}

// set returns the SetVariableStatus and, for Rejected, a reason code.
func (m *deviceModel) set(component string, evse int, name, instance, value string) (string, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, status := m.find(component, evse, name, instance)
	switch {
	case v == nil:
		return status, ""
	case v.readOnly:
		return "Rejected", "ReadOnly"
	}
	switch v.dataType {
	case "integer":
		if _, err := strconv.Atoi(value); err != nil {
			return "Rejected", "InvalidValue"
		}
	case "boolean":
		if value != "true" && value != "false" {
			return "Rejected", "InvalidValue"
		}
	}
	v.value = value
	return "Accepted", ""
}

// report returns the variables for a base report: everything, or the
// configurable ones.
func (m *deviceModel) report(configurationOnly bool) []variable {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []variable
	for _, v := range m.vars {
		if !configurationOnly || !v.readOnly {
			out = append(out, *v)
		}
	}
	return out
}

func (m *deviceModel) itemsPerMessage() int {
	v, _ := m.get("DeviceDataCtrlr", 0, "ItemsPerMessage", "GetReport")
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 10
	}
	return n
}

// monitor returns the SetMonitoringStatus and the new monitor's id.
func (m *deviceModel) monitor(component string, evse int, name string) (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, status := m.find(component, evse, name, "")
	switch {
	case v == nil:
		return status, 0
	case !v.monitorable:
		return "Rejected", 0
	}
	m.monitorID++
	return "Accepted", m.monitorID
}

// chunks splits a report into messages of at most n items.
func chunks(vars []variable, n int) [][]variable {
	var out [][]variable
	for len(vars) > n {
		out = append(out, vars[:n])
		vars = vars[n:]
	}
	return append(out, vars)
}

func evseOf(evse int) string {
	if evse == 0 {
		return ""
	}
	return fmt.Sprintf("[EVSE %d]", evse)
}

func printVar(who, component string, evse int, name, instance, value string, readOnly bool) {
	if instance != "" {
		name += "[" + instance + "]"
	}
	mode := "RW"
	if readOnly {
		mode = "RO"
	}
	fmt.Printf("%s:   %-30s %-27s %s = %s\n", who, component+evseOf(evse), name, mode, value)
}

// ---------------------------------------------------------------------------

func main() {
	version := flag.String("version", "2.0.1", "OCPP version: 2.0.1 or 2.1")
	flag.Parse()
	var err error
	switch *version {
	case "2.0.1":
		err = run201()
	case "2.1":
		err = run21()
	default:
		err = fmt.Errorf("unknown version %q", *version)
	}
	if err != nil {
		log.Fatal(err)
	}
}

// connect starts a CSMS and connects the station, both for version v.
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

// reports collects NotifyReport messages on the CSMS side, per request.
type reports struct {
	mu   sync.Mutex
	done map[int]chan struct{}
}

func (r *reports) expect(requestID int) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan struct{})
	r.done[requestID] = ch
	return ch
}

func (r *reports) finish(requestID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.done[requestID]; ok {
		close(ch)
		delete(r.done, requestID)
	}
}

func wait(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// OCPP 2.0.1 (Part 2: B05 Set Variables, B06 Get Variables, B07 Get Base
// Report; N04 Set Variable Monitoring).

type station201 struct {
	ocpp201.UnimplementedChargingStationHandler
	dm *deviceModel
}

func component201(c ocpp201.ComponentType) (string, int) {
	if c.EVSE != nil {
		return c.Name, c.EVSE.ID
	}
	return c.Name, 0
}

func instance201(v ocpp201.VariableType) string {
	if v.Instance == nil {
		return ""
	}
	return *v.Instance
}

func (s station201) GetBaseReport(ctx context.Context, p ocpp.Peer, req *ocpp201.GetBaseReportRequest) (*ocpp201.GetBaseReportResponse, error) {
	if req.ReportBase == ocpp201.ReportBaseSummaryInventory {
		return &ocpp201.GetBaseReportResponse{Status: ocpp201.GenericDeviceModelStatusNotSupported}, nil
	}
	vars := s.dm.report(req.ReportBase == ocpp201.ReportBaseConfigurationInventory)
	parts := chunks(vars, s.dm.itemsPerMessage())
	fmt.Printf("station: base report %d (%s): %d variables in %d messages\n", req.RequestID, req.ReportBase, len(vars), len(parts))
	// The NotifyReport messages go out after the GetBaseReport response.
	ocpp.AfterResponse(ctx, func() {
		generated := time.Now().UTC()
		for i, part := range parts {
			data := make([]ocpp201.ReportDataType, len(part))
			for j, v := range part {
				data[j] = reportData201(v)
			}
			if _, err := ocpp201.NotifyReport(ctx, p, &ocpp201.NotifyReportRequest{RequestID: req.RequestID, GeneratedAt: generated,
				SeqNo: i, Tbc: ocpp.Ptr(i < len(parts)-1), ReportData: data}); err != nil {
				fmt.Printf("station: NotifyReport: %v\n", err)
				return
			}
		}
	})
	return &ocpp201.GetBaseReportResponse{Status: ocpp201.GenericDeviceModelStatusAccepted}, nil
}

func reportData201(v variable) ocpp201.ReportDataType {
	c := ocpp201.ComponentType{Name: v.component}
	if v.evse != 0 {
		c.EVSE = &ocpp201.EVSEType{ID: v.evse}
	}
	if v.componentInstance != "" {
		c.Instance = ocpp.Ptr(v.componentInstance)
	}
	vt := ocpp201.VariableType{Name: v.name}
	if v.instance != "" {
		vt.Instance = ocpp.Ptr(v.instance)
	}
	mut := ocpp201.MutabilityReadWrite
	if v.readOnly {
		mut = ocpp201.MutabilityReadOnly
	}
	ch := &ocpp201.VariableCharacteristicsType{DataType: ocpp201.DataEnumType(v.dataType), SupportsMonitoring: v.monitorable, MaxLimit: v.maxLimit}
	if v.unit != "" {
		ch.Unit = ocpp.Ptr(v.unit)
	}
	if v.dataType == "OptionList" {
		ch.ValuesList = ocpp.Ptr("Available,Occupied,Reserved,Unavailable,Faulted")
	}
	return ocpp201.ReportDataType{Component: c, Variable: vt, VariableCharacteristics: ch,
		VariableAttribute: []ocpp201.VariableAttributeType{{Type: ocpp.Ptr(ocpp201.AttributeActual), Value: ocpp.Ptr(v.value), Mutability: &mut}}}
}

func (s station201) GetVariables(ctx context.Context, p ocpp.Peer, req *ocpp201.GetVariablesRequest) (*ocpp201.GetVariablesResponse, error) {
	resp := &ocpp201.GetVariablesResponse{GetVariableResult: []ocpp201.GetVariableResultType{}}
	for _, d := range req.GetVariableData {
		r := ocpp201.GetVariableResultType{Component: d.Component, Variable: d.Variable, AttributeType: d.AttributeType}
		if d.AttributeType != nil && *d.AttributeType != ocpp201.AttributeActual {
			r.AttributeStatus = ocpp201.GetVariableStatusNotSupportedAttributeType
		} else {
			name, evse := component201(d.Component)
			value, status := s.dm.get(name, evse, d.Variable.Name, instance201(d.Variable))
			r.AttributeStatus = ocpp201.GetVariableStatusEnumType(status)
			if status == "Accepted" {
				r.AttributeValue = ocpp.Ptr(value)
			}
		}
		resp.GetVariableResult = append(resp.GetVariableResult, r)
	}
	return resp, nil
}

func (s station201) SetVariables(ctx context.Context, p ocpp.Peer, req *ocpp201.SetVariablesRequest) (*ocpp201.SetVariablesResponse, error) {
	resp := &ocpp201.SetVariablesResponse{SetVariableResult: []ocpp201.SetVariableResultType{}}
	for _, d := range req.SetVariableData {
		r := ocpp201.SetVariableResultType{Component: d.Component, Variable: d.Variable, AttributeType: d.AttributeType}
		if d.AttributeType != nil && *d.AttributeType != ocpp201.AttributeActual {
			r.AttributeStatus = ocpp201.SetVariableStatusNotSupportedAttributeType
		} else {
			name, evse := component201(d.Component)
			status, reason := s.dm.set(name, evse, d.Variable.Name, instance201(d.Variable), d.AttributeValue)
			r.AttributeStatus = ocpp201.SetVariableStatusEnumType(status)
			if reason != "" {
				r.AttributeStatusInfo = &ocpp201.StatusInfoType{ReasonCode: reason}
			}
		}
		resp.SetVariableResult = append(resp.SetVariableResult, r)
	}
	return resp, nil
}

func (s station201) SetVariableMonitoring(ctx context.Context, p ocpp.Peer, req *ocpp201.SetVariableMonitoringRequest) (*ocpp201.SetVariableMonitoringResponse, error) {
	resp := &ocpp201.SetVariableMonitoringResponse{SetMonitoringResult: []ocpp201.SetMonitoringResultType{}}
	for _, d := range req.SetMonitoringData {
		name, evse := component201(d.Component)
		status, id := s.dm.monitor(name, evse, d.Variable.Name)
		r := ocpp201.SetMonitoringResultType{Status: ocpp201.SetMonitoringStatusEnumType(status), Type: d.Type, Severity: d.Severity,
			Component: d.Component, Variable: d.Variable}
		if id != 0 {
			r.ID = ocpp.Ptr(id)
		}
		resp.SetMonitoringResult = append(resp.SetMonitoringResult, r)
	}
	return resp, nil
}

type csms201 struct {
	ocpp201.UnimplementedCSMSHandler
	reports *reports
}

func (csms201) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (h csms201) NotifyReport(ctx context.Context, p ocpp.Peer, req *ocpp201.NotifyReportRequest) (*ocpp201.NotifyReportResponse, error) {
	tbc := req.Tbc != nil && *req.Tbc
	fmt.Printf("CSMS: NotifyReport %d part %d, %d items, tbc=%v\n", req.RequestID, req.SeqNo, len(req.ReportData), tbc)
	for _, d := range req.ReportData {
		name, evse := component201(d.Component)
		for _, a := range d.VariableAttribute {
			ro := a.Mutability != nil && *a.Mutability == ocpp201.MutabilityReadOnly
			value := ""
			if a.Value != nil {
				value = *a.Value
			}
			printVar("CSMS", name, evse, d.Variable.Name, instance201(d.Variable), value, ro)
		}
	}
	if !tbc {
		h.reports.finish(req.RequestID)
	}
	return &ocpp201.NotifyReportResponse{}, nil
}

func run201() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep := &reports{done: map[int]chan struct{}{}}
	c, session, cleanup, err := connect(ctx, ocpp.V201, ocpp201.CSMS(csms201{reports: rep}),
		ocpp201.ChargingStation(station201{dm: newDeviceModel()}))
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{Reason: ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "DeviceModelStation"}}); err != nil {
		return err
	}

	// B07: the report arrives in NotifyReport messages after the response.
	done := rep.expect(1)
	base, err := ocpp201.GetBaseReport(ctx, session, &ocpp201.GetBaseReportRequest{RequestID: 1, ReportBase: ocpp201.ReportBaseFullInventory})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: GetBaseReport FullInventory: %s\n", base.Status)
	if err := wait(ctx, done); err != nil {
		return err
	}

	comp := func(name string) ocpp201.ComponentType { return ocpp201.ComponentType{Name: name} }
	get := func() error {
		resp, err := ocpp201.GetVariables(ctx, session, &ocpp201.GetVariablesRequest{GetVariableData: []ocpp201.GetVariableDataType{
			{Component: comp("OCPPCommCtrlr"), Variable: ocpp201.VariableType{Name: "HeartbeatInterval"}},
			{Component: comp("SecurityCtrlr"), Variable: ocpp201.VariableType{Name: "SecurityProfile"}},
			{Component: comp("OCPPCommCtrlr"), Variable: ocpp201.VariableType{Name: "NoSuchVariable"}},
			{Component: comp("NoSuchCtrlr"), Variable: ocpp201.VariableType{Name: "Enabled"}},
		}})
		if err != nil {
			return err
		}
		for _, r := range resp.GetVariableResult {
			value := "-"
			if r.AttributeValue != nil {
				value = *r.AttributeValue
			}
			fmt.Printf("CSMS: get %s.%s: %s %s\n", r.Component.Name, r.Variable.Name, r.AttributeStatus, value)
		}
		return nil
	}
	if err := get(); err != nil {
		return err
	}

	set, err := ocpp201.SetVariables(ctx, session, &ocpp201.SetVariablesRequest{SetVariableData: []ocpp201.SetVariableDataType{
		{Component: comp("OCPPCommCtrlr"), Variable: ocpp201.VariableType{Name: "HeartbeatInterval"}, AttributeValue: "60"},
		{Component: comp("SecurityCtrlr"), Variable: ocpp201.VariableType{Name: "SecurityProfile"}, AttributeValue: "3"},
		{Component: comp("AuthCtrlr"), Variable: ocpp201.VariableType{Name: "AuthorizeRemoteStart"}, AttributeValue: "maybe"},
	}})
	if err != nil {
		return err
	}
	for _, r := range set.SetVariableResult {
		reason := ""
		if r.AttributeStatusInfo != nil {
			reason = " (" + r.AttributeStatusInfo.ReasonCode + ")"
		}
		fmt.Printf("CSMS: set %s.%s: %s%s\n", r.Component.Name, r.Variable.Name, r.AttributeStatus, reason)
	}
	if err := get(); err != nil {
		return err
	}

	evse1 := ocpp201.ComponentType{Name: "EVSE", EVSE: &ocpp201.EVSEType{ID: 1}}
	mon, err := ocpp201.SetVariableMonitoring(ctx, session, &ocpp201.SetVariableMonitoringRequest{SetMonitoringData: []ocpp201.SetMonitoringDataType{
		{Component: evse1, Variable: ocpp201.VariableType{Name: "Power"}, Type: ocpp201.MonitorUpperThreshold, Value: 11000, Severity: 5},
		{Component: comp("OCPPCommCtrlr"), Variable: ocpp201.VariableType{Name: "HeartbeatInterval"}, Type: ocpp201.MonitorDelta, Value: 1, Severity: 8},
	}})
	if err != nil {
		return err
	}
	for _, r := range mon.SetMonitoringResult {
		id := "-"
		if r.ID != nil {
			id = strconv.Itoa(*r.ID)
		}
		fmt.Printf("CSMS: monitor %s on %s.%s: %s, id %s\n", r.Type, r.Component.Name, r.Variable.Name, r.Status, id)
	}
	return nil
}

// ---------------------------------------------------------------------------
// OCPP 2.1: the same, with the ocpp21 types.

type station21 struct {
	ocpp21.UnimplementedChargingStationHandler
	dm *deviceModel
}

func component21(c ocpp21.ComponentType) (string, int) {
	if c.EVSE != nil {
		return c.Name, c.EVSE.ID
	}
	return c.Name, 0
}

func instance21(v ocpp21.VariableType) string {
	if v.Instance == nil {
		return ""
	}
	return *v.Instance
}

func (s station21) GetBaseReport(ctx context.Context, p ocpp.Peer, req *ocpp21.GetBaseReportRequest) (*ocpp21.GetBaseReportResponse, error) {
	if req.ReportBase == ocpp21.ReportBaseSummaryInventory {
		return &ocpp21.GetBaseReportResponse{Status: ocpp21.GenericDeviceModelStatusNotSupported}, nil
	}
	vars := s.dm.report(req.ReportBase == ocpp21.ReportBaseConfigurationInventory)
	parts := chunks(vars, s.dm.itemsPerMessage())
	fmt.Printf("station: base report %d (%s): %d variables in %d messages\n", req.RequestID, req.ReportBase, len(vars), len(parts))
	// The NotifyReport messages go out after the GetBaseReport response.
	ocpp.AfterResponse(ctx, func() {
		generated := time.Now().UTC()
		for i, part := range parts {
			data := make([]ocpp21.ReportDataType, len(part))
			for j, v := range part {
				data[j] = reportData21(v)
			}
			if _, err := ocpp21.NotifyReport(ctx, p, &ocpp21.NotifyReportRequest{RequestID: req.RequestID, GeneratedAt: generated,
				SeqNo: i, Tbc: ocpp.Ptr(i < len(parts)-1), ReportData: data}); err != nil {
				fmt.Printf("station: NotifyReport: %v\n", err)
				return
			}
		}
	})
	return &ocpp21.GetBaseReportResponse{Status: ocpp21.GenericDeviceModelStatusAccepted}, nil
}

func reportData21(v variable) ocpp21.ReportDataType {
	c := ocpp21.ComponentType{Name: v.component}
	if v.evse != 0 {
		c.EVSE = &ocpp21.EVSEType{ID: v.evse}
	}
	if v.componentInstance != "" {
		c.Instance = ocpp.Ptr(v.componentInstance)
	}
	vt := ocpp21.VariableType{Name: v.name}
	if v.instance != "" {
		vt.Instance = ocpp.Ptr(v.instance)
	}
	mut := ocpp21.MutabilityReadWrite
	if v.readOnly {
		mut = ocpp21.MutabilityReadOnly
	}
	ch := &ocpp21.VariableCharacteristicsType{DataType: ocpp21.DataEnumType(v.dataType), SupportsMonitoring: v.monitorable, MaxLimit: v.maxLimit}
	if v.unit != "" {
		ch.Unit = ocpp.Ptr(v.unit)
	}
	if v.dataType == "OptionList" {
		ch.ValuesList = ocpp.Ptr("Available,Occupied,Reserved,Unavailable,Faulted")
	}
	return ocpp21.ReportDataType{Component: c, Variable: vt, VariableCharacteristics: ch,
		VariableAttribute: []ocpp21.VariableAttributeType{{Type: ocpp.Ptr(ocpp21.AttributeActual), Value: ocpp.Ptr(v.value), Mutability: &mut}}}
}

func (s station21) GetVariables(ctx context.Context, p ocpp.Peer, req *ocpp21.GetVariablesRequest) (*ocpp21.GetVariablesResponse, error) {
	resp := &ocpp21.GetVariablesResponse{GetVariableResult: []ocpp21.GetVariableResultType{}}
	for _, d := range req.GetVariableData {
		r := ocpp21.GetVariableResultType{Component: d.Component, Variable: d.Variable, AttributeType: d.AttributeType}
		if d.AttributeType != nil && *d.AttributeType != ocpp21.AttributeActual {
			r.AttributeStatus = ocpp21.GetVariableStatusNotSupportedAttributeType
		} else {
			name, evse := component21(d.Component)
			value, status := s.dm.get(name, evse, d.Variable.Name, instance21(d.Variable))
			r.AttributeStatus = ocpp21.GetVariableStatusEnumType(status)
			if status == "Accepted" {
				r.AttributeValue = ocpp.Ptr(value)
			}
		}
		resp.GetVariableResult = append(resp.GetVariableResult, r)
	}
	return resp, nil
}

func (s station21) SetVariables(ctx context.Context, p ocpp.Peer, req *ocpp21.SetVariablesRequest) (*ocpp21.SetVariablesResponse, error) {
	resp := &ocpp21.SetVariablesResponse{SetVariableResult: []ocpp21.SetVariableResultType{}}
	for _, d := range req.SetVariableData {
		r := ocpp21.SetVariableResultType{Component: d.Component, Variable: d.Variable, AttributeType: d.AttributeType}
		if d.AttributeType != nil && *d.AttributeType != ocpp21.AttributeActual {
			r.AttributeStatus = ocpp21.SetVariableStatusNotSupportedAttributeType
		} else {
			name, evse := component21(d.Component)
			status, reason := s.dm.set(name, evse, d.Variable.Name, instance21(d.Variable), d.AttributeValue)
			r.AttributeStatus = ocpp21.SetVariableStatusEnumType(status)
			if reason != "" {
				r.AttributeStatusInfo = &ocpp21.StatusInfoType{ReasonCode: reason}
			}
		}
		resp.SetVariableResult = append(resp.SetVariableResult, r)
	}
	return resp, nil
}

func (s station21) SetVariableMonitoring(ctx context.Context, p ocpp.Peer, req *ocpp21.SetVariableMonitoringRequest) (*ocpp21.SetVariableMonitoringResponse, error) {
	resp := &ocpp21.SetVariableMonitoringResponse{SetMonitoringResult: []ocpp21.SetMonitoringResultType{}}
	for _, d := range req.SetMonitoringData {
		name, evse := component21(d.Component)
		status, id := s.dm.monitor(name, evse, d.Variable.Name)
		r := ocpp21.SetMonitoringResultType{Status: ocpp21.SetMonitoringStatusEnumType(status), Type: d.Type, Severity: d.Severity,
			Component: d.Component, Variable: d.Variable}
		if id != 0 {
			r.ID = ocpp.Ptr(id)
		}
		resp.SetMonitoringResult = append(resp.SetMonitoringResult, r)
	}
	return resp, nil
}

type csms21 struct {
	ocpp21.UnimplementedCSMSHandler
	reports *reports
}

func (csms21) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp21.BootNotificationRequest) (*ocpp21.BootNotificationResponse, error) {
	return &ocpp21.BootNotificationResponse{Status: ocpp21.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (h csms21) NotifyReport(ctx context.Context, p ocpp.Peer, req *ocpp21.NotifyReportRequest) (*ocpp21.NotifyReportResponse, error) {
	tbc := req.Tbc != nil && *req.Tbc
	fmt.Printf("CSMS: NotifyReport %d part %d, %d items, tbc=%v\n", req.RequestID, req.SeqNo, len(req.ReportData), tbc)
	for _, d := range req.ReportData {
		name, evse := component21(d.Component)
		for _, a := range d.VariableAttribute {
			ro := a.Mutability != nil && *a.Mutability == ocpp21.MutabilityReadOnly
			value := ""
			if a.Value != nil {
				value = *a.Value
			}
			printVar("CSMS", name, evse, d.Variable.Name, instance21(d.Variable), value, ro)
		}
	}
	if !tbc {
		h.reports.finish(req.RequestID)
	}
	return &ocpp21.NotifyReportResponse{}, nil
}

func run21() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep := &reports{done: map[int]chan struct{}{}}
	c, session, cleanup, err := connect(ctx, ocpp.V21, ocpp21.CSMS(csms21{reports: rep}),
		ocpp21.ChargingStation(station21{dm: newDeviceModel()}))
	if err != nil {
		return err
	}
	defer cleanup()
	if _, err := ocpp21.BootNotification(ctx, c, &ocpp21.BootNotificationRequest{Reason: ocpp21.BootReasonPowerUp,
		ChargingStation: ocpp21.ChargingStationType{VendorName: "OT Fabric", Model: "DeviceModelStation"}}); err != nil {
		return err
	}

	done := rep.expect(1)
	base, err := ocpp21.GetBaseReport(ctx, session, &ocpp21.GetBaseReportRequest{RequestID: 1, ReportBase: ocpp21.ReportBaseFullInventory})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: GetBaseReport FullInventory: %s\n", base.Status)
	if err := wait(ctx, done); err != nil {
		return err
	}

	comp := func(name string) ocpp21.ComponentType { return ocpp21.ComponentType{Name: name} }
	get := func() error {
		resp, err := ocpp21.GetVariables(ctx, session, &ocpp21.GetVariablesRequest{GetVariableData: []ocpp21.GetVariableDataType{
			{Component: comp("OCPPCommCtrlr"), Variable: ocpp21.VariableType{Name: "HeartbeatInterval"}},
			{Component: comp("SecurityCtrlr"), Variable: ocpp21.VariableType{Name: "SecurityProfile"}},
			{Component: comp("OCPPCommCtrlr"), Variable: ocpp21.VariableType{Name: "NoSuchVariable"}},
			{Component: comp("NoSuchCtrlr"), Variable: ocpp21.VariableType{Name: "Enabled"}},
		}})
		if err != nil {
			return err
		}
		for _, r := range resp.GetVariableResult {
			value := "-"
			if r.AttributeValue != nil {
				value = *r.AttributeValue
			}
			fmt.Printf("CSMS: get %s.%s: %s %s\n", r.Component.Name, r.Variable.Name, r.AttributeStatus, value)
		}
		return nil
	}
	if err := get(); err != nil {
		return err
	}

	set, err := ocpp21.SetVariables(ctx, session, &ocpp21.SetVariablesRequest{SetVariableData: []ocpp21.SetVariableDataType{
		{Component: comp("OCPPCommCtrlr"), Variable: ocpp21.VariableType{Name: "HeartbeatInterval"}, AttributeValue: "60"},
		{Component: comp("SecurityCtrlr"), Variable: ocpp21.VariableType{Name: "SecurityProfile"}, AttributeValue: "3"},
		{Component: comp("AuthCtrlr"), Variable: ocpp21.VariableType{Name: "AuthorizeRemoteStart"}, AttributeValue: "maybe"},
	}})
	if err != nil {
		return err
	}
	for _, r := range set.SetVariableResult {
		reason := ""
		if r.AttributeStatusInfo != nil {
			reason = " (" + r.AttributeStatusInfo.ReasonCode + ")"
		}
		fmt.Printf("CSMS: set %s.%s: %s%s\n", r.Component.Name, r.Variable.Name, r.AttributeStatus, reason)
	}
	if err := get(); err != nil {
		return err
	}

	evse1 := ocpp21.ComponentType{Name: "EVSE", EVSE: &ocpp21.EVSEType{ID: 1}}
	mon, err := ocpp21.SetVariableMonitoring(ctx, session, &ocpp21.SetVariableMonitoringRequest{SetMonitoringData: []ocpp21.SetMonitoringDataType{
		{Component: evse1, Variable: ocpp21.VariableType{Name: "Power"}, Type: ocpp21.MonitorUpperThreshold, Value: 11000, Severity: 5},
		{Component: comp("OCPPCommCtrlr"), Variable: ocpp21.VariableType{Name: "HeartbeatInterval"}, Type: ocpp21.MonitorDelta, Value: 1, Severity: 8},
	}})
	if err != nil {
		return err
	}
	for _, r := range mon.SetMonitoringResult {
		id := "-"
		if r.ID != nil {
			id = strconv.Itoa(*r.ID)
		}
		fmt.Printf("CSMS: monitor %s on %s.%s: %s, id %s\n", r.Type, r.Component.Name, r.Variable.Name, r.Status, id)
	}
	return nil
}
