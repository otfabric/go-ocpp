// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// expired issues a client certificate that expired an hour ago.
func (p *pki) expired(t *testing.T, cn string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-2 * time.Hour), NotAfter: time.Now().Add(-time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Security profile 3 refusals: a client certificate of another CA, an
// expired one, and none at all, against a CSMS that requires one; and TLS
// below 1.2, which a CSMS that listens itself refuses.
func TestTLSRefusals(t *testing.T) {
	noLeak(t)
	p, other := newPKI(t), newPKI(t)
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{p.issue(t, "csms", true)},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    p.caPool,
	}
	byCert := server.AuthenticatorFunc(func(ctx context.Context, r server.ConnectRequest) error {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != r.StationID {
			return errors.New("no certificate of the station")
		}
		return nil
	})
	srv, err := server.New(server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithAuthenticator(byCert), server.WithTLS(tlsCfg))
	if err != nil {
		t.Fatal(err)
	}
	base := "wss://" + listening(t, srv) + "/ocpp/"

	good := station(t, base+"P3", client.WithVersions(ocpp.V201),
		client.WithTLS(&tls.Config{RootCAs: p.caPool, Certificates: []tls.Certificate{p.issue(t, "P3", false)}}))
	connect(t, good)

	for name, cfg := range map[string]*tls.Config{
		"another CA":     {RootCAs: p.caPool, Certificates: []tls.Certificate{other.issue(t, "P3", false)}},
		"expired":        {RootCAs: p.caPool, Certificates: []tls.Certificate{p.expired(t, "P3")}},
		"no certificate": {RootCAs: p.caPool},
		"TLS 1.1":        {RootCAs: p.caPool, Certificates: []tls.Certificate{p.issue(t, "P3", false)}, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11},
	} {
		c := station(t, base+"P3", client.WithVersions(ocpp.V201), client.WithTLS(cfg), client.WithHandshakeTimeout(5*time.Second))
		if err := c.Connect(context.Background()); !errors.Is(err, ocpp.ErrConnectFailed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(srv.Stations()) != 1 {
		t.Fatalf("%d sessions, want only the good station's", len(srv.Stations()))
	}
}

// A ws:// URL needs WithInsecureTransport, and credentials do not go in
// the URL: both are refused before anything is sent.
func TestClientRefusesInsecureSetups(t *testing.T) {
	for _, tt := range []struct {
		url  string
		opts []client.Option
	}{
		{"ws://127.0.0.1:1/ocpp/CS1", []client.Option{client.WithVersions(ocpp.V16)}},
		{"wss://user:pass@127.0.0.1:1/ocpp/CS1", []client.Option{client.WithVersions(ocpp.V16)}},
		{"wss://127.0.0.1:1/ocpp/CS1", []client.Option{client.WithVersions(ocpp.V16), client.WithPingInterval(-time.Second)}},
		{"wss://127.0.0.1:1/ocpp/CS1", []client.Option{client.WithVersions(ocpp.V16), client.WithMaxBadMessages(-1)}},
	} {
		if _, err := client.New(tt.url, tt.opts...); !errors.Is(err, ocpp.ErrInvalidOption) {
			t.Errorf("%s: %v", tt.url, err)
		}
	}
}

// On OCPP 2.x the CSMS refuses an identity that is not an identifierString
// of at most 48 characters without ":" (Part 4 §3.1.1), with HTTP 400; on
// 1.6, which sets no such rule, it is admitted.
func TestInvalidIdentity(t *testing.T) {
	noLeak(t)
	_, base := csms(t, nil, server.WithHandler(ocpp.V16, ocpp.DispatcherFunc(echo)), server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)),
		server.WithoutAuthentication())
	for _, id := range []string{"RDAM%20123", "CS%3A1", strings.Repeat("x", 49)} {
		c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
		err := c.Connect(context.Background())
		if !errors.Is(err, ocpp.ErrConnectFailed) || !strings.Contains(err.Error(), "400") {
			t.Errorf("2.0.1 identity %q: %v", id, err)
		}
	}
	c := station(t, base+"RDAM%20123", client.WithInsecureTransport(), client.WithVersions(ocpp.V16))
	connect(t, c)
}

// CertificateAuth admits a station by its client certificate; AnyOf lets
// one CSMS admit profile 2 and profile 3 stations; a handler sees the TLS
// state through Session.TLS.
func TestCertificateAuthAndAnyOf(t *testing.T) {
	noLeak(t)
	p := newPKI(t)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{p.issue(t, "csms", true)}, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: p.caPool}
	sawCert := make(chan string, 4)
	h := ocpp.DispatcherFunc(func(ctx context.Context, peer ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		if s, ok := peer.(*server.Session); ok && s.TLS() != nil && len(s.TLS().PeerCertificates) > 0 {
			sawCert <- s.TLS().PeerCertificates[0].Subject.CommonName
		} else {
			sawCert <- ""
		}
		return json.RawMessage(`{}`), nil
	})
	auth := server.AnyOf(server.CertificateAuth(), server.BasicAuth(func(ctx context.Context, id string) (string, bool) {
		return "profile-2-password", id == "P2"
	}))
	_, base := csms(t, tlsCfg, server.WithHandler(ocpp.V201, h), server.WithAuthenticator(auth))

	c3 := station(t, base+"P3", client.WithVersions(ocpp.V201),
		client.WithTLS(&tls.Config{RootCAs: p.caPool, Certificates: []tls.Certificate{p.issue(t, "P3", false)}}))
	connect(t, c3)
	_ = c3.Call(context.Background(), "Heartbeat", nil, nil)
	if cn := <-sawCert; cn != "P3" {
		t.Fatalf("handler saw certificate %q", cn)
	}
	c2 := station(t, base+"P2", client.WithVersions(ocpp.V201), client.WithTLS(&tls.Config{RootCAs: p.caPool}),
		client.WithBasicAuth("P2", "profile-2-password"))
	connect(t, c2)
	_ = c2.Call(context.Background(), "Heartbeat", nil, nil)
	if cn := <-sawCert; cn != "" {
		t.Fatalf("profile 2 handler saw certificate %q", cn)
	}
	// Without a certificate or credentials, CertificateAuth refuses (401):
	// it cannot know whether the station exists, so neither is told 404.
	for _, id := range []string{"P2X", "P3"} {
		c := station(t, base+id, client.WithVersions(ocpp.V201), client.WithTLS(&tls.Config{RootCAs: p.caPool}))
		var he *client.HandshakeError
		if err := c.Connect(context.Background()); !errors.As(err, &he) || he.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: %v, want 401", id, err)
		}
	}
	// Every authenticator says unknown: 404.
	unknown := server.AuthenticatorFunc(func(context.Context, server.ConnectRequest) error { return server.ErrUnknownStation })
	if err := server.AnyOf(unknown, unknown).Authenticate(context.Background(), server.ConnectRequest{}); !errors.Is(err, server.ErrUnknownStation) {
		t.Fatalf("all unknown: %v", err)
	}
	if err := server.AnyOf().Authenticate(context.Background(), server.ConnectRequest{}); !errors.Is(err, server.ErrUnauthorized) {
		t.Fatalf("AnyOf(): %v", err)
	}
}
