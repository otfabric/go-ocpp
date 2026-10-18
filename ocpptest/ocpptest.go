// SPDX-License-Identifier: MIT

// Package ocpptest helps applications test their OCPP handlers against a
// real go-ocpp peer over a WebSocket on the loopback interface, the way
// net/http/httptest helps test HTTP handlers.
//
// A CSMS application tests its handlers with a station from Station; a
// station application tests its handlers with a CSMS from CSMS. Recorder
// is a dispatcher that records what arrives and answers from a table, for
// the side the test does not implement:
//
//	rec := ocpptest.NewRecorder()
//	rec.Answer("BootNotification", `{"status":"Accepted","currentTime":"2026-01-01T00:00:00Z","interval":300}`)
//	srv, url := ocpptest.CSMS(t, server.WithHandler(ocpp.V201, rec))
//	c := ocpptest.Station(t, url+"CS1", client.WithHandler(ocpp.V201, myStationHandler))
//	... // drive the station application
//	req := rec.Wait(t, "BootNotification")
//
// Everything started is stopped when the test ends.
package ocpptest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// Timeout bounds the waits of this package.
const Timeout = 10 * time.Second

// CSMS starts a go-ocpp server with opts on the loopback interface and
// returns it with the base URL stations connect to ("ws://127.0.0.1:port/
// ocpp/"; append the station identity). Without an authentication option
// it adds server.WithoutAuthentication. The server is shut down when the
// test ends.
func CSMS(t testing.TB, opts ...server.Option) (*server.Server, string) {
	t.Helper()
	srv, err := server.New(opts...)
	if err != nil && strings.Contains(err.Error(), "no authenticator") {
		srv, err = server.New(append(opts, server.WithoutAuthentication())...)
	}
	if err != nil {
		t.Fatalf("ocpptest: server.New: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/ocpp/", srv)
	hs := httptest.NewServer(mux)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), Timeout)
		defer cancel()
		_ = srv.Shutdown(ctx)
		hs.Close()
	})
	return srv, "ws" + strings.TrimPrefix(hs.URL, "http") + "/ocpp/"
}

// Station creates a go-ocpp client for url with opts, plus
// client.WithInsecureTransport (the test server is plain ws on loopback),
// connects it, and closes it when the test ends.
func Station(t testing.TB, url string, opts ...client.Option) *client.Client {
	t.Helper()
	c, err := client.New(url, append([]client.Option{client.WithInsecureTransport()}, opts...)...)
	if err != nil {
		t.Fatalf("ocpptest: client.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("ocpptest: connect: %v", err)
	}
	return c
}

// Session waits until srv has a session for the station id and returns it.
func Session(t testing.TB, srv *server.Server, id string) *server.Session {
	t.Helper()
	deadline := time.Now().Add(Timeout)
	for time.Now().Before(deadline) {
		if s, ok := srv.Station(id); ok {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("ocpptest: no session for %s", id)
	return nil
}

// Recorder is an ocpp.Dispatcher and ocpp.SendDispatcher that records
// every CALL and SEND it receives and answers CALLs from a table. An
// action without an answer gets the CALLERROR NotImplemented. It does not
// validate payloads: put a version package's dispatcher in front of it
// (or use ValidateRequest) when validation matters. Safe for concurrent
// use.
type Recorder struct {
	mu       sync.Mutex
	answers  map[string]json.RawMessage
	errors   map[string]*ocpp.CallError
	received map[string][]json.RawMessage
	notify   chan struct{}
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder {
	return &Recorder{answers: map[string]json.RawMessage{}, errors: map[string]*ocpp.CallError{},
		received: map[string][]json.RawMessage{}, notify: make(chan struct{})}
}

// Answer sets the CALLRESULT payload for action, a JSON object.
func (r *Recorder) Answer(action, payload string) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[action] = json.RawMessage(payload)
	delete(r.errors, action)
	return r
}

// AnswerError makes action answered with a CALLERROR.
func (r *Recorder) AnswerError(action string, err *ocpp.CallError) *Recorder {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.errors[action] = err
	delete(r.answers, action)
	return r
}

// Dispatch implements ocpp.Dispatcher.
func (r *Recorder) Dispatch(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) (json.RawMessage, error) {
	r.record(action, payload)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, ok := r.errors[action]; ok {
		return nil, err
	}
	if a, ok := r.answers[action]; ok {
		return a, nil
	}
	return nil, ocpp.NewCallError(ocpp.NotImplemented, "")
}

// DispatchSend implements ocpp.SendDispatcher (OCPP 2.1).
func (r *Recorder) DispatchSend(ctx context.Context, p ocpp.Peer, action string, payload json.RawMessage) error {
	r.record(action, payload)
	return nil
}

func (r *Recorder) record(action string, payload json.RawMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.received[action] = append(r.received[action], append(json.RawMessage(nil), payload...))
	close(r.notify)
	r.notify = make(chan struct{})
}

// Received returns the payloads of action received so far, oldest first.
func (r *Recorder) Received(action string) []json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]json.RawMessage(nil), r.received[action]...)
}

// Wait waits until action has been received and returns its latest
// payload; it fails the test after Timeout.
func (r *Recorder) Wait(t testing.TB, action string) json.RawMessage {
	t.Helper()
	timeout := time.After(Timeout)
	for {
		r.mu.Lock()
		got, ch := r.received[action], r.notify
		r.mu.Unlock()
		if len(got) > 0 {
			return got[len(got)-1]
		}
		select {
		case <-ch:
		case <-timeout:
			t.Fatalf("ocpptest: no %s received within %s", action, Timeout)
			return nil
		}
	}
}

var (
	_ ocpp.Dispatcher     = (*Recorder)(nil)
	_ ocpp.SendDispatcher = (*Recorder)(nil)
)
