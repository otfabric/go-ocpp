// SPDX-License-Identifier: MIT

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// A frame whose text is not well-formed (ocppj.Decode) is a malformed frame:
// a CALL is answered with the version's CALLERROR for one, under its message
// ID when that can be read, no handler runs, it is reported to
// OnProtocolError, and the connection goes on. The transport in these tests
// is the in-memory pipe, which passes any bytes; a WebSocket connection
// closes on a message that is not UTF-8 before the engine sees it
// (internal/transport).
func TestTextNotWellFormedIsMalformed(t *testing.T) {
	checkNoLeak(t)
	for _, tt := range []struct {
		v    ocpp.Version
		code ocpp.ErrorCode
	}{{ocpp.V16, ocpp.FormationViolation}, {ocpp.V201, ocpp.RpcFrameworkError}, {ocpp.V21, ocpp.RpcFrameworkError}} {
		var handled atomic.Int64
		m := &countingMetrics{}
		d := ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
			handled.Add(1)
			return json.RawMessage(`{}`), nil
		})
		_, raw := rawPeer(t, tt.v, d, func(c *Config) { c.Metrics = m })
		frames := []string{
			`[2,"a","Authorize",{"idTag":"A\ud83dB"}]`,
			`[2,"b","Authorize",{"idTag":"A\ude00B"}]`,
			"[2,\"c\",\"Authorize\",{\"idTag\":\"A\xffB\"}]",
			`[2,"d","Author\ud83dize",{}]`,
			`[2,"e","Authorize",{"id\udc00Tag":"A"}]`,
		}
		for i, frame := range frames {
			writeRaw(t, raw, frame)
			f := readFrame(t, raw, tt.v)
			if want := string(rune('a' + i)); f.Type != ocppj.CallError || f.ID != want || f.ErrorCode != tt.code {
				t.Fatalf("%s: %s answered with %+v, want CALLERROR %s for %q", tt.v, frame, f, tt.code, want)
			}
		}
		// The message ID cannot be read: answered under "-1" on 2.x, dropped
		// on 1.6, which defines no ID for that.
		writeRaw(t, raw, `[2,"i\ud83d","Authorize",{}]`)
		if tt.v != ocpp.V16 {
			if f := readFrame(t, raw, tt.v); f.Type != ocppj.CallError || f.ID != "-1" || f.ErrorCode != tt.code {
				t.Fatalf("%s: got %+v, want CALLERROR %s under -1", tt.v, f, tt.code)
			}
		}
		// A response with such text is dropped, like any malformed response.
		writeRaw(t, raw, `[3,"r",{"k":"\ud83d"}]`)
		// Well-formed text reaches the handler, and the connection is up.
		writeRaw(t, raw, `[2,"ok","Authorize",{"idTag":"A\ud83d\ude00\ufffdB"}]`)
		if f := readFrame(t, raw, tt.v); f.Type != ocppj.CallResult || f.ID != "ok" {
			t.Fatalf("%s: got %+v, want the answer to ok", tt.v, f)
		}
		if n := handled.Load(); n != 1 {
			t.Errorf("%s: the handler ran %d times, want once", tt.v, n)
		}
		if n := m.protocolErrors.Load(); n != int64(len(frames))+2 {
			t.Errorf("%s: %d protocol errors, want %d", tt.v, n, len(frames)+2)
		}
	}
}

// Frames whose text is not well-formed count for MaxBadMessages.
func TestTextNotWellFormedCounts(t *testing.T) {
	checkNoLeak(t)
	conn := newFakeConn()
	e := fakeEndpoint(t, conn, func(c *Config) { c.MaxBadMessages = 2 })
	conn.reads <- []byte(`[2,"a","Authorize",{"idTag":"\ud83d"}]`)
	conn.reads <- []byte("[3,\"b\",{\"k\":\"\xff\"}]")
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("not closed after 2 frames of text that is not well-formed")
	}
	if !errors.Is(e.Err(), ocpp.ErrConnectionLost) || !errors.Is(e.Err(), ocpp.ErrProtocol) {
		t.Fatalf("err %v", e.Err())
	}
}
