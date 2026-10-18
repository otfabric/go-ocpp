//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The reference images this version of go-ocpp is qualified against:
// otfabric/ocpp-interop v0.0.1 (The Mobility House ocpp 2.1.0, ChargeTimeEU
// Java-OCA-OCPP v2.0), pinned by multi-arch index digest so that a run is
// reproducible. Moving to another release of ocpp-interop means changing
// these constants; the Makefile and .github/workflows/interop.yml follow.
const (
	interopRelease            = "v0.0.1"
	defaultMobilityHouseImage = "ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:6243da691f8682398a8cd420fb91b68a4d0f6aa42267961f8d04d097d172fd00"
	defaultChargeTimeImage    = "ghcr.io/otfabric/ocpp-interop-chargetime@sha256:beeb0e2b34d3c56b5835ed280b38e52ab4d1eba86251552e72e33bf12b7e3b2e"
)

// adapter is one reference implementation: a container image that follows
// the ocpp-interop container contract.
type adapter struct {
	name, image string
	pinned      bool // the image is the pinned default, not an override
}

// adapters returns the references to test against. The image of each can be
// overridden, for example to try a local build or a candidate:
//
//	OCPP_INTEROP_MOBILITYHOUSE_IMAGE, OCPP_INTEROP_CHARGETIME_IMAGE
//
// OCPP_INTEROP_ADAPTERS restricts the run to a comma-separated subset.
// Without Docker the tests skip, unless OCPP_INTEROP_REQUIRE=1, which
// 'make interop' and CI set: asked to test against the references, passing
// by skipping would say nothing.
func adapters(t testing.TB) []adapter {
	t.Helper()
	if _, _, err := docker("version", "--format", "{{.Server.Version}}"); err != nil {
		if os.Getenv("OCPP_INTEROP_REQUIRE") == "1" {
			t.Fatalf("Docker is not available: %v", err)
		}
		t.Skipf("Docker is not available: %v", err)
	}
	all := []adapter{
		image("mobilityhouse", "OCPP_INTEROP_MOBILITYHOUSE_IMAGE", defaultMobilityHouseImage),
		image("chargetime", "OCPP_INTEROP_CHARGETIME_IMAGE", defaultChargeTimeImage),
	}
	only := os.Getenv("OCPP_INTEROP_ADAPTERS")
	if only == "" {
		return all
	}
	var out []adapter
	for _, a := range all {
		for _, want := range strings.Split(only, ",") {
			if strings.TrimSpace(want) == a.name {
				out = append(out, a)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("OCPP_INTEROP_ADAPTERS=%q selects no adapter", only)
	}
	return out
}

func image(name, env, def string) adapter {
	if v := os.Getenv(env); v != "" {
		return adapter{name: name, image: v, pinned: v == def}
	}
	return adapter{name: name, image: def, pinned: true}
}

// capabilities is the part of the print-capabilities document the tests use.
type capabilities struct {
	AdapterVersion  string `json:"adapterVersion"`
	ContractVersion string `json:"contractVersion"`
	Upstream        struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"upstream"`
	Versions map[string]struct {
		Client   roleCaps `json:"client"`
		Server   roleCaps `json:"server"`
		Features struct {
			SecurityProfiles []int `json:"securityProfiles"`
		} `json:"features"`
	} `json:"versions"`
}

type roleCaps struct {
	Supported bool     `json:"supported"`
	Sends     []string `json:"sends"`
	Handles   []string `json:"handles"`
}

var capsCache sync.Map // image -> *capabilities

func (a adapter) caps(t testing.TB) *capabilities {
	t.Helper()
	if c, ok := capsCache.Load(a.image); ok {
		return c.(*capabilities)
	}
	out, stderr, err := docker("run", "--rm", a.image, "print-capabilities")
	if err != nil {
		t.Fatalf("print-capabilities of %s: %v\n%s", a.image, err, stderr)
	}
	c := &capabilities{}
	if err := json.Unmarshal([]byte(out), c); err != nil {
		t.Fatalf("capabilities of %s are not JSON: %v", a.image, err)
	}
	capsCache.Store(a.image, c)
	return c
}

// role returns what the reference declares for one role of a version, and
// skips the test if it does not support that role.
func (a adapter) role(t testing.TB, version, role string) roleCaps {
	t.Helper()
	v, ok := a.caps(t).Versions[version]
	r := v.Client
	if role == "server" {
		r = v.Server
	}
	if !ok || !r.Supported {
		t.Skipf("%s does not declare the %s role for OCPP %s", a.name, role, version)
	}
	return r
}

func (a adapter) securityProfile(t testing.TB, version string, profile int) {
	t.Helper()
	if !slices.Contains(a.caps(t).Versions[version].Features.SecurityProfiles, profile) {
		t.Skipf("%s does not declare security profile %d for OCPP %s", a.name, profile, version)
	}
}

// knownIssues are upstream defects of a reference, by adapter and fixture
// sample, with the evidence. Such a scenario is skipped with the link; it
// is not a pass.
var knownIssues = map[string]string{
	// ocpp.v16.call.SecurityEventNotification.tech_info has no default, so the
	// upstream cannot send the optional techInfo left out.
	"mobilityhouse/securityEventMinimal": "https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236",
}

var seq atomic.Int64

func docker(args ...string) (stdout, stderr string, err error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("docker", args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	return out.String(), errb.String(), err
}

// container is a running reference container: a CSMS (server) or a
// station (client run).
type container struct {
	adapter adapter
	name    string
	port    int // the CSMS's port on the host's loopback; 0 for a station
}

// startServer runs the reference CSMS of a for OCPP 1.6 with its baseline
// fixture, published on a free loopback port, and waits until it is ready.
func startServer(t testing.TB, a adapter, extra ...string) *container {
	t.Helper()
	port := freePort(t)
	c := &container{adapter: a, name: containerName(a), port: port}
	args := append([]string{"run", "-d", "--name", c.name, "-p", fmt.Sprintf("127.0.0.1:%d:9000", port),
		a.image, "server", "--version", "1.6"}, extra...)
	c.start(t, args)
	return c
}

// startStation runs the reference station of a with "client run", connected
// to a go-ocpp server on the host at port, and waits until it is connected.
// The container reaches the host through the gateway alias, on Docker
// Desktop and on Linux alike; the server must listen on all interfaces for
// that, because on Linux the alias is the bridge address.
func startStation(t testing.TB, a adapter, port int, extra ...string) *container {
	t.Helper()
	c := &container{adapter: a, name: containerName(a)}
	args := append([]string{"run", "-d", "--name", c.name, "--add-host=host.docker.internal:host-gateway",
		a.image, "client", "run", "--version", "1.6", "--url", fmt.Sprintf("ws://host.docker.internal:%d/ocpp", port)}, extra...)
	c.start(t, args)
	return c
}

func containerName(a adapter) string {
	return fmt.Sprintf("go-ocpp-interop-%s-%d-%d", a.name, os.Getpid(), seq.Add(1))
}

func (c *container) start(t testing.TB, args []string) {
	t.Helper()
	if _, stderr, err := docker(args...); err != nil {
		t.Fatalf("start %s: %v\n%s", c.adapter.name, err, stderr)
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, errOut, _ := docker("logs", "--tail", "80", c.name)
			t.Logf("%s log:\n%s%s", c.name, out, errOut)
		}
		_, _, _ = docker("rm", "-f", c.name)
	})
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, _, err := docker("exec", c.name, "test", "-f", "/run/ocpp-interop/ready"); err == nil {
			return
		}
		if state, _, _ := docker("inspect", "--format", "{{.State.Status}}", c.name); strings.TrimSpace(state) != "running" {
			out, errOut, _ := docker("logs", c.name)
			t.Fatalf("%s is %q, not running:\n%s%s", c.name, strings.TrimSpace(state), out, errOut)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s not ready after 90s", c.name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// result is the result document of "call" and "client <Action>".
type result struct {
	OK        bool            `json:"ok"`
	Response  json.RawMessage `json:"response"`
	Request   json.RawMessage `json:"request"`
	CallError *struct {
		Code        string `json:"code"`
		Description string `json:"description"`
	} `json:"callError"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	exit int
}

// call makes the reference send one CALL on its open connection: to the
// station (a CSMS container) or to the CSMS (a station container).
func (c *container) call(t testing.TB, station, action string, payload json.RawMessage) *result {
	t.Helper()
	args := []string{"exec", c.name, "ocpp-interop", "call"}
	if station != "" {
		args = append(args, "--station", station)
	}
	args = append(args, action, "--payload", string(payload), "--timeout-ms", "10000")
	stdout, stderr, err := docker(args...)
	r := &result{}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		r.exit = exitErr.ExitCode()
	default:
		t.Fatalf("call %s on %s: %v", action, c.name, err)
	}
	line := strings.TrimSpace(stdout)
	if line == "" || strings.Contains(line, "\n") {
		t.Fatalf("call %s on %s: want one result line, got %q (exit %d, stderr %q)", action, c.name, stdout, r.exit, stderr)
	}
	if err := json.Unmarshal([]byte(line), r); err != nil {
		t.Fatalf("call %s on %s: result is not JSON: %v\n%s", action, c.name, err, line)
	}
	return r
}

// stop stops the container with SIGTERM.
func (c *container) stop(t testing.TB) {
	t.Helper()
	if _, stderr, err := docker("stop", "-t", "15", c.name); err != nil {
		t.Fatalf("stop %s: %v\n%s", c.name, err, stderr)
	}
}

func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func eventually(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestReferences checks that the pinned images are the ocpp-interop release
// the documentation says this version is qualified against.
func TestReferences(t *testing.T) {
	for _, a := range adapters(t) {
		c := a.caps(t)
		t.Logf("%s: ocpp-interop %s (contract %s), %s %s, %s", a.name, c.AdapterVersion, c.ContractVersion,
			c.Upstream.Name, c.Upstream.Version, a.image)
		if a.pinned && c.AdapterVersion != interopRelease {
			t.Errorf("%s is pinned as ocpp-interop %s but reports %s", a.name, interopRelease, c.AdapterVersion)
		}
	}
}
