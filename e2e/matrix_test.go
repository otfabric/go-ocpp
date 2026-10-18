// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/internal/schematest"
	"github.com/otfabric/go-ocpp/ocpp16"
	"github.com/otfabric/go-ocpp/ocpp201"
	"github.com/otfabric/go-ocpp/ocpp21"
	"github.com/otfabric/go-ocpp/server"
)

// versionPackage is what the matrix needs from a version package.
type versionPackage struct {
	version          ocpp.Version
	dir              string // the package directory, holding coverage.json
	validateRequest  func(action string, payload json.RawMessage) error
	validateResponse func(action string, payload json.RawMessage) error
}

var versionPackages = []versionPackage{
	{ocpp.V16, "ocpp16", ocpp16.ValidateRequest, ocpp16.ValidateResponse},
	{ocpp.V201, "ocpp201", ocpp201.ValidateRequest, ocpp201.ValidateResponse},
	{ocpp.V21, "ocpp21", ocpp21.ValidateRequest, ocpp21.ValidateResponse},
}

// manifestAction is one entry of a coverage.json.
type manifestAction struct {
	Action         string `json:"action"`
	From           string `json:"from"`
	Send           bool   `json:"send"`
	RequestSchema  string `json:"requestSchema"`
	ResponseSchema string `json:"responseSchema"`
}

func manifestOf(t *testing.T, dir string) []manifestAction {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", dir, "coverage.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Actions []manifestAction `json:"actions"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m.Actions
}

// instances holds, per action, a request and a response with every property
// (full) and with the required ones only.
type instances struct {
	req, resp [2]json.RawMessage // [full, minimal]
}

func instancesOf(t *testing.T, actions []manifestAction) map[string]instances {
	t.Helper()
	out := map[string]instances{}
	for _, a := range actions {
		var in instances
		for i, full := range []bool{true, false} {
			in.req[i] = schematest.Instance(t, filepath.Join("..", a.RequestSchema), full)
			if !a.Send {
				in.resp[i] = schematest.Instance(t, filepath.Join("..", a.ResponseSchema), full)
			}
		}
		out[a.Action] = in
	}
	return out
}

// conformingPeer is a Dispatcher that answers every valid request of a
// version with a schema-valid response, and records the SENDs it receives.
// Requests are validated by the version package, as a typed dispatcher
// would; an invalid one is answered with its CALLERROR.
type conformingPeer struct {
	pkg   versionPackage
	inst  map[string]instances
	full  bool
	mu    sync.Mutex
	calls map[string]int
	sends map[string]int
}

func newConformingPeer(pkg versionPackage, inst map[string]instances, full bool) *conformingPeer {
	return &conformingPeer{pkg: pkg, inst: inst, full: full, calls: map[string]int{}, sends: map[string]int{}}
}

func (c *conformingPeer) index() int {
	if c.full {
		return 0
	}
	return 1
}

func (c *conformingPeer) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	if err := c.pkg.validateRequest(action, payload); err != nil {
		var ve *ocpp.ValidationError
		if errors.As(err, &ve) {
			return nil, ve.CallError()
		}
		return nil, err
	}
	c.mu.Lock()
	c.calls[action]++
	c.mu.Unlock()
	return c.inst[action].resp[c.index()], nil
}

func (c *conformingPeer) DispatchSend(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) error {
	if err := c.pkg.validateRequest(action, payload); err != nil {
		return err
	}
	c.mu.Lock()
	c.sends[action]++
	c.mu.Unlock()
	return nil
}

func (c *conformingPeer) count(action string, send bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if send {
		return c.sends[action]
	}
	return c.calls[action]
}

// TestMatrix sends every action of every version between a go-ocpp
// Charging Station and a go-ocpp CSMS over a real WebSocket connection, in
// the direction the specification gives it (both for DataTransfer), with a
// request and a response that carry every property and with ones that carry
// the required properties only. Each payload is validated on arrival by
// the receiving side's version package. Compression is on for the 2.x
// versions, as a CSMS must support it.
// transport is how a matrix run connects: a security profile (0 none, 1
// Basic over ws, 2 TLS and Basic, 3 mutual TLS) and compression.
type transport struct {
	name     string
	profile  int
	compress bool
}

// The transports every action of every version is exchanged over: the
// payloads with every property over each of them, and with the required
// properties only over plain WebSocket.
var transports = []transport{
	{"plain", 0, false},
	{"compressed", 0, true},
	{"profile1", 1, false},
	{"profile2", 2, true},
	{"profile3", 3, true},
	{"profile3-uncompressed", 3, false},
}

func TestMatrix(t *testing.T) {
	p := newPKI(t)
	for _, pkg := range versionPackages {
		for _, tr := range transports {
			for _, full := range []bool{true, false} {
				if !full && tr.name != "plain" {
					continue
				}
				name := pkg.version.String() + "/" + tr.name + "/minimal"
				if full {
					name = pkg.version.String() + "/" + tr.name + "/full"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					runMatrix(t, pkg, full, tr, p)
				})
			}
		}
	}
}

// matrixPassword is the station password of profiles 1 and 2.
const matrixPassword = "matrix-password-0001"

func runMatrix(t *testing.T, pkg versionPackage, full bool, tr transport, p *pki) {
	actions := manifestOf(t, pkg.dir)
	inst := instancesOf(t, actions)
	csmsSide := newConformingPeer(pkg, inst, full)
	stationSide := newConformingPeer(pkg, inst, full)
	id := "M" + strings.ReplaceAll(pkg.dir+"-"+tr.name, "-", "")
	if !full {
		id += "min"
	}
	serverOpts := []server.Option{server.WithHandler(pkg.version, csmsSide)}
	opts := []client.Option{client.WithVersions(pkg.version), client.WithHandler(pkg.version, stationSide)}
	var tlsCfg *tls.Config
	switch tr.profile {
	case 0:
		serverOpts = append(serverOpts, server.WithoutAuthentication())
		opts = append(opts, client.WithInsecureTransport())
	case 1, 2:
		serverOpts = append(serverOpts, server.WithAuthenticator(server.BasicAuth(func(ctx context.Context, station string) (string, bool) {
			return matrixPassword, station == id
		})))
		opts = append(opts, client.WithBasicAuth(id, matrixPassword))
		if tr.profile == 1 {
			opts = append(opts, client.WithInsecureTransport())
		}
	case 3:
		serverOpts = append(serverOpts, server.WithAuthenticator(server.CertificateAuth()))
		opts = append(opts, client.WithTLS(&tls.Config{RootCAs: p.caPool, Certificates: []tls.Certificate{p.issue(t, id, false)}}))
	}
	if tr.profile >= 2 {
		tlsCfg = &tls.Config{Certificates: []tls.Certificate{p.issue(t, "csms", true)}, ClientCAs: p.caPool}
		if tr.profile == 3 {
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
		}
		if tr.profile == 2 {
			opts = append(opts, client.WithTLS(&tls.Config{RootCAs: p.caPool}))
		}
	}
	if tr.compress {
		opts = append(opts, client.WithCompression())
	}
	srv, base := csms(t, tlsCfg, serverOpts...)
	c := station(t, base+id, opts...)
	connect(t, c)
	session := waitStation(t, srv, id)
	i := csmsSide.index()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, a := range actions {
		type sender struct {
			name     string
			caller   ocpp.Caller
			sender   ocpp.Sender
			receiver *conformingPeer
		}
		var senders []sender
		if a.From != "CSMS" {
			senders = append(senders, sender{"station", c, c, csmsSide})
		}
		if a.From != "ChargingStation" {
			senders = append(senders, sender{"csms", session, session, stationSide})
		}
		for _, s := range senders {
			before := s.receiver.count(a.Action, a.Send)
			if a.Send {
				if err := s.sender.Send(ctx, a.Action, inst[a.Action].req[i]); err != nil {
					t.Fatalf("%s from the %s: %v", a.Action, s.name, err)
				}
				eventually(t, a.Action+" SEND received", func() bool { return s.receiver.count(a.Action, true) == before+1 })
				continue
			}
			var resp json.RawMessage
			if err := s.caller.Call(ctx, a.Action, inst[a.Action].req[i], &resp); err != nil {
				t.Fatalf("%s from the %s: %v", a.Action, s.name, err)
			}
			if err := pkg.validateResponse(a.Action, resp); err != nil {
				t.Fatalf("%s from the %s: response: %v", a.Action, s.name, err)
			}
			if n := s.receiver.count(a.Action, false); n != before+1 {
				t.Fatalf("%s from the %s: handled %d times, want %d", a.Action, s.name, n, before+1)
			}
		}
	}
}

// TestMatrixCoverage checks the matrix sees every action of each version
// package, so that a manifest that went missing cannot pass it silently.
func TestMatrixCoverage(t *testing.T) {
	want := map[ocpp.Version]int{ocpp.V16: len(ocpp16.Actions), ocpp.V201: len(ocpp201.Actions), ocpp.V21: len(ocpp21.Actions)}
	for _, pkg := range versionPackages {
		if n := len(manifestOf(t, pkg.dir)); n != want[pkg.version] || n == 0 {
			t.Errorf("OCPP %s: the manifest lists %d actions, the package %d", pkg.version, n, want[pkg.version])
		}
	}
}
