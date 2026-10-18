// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// fuzzDispatcher answers CALLs by action: "Err" with a CALLERROR, "Panic"
// by panicking, "Bad" with a payload that is not an object, anything else
// by echoing the payload. It accepts every SEND.
type fuzzDispatcher struct{}

func (fuzzDispatcher) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	switch action {
	case "Err":
		return nil, ocpp.NewCallError(ocpp.NotSupported, strings.Repeat("d", 300))
	case "Panic":
		panic("fuzz")
	case "Bad":
		return json.RawMessage(`[1]`), nil
	}
	return payload, nil
}

func (fuzzDispatcher) DispatchSend(context.Context, ocpp.Peer, string, json.RawMessage) error {
	return nil
}

// FuzzEndpoint feeds a sequence of arbitrary frames (one per line) to an
// endpoint of any version, then a CALL that marks the end. The endpoint
// must not panic or stall: the marker is answered, and everything written
// is a well-formed CALLRESULT or CALLERROR of the version, never anything
// a SEND or a response would call for.
func FuzzEndpoint(f *testing.F) {
	for _, s := range []string{
		"[2,\"1\",\"Heartbeat\",{}]",
		"[2,\"1\",\"Err\",{}]\n[2,\"2\",\"Panic\",{}]\n[2,\"3\",\"Bad\",{}]",
		"[3,\"x\",{}]\n[4,\"y\",\"GenericError\",\"\",{}]\n[5,\"z\",\"GenericError\",\"\",{}]",
		"[6,\"s\",\"NotifyPeriodicEventStream\",{}]\n[9,\"u\",{}]",
		"not json\n[]\n[2]\n[2,1,\"x\",{}]\n[2,\"1\",\"x\",[]]\n{\"a\":1}",
		"[2,\"" + strings.Repeat("i", 40) + "\",\"Heartbeat\",{}]",
		"[2,\"1\",\"Heartbeat\",{}]\n[2,\"1\",\"Heartbeat\",{}]",
	} {
		for v := range 3 {
			f.Add([]byte(s), uint8(v))
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, vb uint8) {
		v := ocpp.Version(vb%3 + 1)
		conn := newFakeConn()
		e := newEndpoint(conn, v, ocpp.CSMS, fuzzDispatcher{}, nil)
		defer func() { _ = e.Close() }()
		lines := bytes.Split(data, []byte("\n"))
		if len(lines) > 15 {
			lines = lines[:15]
		}
		for _, l := range lines {
			conn.reads <- l
		}
		conn.reads <- []byte(`[2,"end-of-fuzz","Heartbeat",{}]`)

		deadline := time.Now().Add(5 * time.Second)
		for !answered(conn, "end-of-fuzz") {
			if e.Err() != nil {
				t.Fatalf("endpoint ended: %v", e.Err())
			}
			if time.Now().After(deadline) {
				t.Fatal("the end marker was not answered")
			}
			time.Sleep(100 * time.Microsecond)
		}
		conn.mu.Lock()
		defer conn.mu.Unlock()
		for _, w := range conn.written {
			fr, err := ocppj.Decode(w, v, ocppj.Limits{MaxFrameSize: len(w) + 1})
			if err != nil {
				t.Fatalf("wrote a malformed frame %q: %v", w, err)
			}
			if fr.Type != ocppj.CallResult && fr.Type != ocppj.CallError {
				t.Fatalf("wrote %s in reply to incoming frames: %q", fr.Type, w)
			}
			if fr.Type == ocppj.CallError && !ocppj.ValidErrorCode(v, fr.ErrorCode) {
				t.Fatalf("error code %q is not one of OCPP %s: %q", fr.ErrorCode, v, w)
			}
		}
	})
}

func answered(c *fakeConn, id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.written {
		if bytes.Contains(w, []byte(`"`+id+`"`)) {
			return true
		}
	}
	return false
}
