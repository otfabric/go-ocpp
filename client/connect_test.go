// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	"github.com/otfabric/go-ocpp"
)

// connecting returns a client whose state is Connecting, as while another
// Connect is in the handshake.
func connecting(t *testing.T) *Client {
	t.Helper()
	c, err := New("wss://csms.example/ocpp/CS1", WithVersions(ocpp.V16))
	if err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.setStateLocked(ocpp.Connecting, nil)
	c.mu.Unlock()
	return c
}

// A Connect waiting for another one ends with ErrClosed when the client is
// closed while it waits.
func TestConnectWaitingClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := connecting(t)
		done := make(chan error, 1)
		go func() { done <- c.Connect(context.Background()) }()
		synctest.Wait() // the Connect is waiting
		_ = c.Close()
		if err := <-done; !errors.Is(err, ocpp.ErrClosed) {
			t.Fatalf("Connect = %v", err)
		}
	})
}

// A waiting Connect ends with its context, and returns the other Connect's
// error when that one fails.
func TestConnectWaitingOutcome(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := connecting(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.Connect(ctx) }()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, ocpp.ErrConnectFailed) || !errors.Is(err, context.Canceled) {
			t.Fatalf("Connect = %v", err)
		}

		go func() { done <- c.Connect(context.Background()) }()
		synctest.Wait()
		failed := errors.New("refused")
		c.mu.Lock()
		c.setStateLocked(ocpp.Disconnected, failed)
		c.mu.Unlock()
		if err := <-done; !errors.Is(err, failed) {
			t.Fatalf("Connect = %v", err)
		}
		_ = c.Close()
	})
}

// RFC 7617 §2: a Basic user-id with a colon is invalid; the CSMS would
// split it at the colon and see another user and password.
func TestBasicAuthColon(t *testing.T) {
	_, err := New("wss://csms.example/ocpp/CS:1", WithVersions(ocpp.V16), WithBasicAuth("CS:1", "pw"))
	if !errors.Is(err, ocpp.ErrInvalidOption) {
		t.Fatalf("New = %v", err)
	}
}
