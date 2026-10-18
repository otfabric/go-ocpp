//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// testCA is a certificate authority generated per run for security
// profiles 2 and 3 (contract 0.5, --tls-dir). RSA, which every reference
// accepts; no key is committed.
type testCA struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
	pem  []byte
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "go-ocpp interop CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pool: pool}
}

// issue returns a certificate and key in PEM, and as a tls.Certificate: a
// server certificate for hosts, or a client certificate whose CommonName
// is a station identity.
func (ca *testCA) issue(t *testing.T, cn string, hosts []string) (certPEM, keyPEM []byte, pair tls.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	if hosts != nil {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		for _, h := range hosts {
			if ip := net.ParseIP(h); ip != nil {
				tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
			} else {
				tmpl.DNSNames = append(tmpl.DNSNames, h)
			}
		}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	// Contract 0.5: unencrypted PKCS #8.
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	pair, err = tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, keyPEM, pair
}

// tlsDir writes files into a directory a container mounts read-only at
// /tls, and returns the docker run option that mounts it.
func tlsDir(t *testing.T, files map[string][]byte) []string {
	t.Helper()
	dir, err := os.MkdirTemp("", "go-ocpp-interop-tls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, b := range files {
		// Readable by the container's user: throwaway material of one run.
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"-v", dir + ":/tls:ro"}
}

// TestStationTLS connects the go-ocpp client with security profiles 2 and
// 3 to each reference CSMS that declares them: the right credentials work
// and a CALL in each direction is answered; a wrong password, the
// certificate of another station and an untrusted server are refused.
func TestStationTLS(t *testing.T) {
	ca, other := newTestCA(t), newTestCA(t)
	for _, a := range adapters(t) {
		for _, v := range versions {
			for _, profile := range []int{2, 3} {
				t.Run(fmt.Sprintf("%s/%s/profile%d", a.name, v, profile), func(t *testing.T) {
					a.role(t, v, "server")
					a.securityProfile(t, v, profile)
					a.skipKnownIssue(t, v, fmt.Sprintf("tls-profile%d-csms", profile))
					f := a.fixture(t, v)
					id, password := f.station()
					srvCert, srvKey, _ := ca.issue(t, "reference CSMS", []string{"localhost", "127.0.0.1"})
					mount := tlsDir(t, map[string][]byte{"server.crt": srvCert, "server.key": srvKey, "ca.crt": ca.pem})
					ref := startServerWith(t, a, v, mount, "--security-profile", fmt.Sprint(profile), "--tls-dir", "/tls")
					url := fmt.Sprintf("wss://127.0.0.1:%d/ocpp/%s", ref.port, id)

					creds := func(cn string, ca *testCA) []client.Option {
						if profile == 2 {
							return []client.Option{client.WithBasicAuth(id, password)}
						}
						_, _, pair := ca.issue(t, cn, nil)
						return []client.Option{client.WithTLS(&tls.Config{RootCAs: ca.pool, Certificates: []tls.Certificate{pair}})}
					}
					opts := append([]client.Option{client.WithVersions(v), client.WithTLS(&tls.Config{RootCAs: ca.pool})}, creds(id, ca)...)
					if profile == 3 {
						opts = append([]client.Option{client.WithVersions(v)}, creds(id, ca)...)
					}
					c, err := client.New(url, opts...)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = c.Close() })
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if err := c.Connect(ctx); err != nil {
						t.Fatalf("connect with profile %d: %v", profile, err)
					}
					if err := c.Call(ctx, "Heartbeat", json.RawMessage(`{}`), nil); err != nil {
						t.Fatalf("Heartbeat over TLS: %v", err)
					}

					// Refusals.
					var bad []client.Option
					if profile == 2 {
						wrong := []byte(password)
						wrong[len(wrong)-1] ^= 1
						bad = []client.Option{client.WithVersions(v), client.WithTLS(&tls.Config{RootCAs: ca.pool}), client.WithBasicAuth(id, string(wrong))}
					} else {
						_, _, pair := ca.issue(t, id+"X", nil) // another station's certificate
						bad = []client.Option{client.WithVersions(v), client.WithTLS(&tls.Config{RootCAs: ca.pool, Certificates: []tls.Certificate{pair}})}
					}
					for name, o := range map[string][]client.Option{
						"wrong credentials": bad,
						"untrusted server":  {client.WithVersions(v), client.WithTLS(&tls.Config{RootCAs: other.pool}), client.WithBasicAuth(id, password)},
					} {
						bc, err := client.New(url, o...)
						if err != nil {
							t.Fatal(err)
						}
						if err := bc.Connect(ctx); !errors.Is(err, ocpp.ErrConnectFailed) {
							t.Errorf("%s: %v", name, err)
						}
						_ = bc.Close()
					}
				})
			}
		}
	}
}

// TestReferenceStationTLS has each reference station that declares
// profiles 2 and 3 connect to a go-ocpp server over TLS: the session is
// established with the right credentials (for profile 3 the client
// certificate is the station's), a CALL is answered, and a station
// presenting another station's certificate or trusting another CA does not
// connect.
func TestReferenceStationTLS(t *testing.T) {
	ca, other := newTestCA(t), newTestCA(t)
	for _, a := range adapters(t) {
		for _, v := range versions {
			for _, profile := range []int{2, 3} {
				t.Run(fmt.Sprintf("%s/%s/profile%d", a.name, v, profile), func(t *testing.T) {
					a.role(t, v, "client")
					a.securityProfile(t, v, profile)
					a.skipKnownIssue(t, v, fmt.Sprintf("tls-profile%d-station", profile))
					f := a.fixture(t, v)
					id, password := f.station()
					_, _, srvPair := ca.issue(t, "go-ocpp CSMS", []string{"host.docker.internal", "localhost", "127.0.0.1"})
					cfg := &tls.Config{Certificates: []tls.Certificate{srvPair}, ClientCAs: ca.pool, ClientAuth: tls.VerifyClientCertIfGiven}
					auth := server.BasicAuth(func(ctx context.Context, station string) (string, bool) { return password, station == id })
					if profile == 3 {
						auth = server.CertificateAuth()
					}
					d := newDispatcher(f, f.Answers.CSMS, v)
					srv, port := newCSMS(t, server.WithHandler(v, d), server.WithAuthenticator(auth), server.WithTLS(cfg))

					cliCert, cliKey, _ := ca.issue(t, id, nil)
					mount := tlsDir(t, map[string][]byte{"ca.crt": ca.pem, "client.crt": cliCert, "client.key": cliKey})
					startStationWith(t, a, v, "wss", port, mount, "--security-profile", fmt.Sprint(profile), "--tls-dir", "/tls")
					sess := waitSession(t, srv, id)
					if profile == 3 {
						if st := sess.TLS(); st == nil || len(st.PeerCertificates) == 0 || st.PeerCertificates[0].Subject.CommonName != id {
							t.Fatalf("profile 3 session without the station's certificate: %+v", st)
						}
					}
					var resp json.RawMessage
					if err := sess.Call(context.Background(), "ClearCache", json.RawMessage(`{}`), &resp); err != nil {
						t.Fatalf("call over TLS: %v", err)
					}

					// Refusals: another station's certificate (profile 3) or a
					// wrong password (profile 2); a CA the server is not from.
					wrongCert, wrongKey, _ := ca.issue(t, id+"X", nil)
					badCA := map[string][]byte{"ca.crt": other.pem, "client.crt": cliCert, "client.key": cliKey}
					badCred := map[string][]byte{"ca.crt": ca.pem, "client.crt": wrongCert, "client.key": wrongKey}
					url := fmt.Sprintf("wss://host.docker.internal:%d/ocpp", port)
					cases := map[string][]string{"untrusted server": tlsDir(t, badCA)}
					if profile == 3 {
						cases["another station's certificate"] = tlsDir(t, badCred)
					}
					for name, m := range cases {
						if code := runOnce(t, a, v, url, m, "--security-profile", fmt.Sprint(profile), "--tls-dir", "/tls"); code == 0 {
							t.Errorf("%s: the station connected", name)
						}
					}
				})
			}
		}
	}
}

// TestStationCompression checks that each reference CSMS that declares
// compression accepts permessage-deflate when it is offered, and that the
// go-ocpp client, offering it, exchanges the fixture's samples with it.
func TestStationCompression(t *testing.T) {
	for _, a := range adapters(t) {
		for _, v := range versions {
			t.Run(a.name+"/"+v.String(), func(t *testing.T) {
				a.role(t, v, "server")
				if !a.caps(t).Versions[v.String()].Features.Compression {
					t.Skipf("%s does not declare compression for OCPP %s", a.name, v)
				}
				a.skipKnownIssue(t, v, "compression-csms")
				f := a.fixture(t, v)
				id, _ := f.station()
				ref := startServer(t, a, v)
				url := fmt.Sprintf("ws://127.0.0.1:%d/ocpp/%s", ref.port, id)

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{v.Subprotocol()},
					CompressionMode: websocket.CompressionNoContextTakeover})
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				ext := resp.Header.Get("Sec-WebSocket-Extensions")
				_ = ws.Close(websocket.StatusNormalClosure, "")
				if !strings.Contains(ext, "permessage-deflate") {
					t.Fatalf("the reference CSMS did not accept compression: %q", ext)
				}

				d := newDispatcher(f, f.Answers.Station, v)
				c := newStation(t, v, url, client.WithCompression(), client.WithHandler(v, d))
				if err := c.Connect(ctx); err != nil {
					t.Fatalf("connect: %v", err)
				}
				for _, name := range f.samplesFrom("station") {
					s := f.Samples[name]
					if isSend(v, s.Action) {
						continue
					}
					if _, ok := knownIssues[a.name+"/"+v.String()+"/"+name]; ok {
						continue
					}
					var resp json.RawMessage
					err := c.Call(ctx, s.Action, s.Payload, &resp)
					t.Run(name, func(t *testing.T) { checkGoCall(t, f, s, resp, err) })
				}
			})
		}
	}
}

// TestReferenceStationCompression has each reference station that declares
// compression connect with --compression to a go-ocpp server: it offers
// permessage-deflate, and CALLs in both directions work over the
// compressed connection.
func TestReferenceStationCompression(t *testing.T) {
	for _, a := range adapters(t) {
		for _, v := range versions {
			t.Run(a.name+"/"+v.String(), func(t *testing.T) {
				a.role(t, v, "client")
				if !a.caps(t).Versions[v.String()].Features.Compression {
					t.Skipf("%s does not declare compression for OCPP %s", a.name, v)
				}
				a.skipKnownIssue(t, v, "compression-station")
				f := a.fixture(t, v)
				id, _ := f.station()
				d := newDispatcher(f, f.Answers.CSMS, v)
				srv, err := server.New(server.WithHandler(v, d), server.WithoutAuthentication())
				if err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				offered := ""
				ln, err := net.Listen("tcp", "0.0.0.0:0")
				if err != nil {
					t.Fatal(err)
				}
				hs := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					offered = r.Header.Get("Sec-WebSocket-Extensions")
					mu.Unlock()
					srv.ServeHTTP(w, r)
				})}
				go func() { _ = hs.Serve(ln) }()
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					_ = srv.Shutdown(ctx)
					_ = hs.Close()
				})
				startStation(t, a, v, ln.Addr().(*net.TCPAddr).Port, "--compression")
				sess := waitSession(t, srv, id)
				mu.Lock()
				got := offered
				mu.Unlock()
				if !strings.Contains(got, "permessage-deflate") {
					t.Fatalf("the reference station did not offer compression: %q", got)
				}
				var resp json.RawMessage
				if err := sess.Call(context.Background(), "ClearCache", json.RawMessage(`{}`), &resp); err != nil {
					t.Fatalf("call over the compressed connection: %v", err)
				}
			})
		}
	}
}
