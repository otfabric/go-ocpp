// SPDX-License-Identifier: MIT

// Command ocpp201 runs an OCPP 2.0.1 CSMS and Charging Station in one
// process, over TLS with Basic authentication (security profile 2) and
// WebSocket compression.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"math/big"
	"net"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/server"
)

const (
	stationID = "OT-FABRIC-TEST-001"
	password  = "an-example-password-1234" // in production: per station, random, from a secret store
)

// csms answers the Charging Station's CALLs.
type csms struct {
	ocpp201.UnimplementedCSMSHandler
}

func (csms) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	fmt.Printf("CSMS: %s booted (%s): %s %s\n", p.ID(), req.Reason, req.ChargingStation.VendorName, req.ChargingStation.Model)
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (csms) TransactionEvent(ctx context.Context, p ocpp.Peer, req *ocpp201.TransactionEventRequest) (*ocpp201.TransactionEventResponse, error) {
	fmt.Printf("CSMS: transaction %s %s (%s)\n", req.TransactionInfo.TransactionID, req.EventType, req.TriggerReason)
	return &ocpp201.TransactionEventResponse{}, nil
}

// station answers the CSMS's CALLs from a small device model.
type station struct {
	ocpp201.UnimplementedChargingStationHandler
	variables map[[2]string]string // component, variable -> Actual value
}

func (s station) GetVariables(ctx context.Context, p ocpp.Peer, req *ocpp201.GetVariablesRequest) (*ocpp201.GetVariablesResponse, error) {
	resp := &ocpp201.GetVariablesResponse{GetVariableResult: []ocpp201.GetVariableResultType{}}
	for _, d := range req.GetVariableData {
		r := ocpp201.GetVariableResultType{Component: d.Component, Variable: d.Variable, AttributeStatus: ocpp201.GetVariableStatusUnknownVariable}
		if v, ok := s.variables[[2]string{d.Component.Name, d.Variable.Name}]; ok {
			r.AttributeStatus, r.AttributeValue = ocpp201.GetVariableStatusAccepted, ocpp.Ptr(v)
		}
		resp.GetVariableResult = append(resp.GetVariableResult, r)
	}
	return resp, nil
}

func (station) Reset(ctx context.Context, p ocpp.Peer, req *ocpp201.ResetRequest) (*ocpp201.ResetResponse, error) {
	fmt.Printf("station: reset %s requested\n", req.Type)
	return &ocpp201.ResetResponse{Status: ocpp201.ResetStatusScheduled}, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	serverTLS, clientTLS, err := exampleTLS()
	if err != nil {
		return err
	}

	srv, err := server.New(
		server.WithHandler(ocpp.V201, ocpp201.CSMS(csms{})),
		server.WithAuthenticator(server.BasicAuth(func(ctx context.Context, id string) (string, bool) {
			return password, id == stationID
		})),
		server.WithTLS(serverTLS),
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

	c, err := client.New("wss://"+ln.Addr().String()+"/ocpp/"+stationID,
		client.WithVersions(ocpp.V201),
		client.WithHandler(ocpp.V201, ocpp201.ChargingStation(station{variables: map[[2]string]string{
			{"OCPPCommCtrlr", "HeartbeatInterval"}: "300",
		}})),
		client.WithBasicAuth(stationID, password),
		client.WithTLS(clientTLS),
		client.WithCompression(),
		client.WithReconnect(client.DefaultReconnect()),
	)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	boot, err := ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{
		Reason:          ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OTFabric", Model: "TestStation"},
	})
	if err != nil {
		return err
	}
	fmt.Printf("station: boot %s\n", boot.Status)

	session, ok := srv.Station(stationID)
	if !ok {
		return fmt.Errorf("no session for %s", stationID)
	}
	vars, err := ocpp201.GetVariables(ctx, session, &ocpp201.GetVariablesRequest{GetVariableData: []ocpp201.GetVariableDataType{
		{Component: ocpp201.ComponentType{Name: "OCPPCommCtrlr"}, Variable: ocpp201.VariableType{Name: "HeartbeatInterval"}},
		{Component: ocpp201.ComponentType{Name: "OCPPCommCtrlr"}, Variable: ocpp201.VariableType{Name: "NoSuchVariable"}},
	}})
	if err != nil {
		return err
	}
	for _, r := range vars.GetVariableResult {
		value := "-"
		if r.AttributeValue != nil {
			value = *r.AttributeValue
		}
		fmt.Printf("CSMS: %s.%s: %s %s\n", r.Component.Name, r.Variable.Name, r.AttributeStatus, value)
	}

	if _, err := ocpp201.TransactionEvent(ctx, c, &ocpp201.TransactionEventRequest{
		EventType: ocpp201.TransactionEventStarted, Timestamp: time.Now().UTC(), TriggerReason: ocpp201.TriggerReasonAuthorized,
		SeqNo: 0, TransactionInfo: ocpp201.TransactionType{TransactionID: "TX-0001"},
		EVSE:    &ocpp201.EVSEType{ID: 1, ConnectorID: ocpp.Ptr(1)},
		IDToken: &ocpp201.IDTokenType{IDToken: "04A1B2C3D4E5F6", Type: ocpp201.IDTokenISO14443},
	}); err != nil {
		return err
	}

	reset, err := ocpp201.Reset(ctx, session, &ocpp201.ResetRequest{Type: ocpp201.ResetOnIdle})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: reset %s\n", reset.Status)
	return nil
}

func exampleTLS() (*tls.Config, *tls.Config, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "csms"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}},
		&tls.Config{RootCAs: pool}, nil
}
