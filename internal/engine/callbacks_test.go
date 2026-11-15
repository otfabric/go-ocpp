// SPDX-License-Identifier: MIT

package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/ocppj"
)

// logged returns a logger and a function that reads what it has written.
func logged() (*slog.Logger, func() string) {
	var buf bytes.Buffer
	var mu sync.Mutex
	l := slog.New(slog.NewTextHandler(&lockedWriter{w: &buf, mu: &mu}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return l, func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// A function registered with ocpp.AfterResponse that does not return
// normally is recovered where the engine calls it and logged with its
// stack; the functions registered after it still run, in order, and the
// endpoint goes on answering.
func TestAfterResponseFunctionThatDoesNotReturnNormally(t *testing.T) {
	checkNoLeak(t)
	log, logs := logged()
	ran := make(chan string, 4)
	type report struct{ sent int }
	send := func(r *report) { r.sent++ }
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		if action == "GetBaseReport" {
			ocpp.AfterResponse(ctx, func() { ran <- "first" })
			ocpp.AfterResponse(ctx, func() { send(nil) }) // no report: this function does not return
			ocpp.AfterResponse(ctx, func() { panic(errors.New("no report to send")) })
			ocpp.AfterResponse(ctx, func() { ran <- "last" })
		}
		return json.RawMessage(`{"status":"Accepted"}`), nil
	})
	e, raw := rawPeer(t, ocpp.V201, d, func(c *Config) { c.Logger = log })
	writeRaw(t, raw, `[2,"r1","GetBaseReport",{}]`)
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallResult || f.ID != "r1" {
		t.Fatalf("got %+v, want the response to r1", f)
	}
	for _, want := range []string{"first", "last"} {
		select {
		case got := <-ran:
			if got != want {
				t.Fatalf("the %s function ran where the %s one was due", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the %s function did not run", want)
		}
	}
	// The endpoint is up: the next CALL is answered, and it can call.
	writeRaw(t, raw, `[2,"r2","Heartbeat",{}]`)
	if f := readFrame(t, raw, ocpp.V201); f.Type != ocppj.CallResult || f.ID != "r2" {
		t.Fatalf("got %+v, want the response to r2", f)
	}
	if err := e.Err(); err != nil {
		t.Fatalf("the endpoint ended: %v", err)
	}
	out := logs()
	const msg = "function registered with AfterResponse did not return normally"
	if n := strings.Count(out, msg); n != 2 {
		t.Fatalf("logged %d times, want once for each of the two functions:\n%s", n, out)
	}
	for _, want := range []string{"level=ERROR", "action=GetBaseReport", "message_id=r1", "stack=", "nil pointer dereference", "no report to send",
		"TestAfterResponseFunctionThatDoesNotReturnNormally"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
}

// A handler interceptor that does not return normally, before or after it
// called next, is recovered on the dispatcher: the CALL is answered with
// InternalError, the failure is reported to OnCallHandled as
// ocpp.ErrHandlerPanic and logged with its stack, and the next message is
// dispatched. The same holds for an incoming SEND, which gets no answer.
func TestHandlerInterceptorThatDoesNotReturnNormally(t *testing.T) {
	checkNoLeak(t)
	log, logs := logged()
	m := &handledMetrics{}
	var handled []string
	d := sendDispatcher{got: make(chan string, 4)}
	icpt := func(ctx context.Context, info ocpp.CallInfo, next func(context.Context) error) error {
		if info.Action == "Before" {
			panic("interceptor, before the handler")
		}
		err := next(ctx)
		handled = append(handled, info.Action) // the dispatcher goroutine only
		if info.Action == "After" || info.Action == "AfterSend" {
			panic("interceptor, after the handler")
		}
		return err
	}
	_, raw := rawPeer(t, ocpp.V21, d, func(c *Config) { c.Logger = log; c.Metrics = m; c.HandlerInterceptor = icpt })
	for _, action := range []string{"Before", "After"} {
		writeRaw(t, raw, `[2,"`+action+`","`+action+`",{}]`)
		f := readFrame(t, raw, ocpp.V21)
		if f.Type != ocppj.CallError || f.ID != action || f.ErrorCode != ocpp.InternalError || f.Description != "" {
			t.Fatalf("%s: got %+v, want CALLERROR InternalError without description", action, f)
		}
	}
	writeRaw(t, raw, `[6,"s","AfterSend",{}]`)
	if got := <-d.got; got != "AfterSend {}" {
		t.Fatalf("SEND dispatched as %q", got)
	}
	// The dispatcher goes on: a CALL after them is handled and answered.
	writeRaw(t, raw, `[2,"ok","Heartbeat",{}]`)
	if f := readFrame(t, raw, ocpp.V21); f.Type != ocppj.CallResult || f.ID != "ok" {
		t.Fatalf("got %+v, want the response to ok", f)
	}
	if got := strings.Join(handled, " "); got != "After AfterSend Heartbeat" {
		t.Errorf("handlers ran for %q, want After, AfterSend and Heartbeat", got)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errs) != 4 || m.errs[3] != nil {
		t.Fatalf("OnCallHandled got %v, want three failures and the Heartbeat", m.errs)
	}
	for i, err := range m.errs[:3] {
		if !errors.Is(err, ocpp.ErrHandlerPanic) {
			t.Errorf("OnCallHandled %d got %v, want ErrHandlerPanic", i, err)
		}
	}
	out := logs()
	if n := strings.Count(out, "handler interceptor did not return normally"); n != 3 {
		t.Errorf("logged %d times, want 3:\n%s", n, out)
	}
	for _, want := range []string{"level=ERROR", "action=Before", "message_id=After", "action=AfterSend", "stack=", "TestHandlerInterceptorThatDoesNotReturnNormally"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log lacks %q:\n%s", want, out)
		}
	}
}

// Done and Close wait for the reader, the writer, the pinger and the
// dispatcher, and not for a function registered with ocpp.AfterResponse:
// both return while such a function still runs, and what it then calls on
// the endpoint fails at once.
func TestDoneDoesNotWaitForAfterResponse(t *testing.T) {
	checkNoLeak(t)
	started, release := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	var e *Endpoint
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		ocpp.AfterResponse(ctx, func() {
			close(started)
			<-release
			result <- e.Call(context.Background(), "NotifyReport", nil, nil)
		})
		return json.RawMessage(`{}`), nil
	})
	var raw *raw
	e, raw = rawPeer(t, ocpp.V201, d, func(c *Config) { c.PingInterval = time.Hour })
	writeRaw(t, raw, `[2,"r","GetBaseReport",{}]`)
	readFrame(t, raw, ocpp.V201)
	<-started
	closed := make(chan error, 1)
	go func() { closed <- e.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a function registered with AfterResponse")
	}
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done waited for a function registered with AfterResponse")
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("a Call after the end: %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a Call on the ended endpoint did not return")
	}
}

// A function registered with ocpp.AfterResponse may close the endpoint: Close
// returns in it, and Done is closed.
func TestCloseFromAfterResponse(t *testing.T) {
	checkNoLeak(t)
	closed := make(chan error, 1)
	var e *Endpoint
	d := ocpp.DispatcherFunc(func(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
		ocpp.AfterResponse(ctx, func() { closed <- e.Close() })
		return json.RawMessage(`{}`), nil
	})
	var raw *raw
	e, raw = rawPeer(t, ocpp.V16, d, nil)
	writeRaw(t, raw, `[2,"r","Reset",{}]`)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close from a function registered with AfterResponse did not return")
	}
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if e.Err() != ocpp.ErrClosed {
		t.Fatalf("Err = %v", e.Err())
	}
}
