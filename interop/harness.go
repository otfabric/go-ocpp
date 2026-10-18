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

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocpp21"
)

// The reference images this version of go-ocpp is qualified against:
// otfabric/ocpp-interop v0.3.1 (The Mobility House ocpp 2.1.0, ChargeTimeEU
// Java-OCA-OCPP v2.0, lorenzodonini/ocpp-go v0.19.0: OCPP 1.6J and 2.0.1 in
// all three, 2.1 in The Mobility House; EVerest libocpp of everest-core
// 2026.02.1, a station for 1.6J, 2.0.1 and 2.1), pinned by multi-arch index
// digest
// so that a run is reproducible. Moving to another release of ocpp-interop
// means changing these constants; the Makefile and
// .github/workflows/interop.yml follow.
const (
	interopRelease            = "v0.3.1"
	defaultMobilityHouseImage = "ghcr.io/otfabric/ocpp-interop-mobilityhouse@sha256:e82896331789e7a1d5ac678994e8a0b5ed29e48ba16a8beb994c16fb7a2f5dd6"
	defaultChargeTimeImage    = "ghcr.io/otfabric/ocpp-interop-chargetime@sha256:a46c0db29e154e96f8779aebb585fd7c10db1b23be9401442881be21504d2247"
	defaultLorenzodoniniImage = "ghcr.io/otfabric/ocpp-interop-lorenzodonini@sha256:8328f5bf6e66281bec10ff35b812eb957c59fd4778f9984c09c28248a0aa9d91"
	defaultEverestImage       = "ghcr.io/otfabric/ocpp-interop-everest@sha256:990c3ea95c7caa98449663bb7f48d5be7df0144241750e72adbbec2022c3321c"
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
//	OCPP_INTEROP_MOBILITYHOUSE_IMAGE, OCPP_INTEROP_CHARGETIME_IMAGE,
//	OCPP_INTEROP_LORENZODONINI_IMAGE
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
	var all []adapter
	for _, a := range []adapter{
		image("mobilityhouse", "OCPP_INTEROP_MOBILITYHOUSE_IMAGE", defaultMobilityHouseImage),
		image("chargetime", "OCPP_INTEROP_CHARGETIME_IMAGE", defaultChargeTimeImage),
		image("lorenzodonini", "OCPP_INTEROP_LORENZODONINI_IMAGE", defaultLorenzodoniniImage),
		image("everest", "OCPP_INTEROP_EVEREST_IMAGE", defaultEverestImage),
	} {
		if a.image != "" {
			all = append(all, a)
		}
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
			Compression      bool  `json:"compression"`
			Send             bool  `json:"send"`            // OCPP 2.1 SEND (contract 0.4)
			CallResultError  bool  `json:"callResultError"` // OCPP 2.1 CALLRESULTERROR (contract 0.4)
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
func (a adapter) role(t testing.TB, v ocpp.Version, role string) roleCaps {
	t.Helper()
	version := v.String()
	vc, ok := a.caps(t).Versions[version]
	r := vc.Client
	if role == "server" {
		r = vc.Server
	}
	if !ok || !r.Supported {
		t.Skipf("%s does not declare the %s role for OCPP %s", a.name, role, version)
	}
	return r
}

func (a adapter) securityProfile(t testing.TB, v ocpp.Version, profile int) {
	t.Helper()
	if !slices.Contains(a.caps(t).Versions[v.String()].Features.SecurityProfiles, profile) {
		t.Skipf("%s does not declare security profile %d for OCPP %s", a.name, profile, v)
	}
}

// versions are the OCPP versions the interop suite runs. A reference that
// does not declare a version is skipped for it.
var versions = []ocpp.Version{ocpp.V16, ocpp.V201, ocpp.V21}

// skipKnownIssue skips a scenario that hits a known upstream defect of the
// reference, with the evidence. It is not a pass.
func (a adapter) skipKnownIssue(t testing.TB, v ocpp.Version, scenario string) {
	t.Helper()
	if evidence, ok := knownIssues[a.name+"/"+v.String()+"/"+scenario]; ok {
		t.Skipf("known upstream defect of %s: %s", a.name, evidence)
	}
}

// isSend reports whether action is sent as a SEND (OCPP 2.1).
func isSend(v ocpp.Version, action string) bool {
	if v != ocpp.V21 {
		return false
	}
	for _, a := range ocpp21.Actions {
		if a.Name == action {
			return a.Send
		}
	}
	return false
}

// knownIssues are upstream defects of a reference, by adapter and fixture
// sample, with the evidence. Such a scenario is skipped with the link; it
// is not a pass.
var knownIssues = map[string]string{
	// ocpp.v16.call.SecurityEventNotification.tech_info has no default, so the
	// upstream cannot send the optional techInfo left out.
	// ocpp-go v0.19.0: ResetResponse.StatusInfo has no omitempty, so the
	// station answers "statusInfo": null, which the schema does not allow
	// and go-ocpp's validation refuses.
	"lorenzodonini/2.0.1/resetImmediate": "https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/reset.go#L61-L64",
	// ocpp-go v0.19.0: NotifyReportRequest.Tbc is a bool with omitempty, so
	// "tbc": false is dropped from the request.
	"lorenzodonini/2.0.1/notifyReport":       "https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/notify_report.go#L98-L104",
	"mobilityhouse/1.6/securityEventMinimal": "https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/v16/call.py#L232-L236",
	// mobilityhouse 2.1.0: ChargePoint._raise_key_error knows 1.6, 2.0 and
	// 2.0.1 only, so a 2.1 CALL without a handler is never answered (fixed
	// on master in ae716a1, #744, not released). The fixtures answer every
	// action; only an unknown action is left unanswered.
	"mobilityhouse/2.1/unknown-action": mobilityhouse21NoAnswer,
	// EVerest libocpp (everest-core 2026.02.1): ocpp::v2::MessageType has no
	// ReportDERControl, so libocpp's message queue cannot send it, although
	// the adapter declares it (libocpp has its request and response types).
	"everest/2.1/reportDERControl": "https://github.com/EVerest/everest-core/blob/b61bb12b8cb89ee9d0fab037b05c3bb8cea9b13e/lib/everest/ocpp/include/ocpp/v2/types.hpp#L15-L195",
	// ocpp-go v0.19.0: GetCompositeScheduleResponse does not have the OCA
	// structure: a top-level evseId the schema does not allow, no
	// statusInfo, and a schedule of {startDateTime, chargingSchedule}
	// instead of CompositeScheduleType, sent empty.
	"lorenzodonini/2.0.1/getCompositeSchedule": "https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/smartcharging/get_composite_schedule.go#L31-L50",
	// ocpp-go v0.19.0: SetNetworkProfileResponse.StatusInfo has no
	// omitempty, so the station answers "statusInfo": null.
	"lorenzodonini/2.0.1/setNetworkProfile": "https://github.com/lorenzodonini/ocpp-go/blob/a1eec917af884db0585752f909a61893aa667480/ocpp2.0.1/provisioning/set_network_profile.go#L170-L172",
	// Java-OCA-OCPP v2.0 with Java-WebSocket 1.6.0, OCPP 2.0.1: the CSMS
	// announces server_no_context_takeover but keeps its compression
	// context, so a station cannot decompress the second message.
	"chargetime/2.0.1/compression-csms": "https://github.com/TooTallNate/Java-WebSocket/blob/da3cf2a777aed862f2f5b5cf060cae7969958667/src/main/java/org/java_websocket/extensions/permessage_deflate/PerMessageDeflateExtension.java#L335-L340",
	// The station offers client_no_context_takeover but keeps its context.
	"chargetime/2.0.1/compression-station": "https://github.com/TooTallNate/Java-WebSocket/blob/da3cf2a777aed862f2f5b5cf060cae7969958667/src/main/java/org/java_websocket/extensions/permessage_deflate/PerMessageDeflateExtension.java#L326-L333",
}

const mobilityhouse21NoAnswer = "https://github.com/mobilityhouse/ocpp/blob/91f4f89140a2198dc5a300cfea88d947ac91ae28/ocpp/charge_point.py#L161-L193"

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

// startServer runs the reference CSMS of a for version v with its baseline
// fixture, published on a free loopback port, and waits until it is ready.
func startServer(t testing.TB, a adapter, v ocpp.Version, extra ...string) *container {
	t.Helper()
	return startServerWith(t, a, v, nil, extra...)
}

// startServerWith is startServer with extra docker run options (a volume).
func startServerWith(t testing.TB, a adapter, v ocpp.Version, runOpts []string, extra ...string) *container {
	t.Helper()
	port := freePort(t)
	c := &container{adapter: a, name: containerName(a), port: port}
	args := append([]string{"run", "-d", "--name", c.name, "-p", fmt.Sprintf("127.0.0.1:%d:9000", port)}, runOpts...)
	args = append(append(args, a.image, "server", "--version", v.String()), extra...)
	c.start(t, args)
	return c
}

// startStation runs the reference station of a with "client run", connected
// to a go-ocpp server on the host at port, and waits until it is connected.
// The container reaches the host through the gateway alias, on Docker
// Desktop and on Linux alike; the server must listen on all interfaces for
// that, because on Linux the alias is the bridge address.
func startStation(t testing.TB, a adapter, v ocpp.Version, port int, extra ...string) *container {
	t.Helper()
	return startStationWith(t, a, v, "ws", port, nil, extra...)
}

// startStationWith is startStation with a URL scheme (ws or wss) and extra
// docker run options (a volume).
func startStationWith(t testing.TB, a adapter, v ocpp.Version, scheme string, port int, runOpts []string, extra ...string) *container {
	t.Helper()
	c := &container{adapter: a, name: containerName(a)}
	args := append([]string{"run", "-d", "--name", c.name, "--add-host=host.docker.internal:host-gateway"}, runOpts...)
	args = append(append(args, a.image, "client", "run", "--version", v.String(),
		"--url", fmt.Sprintf("%s://host.docker.internal:%d/ocpp", scheme, port)), extra...)
	c.start(t, args)
	return c
}

// runOnce runs "client <action>" of a, which connects, sends one CALL and
// exits, and returns its exit code: for connections that must fail.
func runOnce(t testing.TB, a adapter, v ocpp.Version, url string, runOpts []string, extra ...string) int {
	t.Helper()
	args := append([]string{"run", "--rm", "--add-host=host.docker.internal:host-gateway"}, runOpts...)
	args = append(append(args, a.image, "client", "Heartbeat", "--version", v.String(), "--url", url,
		"--connect-timeout-ms", "5000"), extra...)
	_, _, err := docker(args...)
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return exitErr.ExitCode()
	}
	t.Fatalf("run %s: %v", a.name, err)
	return -1
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
