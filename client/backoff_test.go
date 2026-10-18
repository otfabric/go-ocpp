// SPDX-License-Identifier: MIT

package client

import (
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-ocpp"
)

func newReconnecting(t *testing.T, r Reconnect) *Client {
	t.Helper()
	c, err := New("wss://csms.example/ocpp/CS1", WithVersions(ocpp.V16), WithReconnect(r))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// The back-off of OCPP 2.x Part 4 §5.4: WaitMinimum, doubled after every
// failed attempt up to RepeatTimes times, plus up to RandomRange at random.
func TestBackoff(t *testing.T) {
	c := newReconnecting(t, Reconnect{WaitMinimum: 10 * time.Millisecond, RandomRange: 3 * time.Millisecond, RepeatTimes: 3})
	for attempt, base := range map[int]time.Duration{1: 10, 2: 20, 3: 40, 4: 80, 5: 80, 9: 80} {
		base *= time.Millisecond
		for range 100 {
			if d := c.backoff(attempt); d < base || d > base+3*time.Millisecond {
				t.Fatalf("attempt %d: %v outside [%v, %v]", attempt, d, base, base+3*time.Millisecond)
			}
		}
	}
	// No randomness and no doubling.
	fixed := newReconnecting(t, Reconnect{WaitMinimum: time.Second})
	for _, n := range []int{1, 2, 10} {
		if d := fixed.backoff(n); d != time.Second {
			t.Fatalf("attempt %d: %v", n, d)
		}
	}
	// A huge RepeatTimes saturates instead of overflowing.
	huge := newReconnecting(t, Reconnect{WaitMinimum: time.Hour, RepeatTimes: 1000})
	if d := huge.backoff(1000); d < time.Hour {
		t.Fatalf("saturated back-off: %v", d)
	}
	if DefaultReconnect.WaitMinimum <= 0 {
		t.Fatal("DefaultReconnect is not usable")
	}
	newReconnecting(t, DefaultReconnect)
}

func TestReconnectValidation(t *testing.T) {
	for _, r := range []Reconnect{{}, {WaitMinimum: -1}, {WaitMinimum: 1, RandomRange: -1}, {WaitMinimum: 1, RepeatTimes: -1}} {
		if _, err := New("wss://csms.example/ocpp/CS1", WithVersions(ocpp.V16), WithReconnect(r)); !errors.Is(err, ocpp.ErrInvalidOption) {
			t.Errorf("%+v: %v", r, err)
		}
	}
}
