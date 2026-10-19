// SPDX-License-Identifier: MIT

// Command ocpp16 runs an OCPP 1.6J Central System and Charge Point in one
// process, over TLS with Basic authentication (security profile 2).
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
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/server"
)

const (
	stationID = "OT-FABRIC-TEST-001"
	password  = "an-example-password-1234" // in production: per station, random, from a secret store
)

// centralSystem answers the Charge Point's CALLs. Embedding the
// Unimplemented handler answers everything else with NotSupported.
type centralSystem struct {
	ocpp16.UnimplementedCentralSystemHandler
}

func (centralSystem) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.BootNotificationRequest) (*ocpp16.BootNotificationResponse, error) {
	fmt.Printf("CSMS: %s booted: %s %s\n", p.ID(), req.ChargePointVendor, req.ChargePointModel)
	// A handler may call the station while it handles a CALL.
	conf, err := ocpp16.GetConfiguration(ctx, p, &ocpp16.GetConfigurationRequest{Key: []string{"HeartbeatInterval"}})
	if err != nil {
		return nil, err
	}
	for _, kv := range conf.ConfigurationKey {
		fmt.Printf("CSMS: %s has %s = %s\n", p.ID(), kv.Key, *kv.Value)
	}
	return &ocpp16.BootNotificationResponse{Status: ocpp16.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func (centralSystem) StatusNotification(ctx context.Context, p ocpp.Peer, req *ocpp16.StatusNotificationRequest) (*ocpp16.StatusNotificationResponse, error) {
	fmt.Printf("CSMS: connector %d is %s\n", req.ConnectorID, req.Status)
	return &ocpp16.StatusNotificationResponse{}, nil
}

func (centralSystem) Authorize(ctx context.Context, p ocpp.Peer, req *ocpp16.AuthorizeRequest) (*ocpp16.AuthorizeResponse, error) {
	status := ocpp16.AuthorizationStatusInvalid
	if req.IDTag == "04A1B2C3D4E5F6" {
		status = ocpp16.AuthorizationStatusAccepted
	}
	return &ocpp16.AuthorizeResponse{IDTagInfo: ocpp16.IDTagInfo{Status: status}}, nil
}

// chargePoint answers the Central System's CALLs.
type chargePoint struct {
	ocpp16.UnimplementedChargePointHandler
}

func (chargePoint) GetConfiguration(ctx context.Context, p ocpp.Peer, req *ocpp16.GetConfigurationRequest) (*ocpp16.GetConfigurationResponse, error) {
	return &ocpp16.GetConfigurationResponse{ConfigurationKey: []ocpp16.KeyValue{{Key: "HeartbeatInterval", Value: ocpp.Ptr("300")}}}, nil
}

func (chargePoint) RemoteStartTransaction(ctx context.Context, p ocpp.Peer, req *ocpp16.RemoteStartTransactionRequest) (*ocpp16.RemoteStartTransactionResponse, error) {
	fmt.Printf("station: remote start for %s\n", req.IDTag)
	return &ocpp16.RemoteStartTransactionResponse{Status: ocpp16.RemoteStartStopStatusAccepted}, nil
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

	// The Central System.
	srv, err := server.New(
		server.WithHandler(ocpp.V16, ocpp16.CentralSystem(centralSystem{})),
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

	// The Charge Point.
	c, err := client.New("wss://"+ln.Addr().String()+"/ocpp/"+stationID,
		client.WithVersions(ocpp.V16),
		client.WithHandler(ocpp.V16, ocpp16.ChargePoint(chargePoint{})),
		client.WithBasicAuth(stationID, password),
		client.WithTLS(clientTLS),
	)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	boot, err := ocpp16.BootNotification(ctx, c, &ocpp16.BootNotificationRequest{ChargePointVendor: "OT Fabric", ChargePointModel: "TestStation"})
	if err != nil {
		return err
	}
	fmt.Printf("station: boot %s, heartbeat every %ds\n", boot.Status, boot.Interval)
	if _, err := ocpp16.StatusNotification(ctx, c, &ocpp16.StatusNotificationRequest{
		ConnectorID: 1, ErrorCode: ocpp16.ChargePointErrorCodeNoError, Status: ocpp16.ChargePointStatusAvailable,
	}); err != nil {
		return err
	}
	auth, err := ocpp16.Authorize(ctx, c, &ocpp16.AuthorizeRequest{IDTag: "04A1B2C3D4E5F6"})
	if err != nil {
		return err
	}
	fmt.Printf("station: token %s\n", auth.IDTagInfo.Status)

	// The Central System sends a CALL to the station.
	session, ok := srv.Station(stationID)
	if !ok {
		return fmt.Errorf("no session for %s", stationID)
	}
	start, err := ocpp16.RemoteStartTransaction(ctx, session, &ocpp16.RemoteStartTransactionRequest{IDTag: "04A1B2C3D4E5F6", ConnectorID: ocpp.Ptr(1)})
	if err != nil {
		return err
	}
	fmt.Printf("CSMS: remote start %s\n", start.Status)
	return nil
}

// exampleTLS makes a throwaway certificate authority and a server
// certificate for 127.0.0.1. In production the CSMS has a certificate from
// a CA the stations trust.
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
