// SPDX-License-Identifier: MIT

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/client"
	"github.com/otfabric/go-ocpp/server"
)

// counter answers CALLs with {} (or with answer) and counts the SENDs it receives.
type counter struct {
	sends  atomic.Int64
	answer func(ctx context.Context, p ocpp.Peer, action string) (json.RawMessage, error)
}

func (c *counter) Dispatch(ctx context.Context, p ocpp.Peer, action string, _ json.RawMessage) (json.RawMessage, error) {
	if c.answer != nil {
		return c.answer(ctx, p, action)
	}
	return json.RawMessage(`{}`), nil
}

func (c *counter) DispatchSend(context.Context, ocpp.Peer, string, json.RawMessage) error {
	c.sends.Add(1)
	return nil
}

const sendPayload = `{"id":1,"pending":0,"basetime":"2026-01-01T00:00:00Z","data":[{"t":0,"v":"230"}]}`

// drainRounds is how often each race is tried: the frames used to be lost only
// when the close overtook a write in progress.
const drainRounds = 100

// A SEND queued just before Shutdown reaches the CSMS: Shutdown waits for a frame
// the writer has taken off its queue but not written yet.
func TestClientShutdownWritesQueuedSend(t *testing.T) {
	noLeak(t)
	recv := &counter{}
	_, base := csms(t, nil, server.WithHandler(ocpp.V21, recv), server.WithoutAuthentication())
	for i := range drainRounds {
		c := station(t, fmt.Sprintf("%sSEND%d", base, i), client.WithInsecureTransport(), client.WithVersions(ocpp.V21))
		connect(t, c)
		if err := c.Send(context.Background(), "NotifyPeriodicEventStream", json.RawMessage(sendPayload)); err != nil {
			t.Fatalf("round %d: Send: %v", i, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Shutdown(ctx); err != nil {
			t.Fatalf("round %d: Shutdown: %v", i, err)
		}
		cancel()
	}
	eventually(t, "every SEND", func() bool { return recv.sends.Load() == drainRounds })
}

// The answer to a CALL from the CSMS is written before a Shutdown that the
// handler started with ocpp.AfterResponse closes the connection (a station that
// accepts a Reset and then restarts).
func TestClientShutdownAfterResponse(t *testing.T) {
	noLeak(t)
	srv, base2 := csms(t, nil, server.WithHandler(ocpp.V201, &counter{}), server.WithoutAuthentication())
	for i := range drainRounds {
		id := fmt.Sprintf("RESET%d", i)
		var c *client.Client
		reset := &counter{answer: func(ctx context.Context, _ ocpp.Peer, _ string) (json.RawMessage, error) {
			ocpp.AfterResponse(ctx, func() {
				sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = c.Shutdown(sctx)
			})
			return json.RawMessage(`{"status":"Accepted"}`), nil
		}}
		c = station(t, base2+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V201), client.WithHandler(ocpp.V201, reset))
		connect(t, c)
		sess := waitStation(t, srv, id)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var resp struct{ Status string }
		if err := sess.Call(ctx, "Reset", json.RawMessage(`{"type":"Immediate"}`), &resp); err != nil || resp.Status != "Accepted" {
			t.Fatalf("round %d: Reset: %v %+v", i, err, resp)
		}
		cancel()
		eventually(t, "the station to close", func() bool { return c.State() == ocpp.Closed })
	}
}

// Session.Shutdown writes a SEND queued just before it, as Client.Shutdown does.
func TestSessionShutdownWritesQueuedSend(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V21, &counter{}), server.WithoutAuthentication())
	recv := &counter{}
	for i := range drainRounds {
		id := fmt.Sprintf("SSEND%d", i)
		c := station(t, base+id, client.WithInsecureTransport(), client.WithVersions(ocpp.V21), client.WithHandler(ocpp.V21, recv))
		connect(t, c)
		sess := waitStation(t, srv, id)
		if err := sess.Send(context.Background(), "NotifyWebPaymentStarted", json.RawMessage(`{"evseId":1,"timeout":60}`)); err != nil {
			t.Fatalf("round %d: Send: %v", i, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := sess.Shutdown(ctx); err != nil {
			t.Fatalf("round %d: Shutdown: %v", i, err)
		}
		cancel()
	}
	eventually(t, "every SEND", func() bool { return recv.sends.Load() == drainRounds })
}

// Server.Shutdown lets a CALL being handled finish and writes its answer before
// it closes the session.
func TestServerShutdownAnswersCallInHandler(t *testing.T) {
	noLeak(t)
	started, release := make(chan struct{}), make(chan struct{})
	slow := &counter{answer: func(ctx context.Context, _ ocpp.Peer, _ string) (json.RawMessage, error) {
		close(started)
		<-release
		return json.RawMessage(`{"currentTime":"2026-01-01T00:00:00Z"}`), nil
	}}
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, slow), server.WithoutAuthentication())
	c := station(t, base+"DRAINSRV", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c)
	answered := make(chan error, 1)
	go func() { answered <- c.Call(context.Background(), "Heartbeat", json.RawMessage(`{}`), nil) }()
	<-started
	shut := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shut <- srv.Shutdown(ctx)
	}()
	select {
	case err := <-shut:
		t.Fatalf("Shutdown returned while a CALL was being handled: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-answered; err != nil {
		t.Fatalf("the CALL being handled: %v", err)
	}
	if err := <-shut; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Bounded by its context: a handler that does not end does not hold it up.
	entered, block := make(chan struct{}), make(chan struct{})
	defer close(block)
	stuck := &counter{answer: func(ctx context.Context, _ ocpp.Peer, _ string) (json.RawMessage, error) {
		close(entered)
		select {
		case <-block:
		case <-ctx.Done():
		}
		return json.RawMessage(`{}`), nil
	}}
	srv2, base2 := csms(t, nil, server.WithHandler(ocpp.V201, stuck), server.WithoutAuthentication())
	c2 := station(t, base2+"STUCK", client.WithInsecureTransport(), client.WithVersions(ocpp.V201))
	connect(t, c2)
	go func() { _ = c2.Call(context.Background(), "Heartbeat", json.RawMessage(`{}`), nil) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := srv2.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with a handler that does not end: %v", err)
	}
	// Closed even so; they end once their stuck handler sees the close.
	eventually(t, "the sessions to end", func() bool { return len(srv2.Stations()) == 0 })
}

// Session.Shutdown returns ctx.Err() when what is in flight does not finish in
// time, and closes the session anyway.
func TestSessionShutdownTimeout(t *testing.T) {
	noLeak(t)
	srv, base := csms(t, nil, server.WithHandler(ocpp.V201, &counter{}), server.WithoutAuthentication())
	entered, release := make(chan struct{}), make(chan struct{})
	slow := &counter{answer: func(ctx context.Context, _ ocpp.Peer, _ string) (json.RawMessage, error) {
		close(entered)
		<-release
		return json.RawMessage(`{"status":"Accepted"}`), nil
	}}
	c := station(t, base+"SLOWST", client.WithInsecureTransport(), client.WithVersions(ocpp.V201), client.WithHandler(ocpp.V201, slow))
	connect(t, c)
	sess := waitStation(t, srv, "SLOWST")
	called := make(chan error, 1)
	go func() {
		called <- sess.Call(context.Background(), "Reset", json.RawMessage(`{"type":"Immediate"}`), nil)
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := sess.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Session.Shutdown: %v", err)
	}
	close(release)
	if err := <-called; !errors.Is(err, ocpp.ErrClosed) {
		t.Fatalf("the CALL in flight when the session closed: %v", err)
	}
	if err := sess.Send(context.Background(), "NotifyWebPaymentStarted", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a SEND on a closed session")
	}
}
