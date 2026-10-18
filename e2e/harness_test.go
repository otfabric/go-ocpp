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
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// echo answers every CALL with the action and the payload it received, so
// tests can see that the right request arrived on the right connection.
func echo(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(fmt.Sprintf(`{"station":%q,"version":%q,"action":%q,"echo":%s}`, p.ID(), p.Version(), action, payload)), nil
}

type echoed struct {
	Station string          `json:"station"`
	Version string          `json:"version"`
	Action  string          `json:"action"`
	Echo    json.RawMessage `json:"echo"`
}

// csms starts a Server behind an httptest server at /ocpp/ and returns it
// with the base URL stations connect to (ws://host/ocpp/).
func csms(t *testing.T, tlsCfg *tls.Config, opts ...server.Option) (*server.Server, string) {
	t.Helper()
	srv, err := server.New(opts...)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/ocpp/", srv)
	hs := httptest.NewUnstartedServer(mux)
	scheme := "ws"
	if tlsCfg != nil {
		hs.TLS = tlsCfg
		hs.StartTLS()
		scheme = "wss"
	} else {
		hs.Start()
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
		hs.Close()
	})
	return srv, scheme + "://" + strings.TrimPrefix(strings.TrimPrefix(hs.URL, "http://"), "https://") + "/ocpp/"
}

// station returns a client; the test connects it.
func station(t *testing.T, url string, opts ...client.Option) *client.Client {
	t.Helper()
	c, err := client.New(url, opts...)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func connect(t *testing.T, c *client.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
}

// waitStation waits until the server has a session for id.
func waitStation(t *testing.T, srv *server.Server, id string) *server.Session {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := srv.Station(id); ok {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no session for %s", id)
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// noLeak fails the test if goroutines started during it are still running
// after its cleanups.
func noLeak(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		deadline := time.Now().Add(10 * time.Second)
		for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if n := runtime.NumGoroutine(); n > before {
			buf := make([]byte, 1<<20)
			t.Errorf("%d goroutines leaked:\n%s", n-before, buf[:runtime.Stack(buf, true)])
		}
	})
}

// stateLog records a client's state changes.
type stateLog struct {
	mu     sync.Mutex
	states []ocpp.State
}

func (l *stateLog) handler(s ocpp.State, err error) {
	l.mu.Lock()
	l.states = append(l.states, s)
	l.mu.Unlock()
}

func (l *stateLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, len(l.states))
	for i, s := range l.states {
		names[i] = s.String()
	}
	return "[" + strings.Join(names, " ") + "]"
}

// pki is a throwaway certificate authority for TLS tests, generated per
// run: no key is committed (SECURITY.md).
type pki struct {
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPool *x509.CertPool
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "go-ocpp test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &pki{ca: ca, caKey: key, caPool: pool}
}

func (p *pki) issue(t *testing.T, cn string, server bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.DNSNames = []string{"localhost"}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
