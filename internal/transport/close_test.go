// SPDX-License-Identifier: MIT

package transport

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp/internal/leakcheck"
)

// stalledHeader is the header of a text frame that is never finished.
// masked: as a client sends it.
func stalledHeader(masked bool) []byte {
	h := []byte{0x81, 0x7F}
	if masked {
		h[1] |= 0x80
	}
	h = binary.BigEndian.AppendUint64(h, 1<<40)
	if masked {
		h = append(h, 0, 0, 0, 0)
	}
	return h
}

// readFrame reads one frame's opcode and payload (unmasking it).
func readFrame(br *bufio.Reader) (byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		return 0, nil, err
	}
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(br, b[:]); err != nil {
			return 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	var mask [4]byte
	if h[1]&0x80 != 0 {
		if _, err := io.ReadFull(br, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(br, p); err != nil {
		return 0, nil, err
	}
	for i := range p {
		p[i] ^= mask[i%4]
	}
	return h[0] & 0x0F, p, nil
}

// awaitClose reads frames until a close frame and returns its code.
func awaitClose(t *testing.T, br *bufio.Reader) int {
	t.Helper()
	for {
		op, p, err := readFrame(br)
		if err != nil {
			t.Fatalf("waiting for the close frame: %v", err)
		}
		if op == 0x8 && len(p) >= 2 {
			return int(binary.BigEndian.Uint16(p))
		}
	}
}

// expectEOF fails unless the other side closes conn.
func expectEOF(t *testing.T, conn net.Conn, br *bufio.Reader) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.Copy(io.Discard, br); err != nil {
		t.Fatalf("the connection was not closed: %v", err)
	}
}

// within fails when f does not return within d.
func within(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); f() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// shortClose shortens the closing-handshake bound for the test.
func shortClose(t *testing.T) {
	t.Helper()
	t.Cleanup(SetCloseTimeout(200 * time.Millisecond))
}

// upgrade sends a WebSocket handshake for subprotocol over a new TCP
// connection to addr, with the header fields of extra replacing or adding
// to the usual ones, and returns the response.
func upgrade(t *testing.T, addr, subprotocol string, extra map[string]string) (net.Conn, *bufio.Reader, *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	h := map[string]string{"Host": addr, "Connection": "Upgrade", "Upgrade": "websocket", "Sec-WebSocket-Version": "13",
		"Sec-WebSocket-Key": "dGhlIHNhbXBsZSBub25jZQ==", "Sec-WebSocket-Protocol": subprotocol}
	maps.Copy(h, extra)
	req := "GET /ocpp/CS1 HTTP/1.1\r\n"
	for k, v := range h {
		req += k + ": " + v + "\r\n"
	}
	_, _ = io.WriteString(conn, req+"\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, br, resp
}

// acceptOne serves Accept with o through wrap, hands the connection (or
// Accept's error) to the test, and waits for the test to finish with it.
func acceptOne(t *testing.T, o AcceptOptions, wrap func(http.ResponseWriter) http.ResponseWriter) (string, <-chan Conn, <-chan error) {
	t.Helper()
	conns, errs := make(chan Conn, 1), make(chan error, 1)
	done := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wrap != nil {
			w = wrap(w)
		}
		c, err := Accept(w, r, o)
		if err != nil {
			errs <- err
			return
		}
		conns <- c
		<-done
	}))
	t.Cleanup(func() { close(done); hs.Close() })
	return hs.Listener.Addr().String(), conns, errs
}

// unwrapWriter hides the Hijacker of the writer underneath, which only
// Unwrap reaches, as middleware writers do.
type unwrapWriter struct{ w http.ResponseWriter }

func (u unwrapWriter) Header() http.Header         { return u.w.Header() }
func (u unwrapWriter) Write(p []byte) (int, error) { return u.w.Write(p) }
func (u unwrapWriter) WriteHeader(code int)        { u.w.WriteHeader(code) }
func (u unwrapWriter) Unwrap() http.ResponseWriter { return u.w }

// readWatch counts what the server reads from the connection it hijacks, so
// that a test can wait until the server is reading again after a number of
// bytes reached it.
type readWatch struct {
	delivered atomic.Int64 // bytes that Read has returned
	reading   atomic.Int64 // Read calls in progress
}

// wrap returns a writer whose Hijack hands out the watched connection.
func (r *readWatch) wrap(w http.ResponseWriter) http.ResponseWriter { return watchWriter{w, r} }

// await waits until n bytes have been read and another Read is in progress.
func (r *readWatch) await(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for r.delivered.Load() < n || r.reading.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the server read %d of %d bytes, %d reads in progress", r.delivered.Load(), n, r.reading.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

type watchWriter struct {
	http.ResponseWriter
	r *readWatch
}

func (w watchWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, brw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err != nil {
		return nil, nil, err
	}
	// coder/websocket reads from the connection it is given, after what
	// brw has buffered.
	return &watchedConn{Conn: c, r: w.r}, brw, nil
}

type watchedConn struct {
	net.Conn
	r *readWatch
}

func (c *watchedConn) Read(p []byte) (int, error) {
	c.r.reading.Add(1)
	n, err := c.Conn.Read(p)
	c.r.reading.Add(-1) // before delivered: a read in progress after n bytes is a later one
	c.r.delivered.Add(int64(n))
	return n, err
}

// A peer that answers the server's close frame with a frame it never
// finishes holds no closing handshake past the bound: Close returns and the connection is closed.
func TestAcceptCloseBounded(t *testing.T) {
	leakcheck.Check(t)
	shortClose(t)
	for name, wrap := range map[string]func(http.ResponseWriter) http.ResponseWriter{
		"hijacker":        nil,
		"through Unwrap":  func(w http.ResponseWriter) http.ResponseWriter { return unwrapWriter{w} },
		"WriteHeaderNow":  func(w http.ResponseWriter) http.ResponseWriter { return &nowWriter{unwrapWriter: unwrapWriter{w}} },
		"Close twice":     nil,
		"Abort in Close":  nil,
		"binary message":  nil,
		"subprotocol not": nil,
	} {
		t.Run(name, func(t *testing.T) {
			o := AcceptOptions{Subprotocol: "ocpp1.6"}
			offer := "ocpp1.6"
			if name == "subprotocol not" {
				offer = "ocpp2.0.1"
			}
			var reads *readWatch
			if name == "Abort in Close" {
				reads = &readWatch{}
				wrap = reads.wrap
			}
			addr, conns, errs := acceptOne(t, o, wrap)
			conn, br, resp := upgrade(t, addr, offer, nil)
			if resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("handshake answered %d", resp.StatusCode)
			}
			if name == "subprotocol not" {
				if code := awaitClose(t, br); code != StatusPolicy {
					t.Fatalf("close code %d", code)
				}
				_, _ = conn.Write(stalledHeader(true))
				within(t, 5*time.Second, "Accept", func() {
					if err := <-errs; err == nil {
						t.Error("subprotocol accepted")
					}
				})
				expectEOF(t, conn, br)
				return
			}
			c := <-conns
			var closed func()
			switch name {
			case "binary message":
				_, _ = conn.Write([]byte{0x82, 0x81, 0, 0, 0, 0, 1})
				read := make(chan error, 1)
				go func() { _, err := c.ReadMessage(context.Background()); read <- err }()
				if code := awaitClose(t, br); code != StatusUnsupported {
					t.Fatalf("close code %d", code)
				}
				closed = func() {
					if err := <-read; err == nil {
						t.Error("a binary message was read")
					}
				}
			case "Abort in Close":
				// Without the bound's help: Abort itself ends the close.
				t.Cleanup(SetCloseTimeout(time.Hour))
				fallthrough
			default:
				ret := make(chan struct{})
				go func() { _ = c.Close(StatusNormal, ""); close(ret) }()
				if code := awaitClose(t, br); code != StatusNormal {
					t.Fatalf("close code %d", code)
				}
				closed = func() { <-ret }
			}
			header := stalledHeader(true)
			_, _ = conn.Write(header)
			switch name {
			case "Close twice":
				within(t, 5*time.Second, "a second Close", func() { _ = c.Close(StatusGoingAway, "") })
			case "Abort in Close":
				// The close has read the header and waits for the payload.
				reads.await(t, int64(len(header)))
				within(t, 5*time.Second, "Abort", c.Abort)
			}
			within(t, 5*time.Second, "the closing handshake", closed)
			expectEOF(t, conn, br)
		})
	}
}

// nowWriter records WriteHeaderNow, which writers of some frameworks need
// after the 101.
type nowWriter struct {
	unwrapWriter
	now atomic.Bool
}

func (n *nowWriter) WriteHeaderNow() { n.now.Store(true) }

// WriteHeaderNow of the application's writer still follows the 101.
func TestAcceptWriteHeaderNow(t *testing.T) {
	leakcheck.Check(t)
	var nw *nowWriter
	addr, conns, _ := acceptOne(t, AcceptOptions{Subprotocol: "ocpp1.6"}, func(w http.ResponseWriter) http.ResponseWriter {
		nw = &nowWriter{unwrapWriter: unwrapWriter{w}}
		return nw
	})
	_, _, resp := upgrade(t, addr, "ocpp1.6", nil)
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("handshake answered %d", resp.StatusCode)
	}
	c := <-conns
	defer c.Abort()
	if !nw.now.Load() {
		t.Fatal("WriteHeaderNow not called")
	}
}

// A handshake refused inside Accept closes the connection: a foreign
// Origin (403) and malformed upgrade requests (426, 400).
func TestAcceptRefusalClosesConnection(t *testing.T) {
	leakcheck.Check(t)
	addr, _, errs := acceptOne(t, AcceptOptions{Subprotocol: "ocpp1.6", OriginPatterns: []string{"dashboard.example.com"}}, nil)
	for name, c := range map[string]struct {
		extra  map[string]string
		status int
	}{
		"foreign Origin": {map[string]string{"Origin": "https://other.example.com"}, http.StatusForbidden},
		"allowed Origin": {map[string]string{"Origin": "https://dashboard.example.com", "Sec-WebSocket-Version": "8"}, http.StatusBadRequest},
		"no upgrade":     {map[string]string{"Upgrade": "h2c"}, http.StatusUpgradeRequired},
		"bad version":    {map[string]string{"Sec-WebSocket-Version": "8"}, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			conn, br, resp := upgrade(t, addr, "ocpp1.6", c.extra)
			if resp.StatusCode != c.status {
				t.Fatalf("status %d, want %d", resp.StatusCode, c.status)
			}
			if resp.StatusCode == http.StatusSwitchingProtocols || !resp.Close {
				t.Fatalf("status %d, Connection %q", resp.StatusCode, resp.Header.Get("Connection"))
			}
			if err := <-errs; err == nil {
				t.Error("refused handshake accepted")
			}
			expectEOF(t, conn, br)
		})
	}

	// A writer without a Hijacker: 501, also with Connection: close.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ocpp/CS1", nil)
	r.Header = http.Header{"Connection": {"Upgrade"}, "Upgrade": {"websocket"}, "Sec-Websocket-Version": {"13"},
		"Sec-Websocket-Key": {"dGhlIHNhbXBsZSBub25jZQ=="}}
	if _, err := Accept(rec, r, AcceptOptions{}); err == nil || rec.Code != http.StatusNotImplemented ||
		rec.Header().Get("Connection") != "close" {
		t.Fatalf("%v %d %q", err, rec.Code, rec.Header().Get("Connection"))
	}
}

func TestCheckOrigin(t *testing.T) {
	leakcheck.Check(t)
	for _, c := range []struct {
		origin, host string
		patterns     []string
		ok           bool
	}{
		{"", "csms.example.com", nil, true},
		{"https://csms.example.com", "CSMS.example.com", nil, true},
		{"https://other.example.com", "csms.example.com", nil, false},
		{"https://Dash.example.com", "csms.example.com", []string{"dash.example.com"}, true},
		{"https://a.example.com", "csms.example.com", []string{"*.example.com"}, true},
		{"http://dash.example.com", "csms.example.com", []string{"https://dash.example.com"}, false},
		{"https://dash.example.com", "csms.example.com", []string{"https://dash.example.com"}, true},
		{"https://dash.example.com", "csms.example.com", []string{"["}, false},
		{"http://%zz", "csms.example.com", nil, false},
		{"null", "csms.example.com", nil, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if err := CheckOrigin(r, c.patterns); (err == nil) != c.ok {
			t.Errorf("Origin %q, host %q, patterns %q: %v", c.origin, c.host, c.patterns, err)
		}
	}
}

// rawCSMS accepts one WebSocket for subprotocol by hand and hands its
// connection to the test.
func rawCSMS(t *testing.T, subprotocol string) (string, <-chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	conns := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = conn.Close() })
		br := bufio.NewReader(conn)
		r, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: %s\r\nSec-WebSocket-Protocol: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]), subprotocol)
		conns <- &bufConn{Conn: conn, br: br}
	}()
	return "ws://" + ln.Addr().String() + "/ocpp/CS1", conns
}

type bufConn struct {
	net.Conn
	br *bufio.Reader
}

// The client side: a CSMS that stalls the closing handshake holds neither
// Close nor the close of a binary message past the bound.
func TestDialCloseBounded(t *testing.T) {
	leakcheck.Check(t)
	shortClose(t)
	for _, name := range []string{"Close", "binary message"} {
		t.Run(name, func(t *testing.T) {
			url, conns := rawCSMS(t, "ocpp1.6")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			c, _, err := Dial(ctx, url, DialOptions{Subprotocols: []string{"ocpp1.6"}})
			if err != nil {
				t.Fatal(err)
			}
			peer := (<-conns).(*bufConn)
			_ = peer.SetDeadline(time.Now().Add(10 * time.Second))
			ret := make(chan error, 1)
			want := StatusNormal
			if name == "binary message" {
				want = StatusUnsupported
				_, _ = peer.Write([]byte{0x82, 0x01, 1})
				go func() { _, err := c.ReadMessage(context.Background()); ret <- err }()
			} else {
				go func() { ret <- c.Close(StatusNormal, "") }()
			}
			if code := awaitClose(t, peer.br); code != want {
				t.Fatalf("close code %d", code)
			}
			_, _ = peer.Write(stalledHeader(false))
			within(t, 5*time.Second, "the closing handshake", func() {
				if err := <-ret; (err == nil) != (name == "Close") {
					t.Errorf("%s: %v", name, err)
				}
			})
			expectEOF(t, peer, peer.br)
		})
	}
}

// A redirect's connection is among those the bound closes; closing it
// again is harmless.
func TestDialedCloser(t *testing.T) {
	leakcheck.Check(t)
	a, b := net.Pipe()
	defer func() { _ = b.Close() }()
	d := &dialed{conns: []net.Conn{a}}
	d.closer()()
	if _, err := a.Write([]byte{1}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("not closed: %v", err)
	}
	d.closer()()
}

func TestCloseTimeout(t *testing.T) {
	leakcheck.Check(t)
	if closeTimeout() != defaultCloseTimeout {
		t.Fatal(closeTimeout())
	}
	restore := SetCloseTimeout(time.Second)
	if closeTimeout() != time.Second {
		t.Fatal(closeTimeout())
	}
	restore()
	if closeTimeout() != defaultCloseTimeout {
		t.Fatal(closeTimeout())
	}
}
