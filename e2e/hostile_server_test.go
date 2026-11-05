// SPDX-License-Identifier: MIT

package e2e

// Hostile charging stations against the server: what a peer that does not
// follow OCPP-J or RFC 6455 can make the CSMS do. The other hardening tests
// are in exhaustion_test.go, security_test.go and protocol_test.go.

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/server"
)

// wsFrame is one WebSocket frame read by a hand-written peer.
type wsFrame struct {
	fin     bool
	opcode  byte
	payload []byte
}

// handPeer is a WebSocket station written by hand over TCP, for what a
// WebSocket library would not do: leave a close frame unanswered, say.
type handPeer struct {
	t    *testing.T
	conn net.Conn
	br   *bufio.Reader
}

// dialHand opens a WebSocket by hand to addr (host:port) at path, offering
// subprotocol, and returns once the server has answered 101.
func dialHand(t *testing.T, addr, path, subprotocol string) *handPeer {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	k := base64.StdEncoding.EncodeToString(key)
	_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Protocol: %s\r\n\r\n", path, addr, k, subprotocol)
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake answered %d", resp.StatusCode)
	}
	sum := sha1.Sum([]byte(k + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if resp.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatal("bad Sec-WebSocket-Accept")
	}
	return &handPeer{t: t, conn: conn, br: br}
}

// write sends one masked frame.
func (h *handPeer) write(fin bool, opcode byte, payload []byte) {
	h.t.Helper()
	if err := h.send(fin, opcode, payload); err != nil {
		h.t.Fatalf("write: %v", err)
	}
}

// send sends one masked frame; unlike write it may be used on a goroutine
// other than the test's.
func (h *handPeer) send(fin bool, opcode byte, payload []byte) error {
	_, err := h.conn.Write(frame(fin, opcode, payload))
	return err
}

// frame returns one masked frame, as a client sends it.
func frame(fin bool, opcode byte, payload []byte) []byte {
	b0 := opcode
	if fin {
		b0 |= 0x80
	}
	hdr := []byte{b0}
	switch n := len(payload); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xffff:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	mask := []byte{1, 2, 3, 4}
	hdr = append(hdr, mask...)
	masked := make([]byte, len(payload))
	for i, c := range payload {
		masked[i] = c ^ mask[i%4]
	}
	return append(hdr, masked...)
}

func (h *handPeer) text(s string) { h.t.Helper(); h.write(true, 0x1, []byte(s)) }

// read returns the next frame from the server (which does not mask).
func (h *handPeer) read() (wsFrame, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(h.br, hdr[:]); err != nil {
		return wsFrame{}, err
	}
	n := uint64(hdr[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(h.br, b[:]); err != nil {
			return wsFrame{}, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(h.br, b[:]); err != nil {
			return wsFrame{}, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > 1<<20 {
		return wsFrame{}, fmt.Errorf("frame of %d bytes", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(h.br, p); err != nil {
		return wsFrame{}, err
	}
	return wsFrame{fin: hdr[0]&0x80 != 0, opcode: hdr[0] & 0x0f, payload: p}, nil
}

// readUntil reads frames until one with opcode arrives.
func (h *handPeer) readUntil(opcode byte) wsFrame {
	h.t.Helper()
	for {
		f, err := h.read()
		if err != nil {
			h.t.Fatalf("waiting for opcode %d: %v", opcode, err)
		}
		if f.opcode == opcode {
			return f
		}
	}
}

// countingDispatcher answers every CALL with {} and counts them.
type countingDispatcher struct{ n atomic.Int64 }

func (d *countingDispatcher) Dispatch(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
	d.n.Add(1)
	return json.RawMessage(`{}`), nil
}

// An HTTP connection whose upgrade was refused (unknown station, wrong
// password, bad path or identity, not an upgrade) is not kept open as an
// idle keep-alive connection: otherwise a peer could hold any number of file
// descriptors with one refused request each, past WithMaxStations, which
// counts sessions only.
func TestRefusedConnectionNotKeptIdle(t *testing.T) {
	noLeak(t)
	srv, err := server.New(server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)),
		server.WithAuthenticator(server.BasicAuth(func(_ context.Context, id string) (string, bool) { return "pw", id == "CS1" || id == "a:b" })),
		server.WithHandshakeTimeout(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	addr := listening(t, srv)
	auth := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("CS1:pw")) + "\r\n"
	upgrade := "Connection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Protocol: ocpp2.0.1\r\n"
	for _, c := range []struct {
		path, header string
		status       int
	}{
		{"/ocpp/UNKNOWN", upgrade, http.StatusNotFound},
		{"/ocpp/", upgrade, http.StatusNotFound},
		{"/ocpp/CS1", upgrade, http.StatusUnauthorized},
		{"/ocpp/a:b", upgrade, http.StatusBadRequest},
		{"/ocpp/CS1", auth, http.StatusUpgradeRequired}, // authenticated, but not a WebSocket upgrade
	} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: x\r\n%s\r\n", c.path, c.header)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Fatalf("%s answered %d, want %d", c.path, resp.StatusCode, c.status)
		}
		// The server must close the connection; reading blocks until it does.
		if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
			t.Fatalf("%s (%d): refused connection kept open: %v", c.path, c.status, err)
		}
		_ = conn.Close()
	}
}

// expectAnswer reads frames until a text one arrives and checks that it
// answers the CALL id.
func (h *handPeer) expectAnswer(id string) {
	h.t.Helper()
	f := h.readUntil(0x1)
	if !strings.HasPrefix(string(f.payload), `[3,"`+id+`",`) {
		h.t.Fatalf("want the answer to %s, got %s", id, f.payload)
	}
}

// expectClose reads frames until the server's close frame and returns its
// status code; frames the server sends before it are skipped.
func (h *handPeer) expectClose() int {
	h.t.Helper()
	f := h.readUntil(0x8)
	if len(f.payload) < 2 {
		return 1005
	}
	return int(binary.BigEndian.Uint16(f.payload))
}

// WebSocket-level abuse below OCPP-J: frames a WebSocket library would not
// send. Each either leaves the session working or ends it with the status
// RFC 6455 gives, and none leaves a goroutine or a session behind.
func TestHostileWebSocketFrames(t *testing.T) {
	noLeak(t)
	d := &countingDispatcher{}
	srv, err := server.New(server.WithHandler(ocpp.V16, d), server.WithoutAuthentication(), server.WithMaxMessageSize(4<<10))
	if err != nil {
		t.Fatal(err)
	}
	addr := listening(t, srv)
	big := []byte(`[2,"big","Heartbeat",{"x":"` + strings.Repeat("x", 8<<10) + `"}]`)
	for _, c := range []struct {
		name  string
		abuse func(h *handPeer)
		close int // 0: the session goes on working
	}{
		{"ping flood", func(h *handPeer) {
			for range 500 {
				h.write(true, 0x9, []byte("p"))
			}
		}, 0},
		{"unsolicited pongs", func(h *handPeer) {
			for range 500 {
				h.write(true, 0xa, nil)
			}
		}, 0},
		{"fragments with control frames between", func(h *handPeer) {
			msg := []byte(`[2,"frag","Heartbeat",{}]`)
			h.write(false, 0x1, msg[:1])
			for i := 1; i < len(msg)-1; i++ {
				h.write(true, 0x9, nil)
				h.write(false, 0x0, msg[i:i+1])
			}
			h.write(true, 0x0, msg[len(msg)-1:])
			h.expectAnswer("frag")
		}, 0},
		{"binary message", func(h *handPeer) { h.write(true, 0x2, []byte(`[2,"b","Heartbeat",{}]`)) }, 1003},
		{"reserved opcode", func(h *handPeer) { h.write(true, 0x3, nil) }, 1002},
		{"continuation without a start", func(h *handPeer) { h.write(true, 0x0, []byte("x")) }, 1002},
		{"oversized control frame", func(h *handPeer) { h.write(true, 0x9, make([]byte, 126)) }, 1002},
		{"fragmented control frame", func(h *handPeer) { h.write(false, 0x9, nil) }, 1002},
		{"message over the limit in fragments", func(h *handPeer) {
			for i := 0; i < len(big)-512; i += 512 {
				op := byte(0x0)
				if i == 0 {
					op = 0x1
				}
				h.write(false, op, big[i:i+512])
			}
		}, 1009},
		{"invalid close code", func(h *handPeer) { h.write(true, 0x8, []byte{0x03, 0xec}) }, 1002}, // 1004 is reserved
	} {
		t.Run(c.name, func(t *testing.T) {
			id := strings.ReplaceAll(c.name, " ", "-")
			h := dialHand(t, addr, "/ocpp/"+id, "ocpp1.6")
			sess := waitStation(t, srv, id)
			c.abuse(h)
			if c.close == 0 {
				h.text(`[2,"after","Heartbeat",{}]`)
				h.expectAnswer("after")
				h.write(true, 0x8, []byte{0x03, 0xe8})
				h.expectClose()
			} else if got := h.expectClose(); got != c.close {
				t.Fatalf("closed with %d, want %d", got, c.close)
			}
			_ = h.conn.Close()
			select {
			case <-sess.Done():
			case <-time.After(15 * time.Second):
				t.Fatal("session did not end")
			}
		})
	}
	eventually(t, "no sessions left", func() bool { return len(srv.Stations()) == 0 })
}

// A panic in the onConnect function of WithSessionHandler, say on an identity
// it did not expect, does not leave the session registered once it ends:
// with RejectNew the station could otherwise never connect again.
func TestSessionHandlerPanic(t *testing.T) {
	noLeak(t)
	var panicked, disconnected atomic.Int64
	srv, err := server.New(server.WithHandler(ocpp.V201, ocpp.DispatcherFunc(echo)), server.WithoutAuthentication(),
		server.WithReplacePolicy(server.RejectNew),
		server.WithSessionHandler(func(s *server.Session) {
			if panicked.Add(1) == 1 {
				panic("unexpected station " + s.ID())
			}
		}, func(*server.Session, error) { disconnected.Add(1) }))
	if err != nil {
		t.Fatal(err)
	}
	addr := listening(t, srv)
	ws := rawStation(t, "ws://"+addr+"/ocpp/", "PANIC", "ocpp2.0.1", false)
	eventually(t, "onConnect ran", func() bool { return panicked.Load() == 1 })
	_ = ws.Close(1000, "")
	eventually(t, "the session gone", func() bool { _, ok := srv.Station("PANIC"); return !ok })
	eventually(t, "onDisconnect ran", func() bool { return disconnected.Load() == 1 })
	healthy(t, "ws://"+addr+"/ocpp/", "PANIC")
}
