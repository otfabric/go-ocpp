// SPDX-License-Identifier: MIT

// Command security-profiles runs one OCPP 2.0.1 CSMS that accepts the
// three OCPP security profiles, and stations that connect with each:
//
//	profile 1: HTTP Basic authentication over plain WebSocket (ws://)
//	profile 2: TLS with HTTP Basic authentication (wss://)
//	profile 3: TLS with a client certificate (mutual TLS, wss://)
//
// It generates a throwaway certificate authority at start-up: no keys are
// stored anywhere. It shows connections that are refused (a wrong
// password, the certificate of another station, a profile 2 station
// trying plain WebSocket) and rotates the CSMS's server certificate
// without a restart.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/server"
)

// A station's registration: the profile it must use and, for profiles 1
// and 2, its password. In production this lives in a database, with the
// password hashed or in a secret store.
type registration struct {
	profile  int
	password string
}

var stations = map[string]registration{
	"SEC-P1-0001": {profile: 1, password: "p1-example-password-0001"},
	"SEC-P2-0001": {profile: 2, password: "p2-example-password-0001"},
	"SEC-P3-0001": {profile: 3},
	"SEC-P3-0002": {profile: 3},
}

// authenticate decides about every connection before the WebSocket
// upgrade. A station may only use the profile it is registered for, so
// that a profile 2 or 3 station cannot be downgraded to plain WebSocket.
func authenticate(ctx context.Context, r server.ConnectRequest) error {
	reg, ok := stations[r.StationID]
	if !ok {
		return server.ErrUnknownStation // HTTP 404
	}
	overTLS := r.TLS != nil
	hasCert := overTLS && len(r.TLS.VerifiedChains) > 0
	switch {
	case reg.profile == 3:
		// The TLS layer verified the chain against ClientCAs; the CSMS
		// checks that the certificate is this station's.
		if !hasCert {
			return fmt.Errorf("%w: profile 3 needs a client certificate", server.ErrUnauthorized)
		}
		if cn := r.TLS.PeerCertificates[0].Subject.CommonName; cn != r.StationID {
			return fmt.Errorf("%w: certificate of %q", server.ErrUnauthorized, cn)
		}
		fmt.Printf("CSMS: %s authenticated with its certificate (profile 3)\n", r.StationID)
		return nil
	case reg.profile == 2 && !overTLS:
		return fmt.Errorf("%w: profile 2 needs TLS", server.ErrUnauthorized)
	case reg.profile == 1 && overTLS:
		return fmt.Errorf("%w: registered for profile 1", server.ErrUnauthorized)
	}
	// Profiles 1 and 2: the user name is the identity; compare the
	// password in constant time. (server.BasicAuth does the same when
	// Basic authentication is all a CSMS needs.)
	if !r.HasBasicAuth || r.Username != r.StationID ||
		subtle.ConstantTimeCompare([]byte(r.Password), []byte(reg.password)) != 1 {
		return server.ErrUnauthorized // the password itself is never logged: Secret prints [redacted]
	}
	fmt.Printf("CSMS: %s authenticated with its password (profile %d)\n", r.StationID, reg.profile)
	return nil
}

type csms struct {
	ocpp201.UnimplementedCSMSHandler
}

func (csms) BootNotification(ctx context.Context, p ocpp.Peer, req *ocpp201.BootNotificationRequest) (*ocpp201.BootNotificationResponse, error) {
	fmt.Printf("CSMS: %s booted\n", p.ID())
	return &ocpp201.BootNotificationResponse{Status: ocpp201.RegistrationStatusAccepted, CurrentTime: time.Now().UTC(), Interval: 300}, nil
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pki, err := newPKI()
	if err != nil {
		return err
	}

	// The server certificate is read through GetCertificate on every
	// handshake, so it can be replaced while the CSMS runs.
	var serverCert atomic.Pointer[tls.Certificate]
	first, err := pki.issue("csms", true, 2)
	if err != nil {
		return err
	}
	serverCert.Store(first)
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return serverCert.Load(), nil
		},
		// Profile 3 stations present a certificate, profile 2 stations do
		// not: verify one when it is given.
		ClientAuth: tls.VerifyClientCertIfGiven,
		ClientCAs:  pki.pool,
	}

	srv, err := server.New(
		server.WithHandler(ocpp.V201, ocpp201.CSMS(csms{})),
		server.WithAuthenticator(server.AuthenticatorFunc(authenticate)),
		server.WithTLS(tlsConfig),
	)
	if err != nil {
		return err
	}
	// One Server, two listeners: TLS (profiles 2 and 3) through Serve,
	// and plain WebSocket (profile 1) by mounting the Server in an
	// http.Server without TLS. Both share the handler and the sessions.
	tlsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	go func() { _ = srv.Serve(tlsLn) }()
	defer func() { _ = srv.Shutdown(ctx) }()
	plainLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	plain := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = plain.Serve(plainLn) }()
	defer func() { _ = plain.Close() }()
	wss := "wss://" + tlsLn.Addr().String() + "/ocpp/"
	ws := "ws://" + plainLn.Addr().String() + "/ocpp/"

	p3a, err := pki.issue("SEC-P3-0001", false, 10)
	if err != nil {
		return err
	}
	p3b, err := pki.issue("SEC-P3-0002", false, 11)
	if err != nil {
		return err
	}
	trust := func(cert *tls.Certificate) *tls.Config {
		cfg := &tls.Config{RootCAs: pki.pool, VerifyConnection: func(cs tls.ConnectionState) error {
			fmt.Printf("station: CSMS certificate serial %s\n", cs.PeerCertificates[0].SerialNumber)
			return nil
		}}
		if cert != nil {
			cfg.Certificates = []tls.Certificate{*cert}
		}
		return cfg
	}

	attempts := []struct {
		what string
		url  string
		opts []client.Option
		ok   bool
	}{
		{"profile 1", ws + "SEC-P1-0001", []client.Option{
			client.WithBasicAuth("SEC-P1-0001", stations["SEC-P1-0001"].password), client.WithInsecureTransport()}, true},
		{"profile 2", wss + "SEC-P2-0001", []client.Option{
			client.WithBasicAuth("SEC-P2-0001", stations["SEC-P2-0001"].password), client.WithTLS(trust(nil))}, true},
		{"profile 3", wss + "SEC-P3-0001", []client.Option{client.WithTLS(trust(p3a))}, true},
		{"profile 2, wrong password", wss + "SEC-P2-0001", []client.Option{
			client.WithBasicAuth("SEC-P2-0001", "not-the-password-000000"), client.WithTLS(trust(nil))}, false},
		{"profile 3, another station's certificate", wss + "SEC-P3-0001", []client.Option{client.WithTLS(trust(p3b))}, false},
		{"profile 2 over plain WebSocket", ws + "SEC-P2-0001", []client.Option{
			client.WithBasicAuth("SEC-P2-0001", stations["SEC-P2-0001"].password), client.WithInsecureTransport()}, false},
		{"unknown station", wss + "SEC-XX-0001", []client.Option{
			client.WithBasicAuth("SEC-XX-0001", "whatever-password-00000"), client.WithTLS(trust(nil))}, false},
	}
	for _, a := range attempts {
		fmt.Printf("\n--- %s ---\n", a.what)
		if err := attempt(ctx, a.url, a.ok, a.opts...); err != nil {
			return fmt.Errorf("%s: %w", a.what, err)
		}
	}

	// Certificate rotation: new connections get the new certificate;
	// existing ones keep theirs until they reconnect.
	fmt.Println("\n--- server certificate rotation ---")
	next, err := pki.issue("csms", true, 3)
	if err != nil {
		return err
	}
	serverCert.Store(next)
	fmt.Println("CSMS: server certificate replaced (serial 3)")
	return attempt(ctx, wss+"SEC-P2-0001", true,
		client.WithBasicAuth("SEC-P2-0001", stations["SEC-P2-0001"].password), client.WithTLS(trust(nil)))
}

// attempt connects a station, boots it and disconnects; wantOK says
// whether the CSMS should accept it.
func attempt(ctx context.Context, url string, wantOK bool, opts ...client.Option) error {
	opts = append(opts, client.WithVersions(ocpp.V201),
		client.WithHandler(ocpp.V201, ocpp201.ChargingStation(ocpp201.UnimplementedChargingStationHandler{})))
	c, err := client.New(url, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	err = c.Connect(ctx)
	switch {
	case err != nil && !wantOK && errors.Is(err, ocpp.ErrConnectFailed):
		fmt.Printf("station: refused as expected: %v\n", err)
		return nil
	case err != nil:
		return err
	case !wantOK:
		return errors.New("connected, but should have been refused")
	}
	_, err = ocpp201.BootNotification(ctx, c, &ocpp201.BootNotificationRequest{Reason: ocpp201.BootReasonPowerUp,
		ChargingStation: ocpp201.ChargingStationType{VendorName: "OT Fabric", Model: "SecureStation"}})
	return err
}

// ---------------------------------------------------------------------------
// A throwaway PKI. In production the CSMS certificate comes from a CA the
// stations trust, and station certificates are issued through
// SignCertificate / CertificateSigned (2.0.1 Part 2 A02, A03).

type pki struct {
	ca   *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newPKI() (*pki, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example CA", Organization: []string{"OT Fabric example"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &pki{ca: ca, key: key, pool: pool}, nil
}

// issue makes a server certificate for 127.0.0.1, or a client
// certificate whose CommonName is the station identity.
func (p *pki) issue(cn string, isServer bool, serial int64) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn, Organization: []string{"OT Fabric example"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if isServer {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
