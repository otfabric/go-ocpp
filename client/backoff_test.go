// SPDX-License-Identifier: MIT

package client

import (
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
)

func TestBackoff(t *testing.T) {
	newClient := func(r Reconnect) *Client {
		c, err := New("wss://csms.example/ocpp/CS1", WithVersions(ocpp.V16), WithReconnect(r))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := newClient(Reconnect{MinDelay: 10 * time.Millisecond, MaxDelay: 80 * time.Millisecond})
	for attempt := 1; attempt <= 10; attempt++ {
		ceil := min(10*time.Millisecond<<attempt, 80*time.Millisecond)
		for range 50 {
			if d := c.backoff(attempt); d < 10*time.Millisecond || d >= ceil {
				t.Fatalf("attempt %d: %v outside [10ms, %v)", attempt, d, ceil)
			}
		}
	}
	// MinDelay equal to MaxDelay: no jitter left.
	if d := newClient(Reconnect{MinDelay: time.Second, MaxDelay: time.Second}).backoff(3); d != time.Second {
		t.Fatalf("fixed delay: %v", d)
	}
	// Zero values take the defaults.
	def := newClient(Reconnect{})
	if def.o.reconnect.MinDelay != DefaultReconnectMin || def.o.reconnect.MaxDelay != DefaultReconnectMax {
		t.Fatalf("defaults: %+v", def.o.reconnect)
	}
}
