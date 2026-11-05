// SPDX-License-Identifier: MIT

package ocpp_test

import (
	"testing"

	"github.com/otfabric/go-ocpp"
)

func TestPtr(t *testing.T) {
	v := "OTF-0001"
	p := ocpp.Ptr(v)
	if p == nil || *p != v {
		t.Fatalf("Ptr(%q) = %v", v, p)
	}
	if q := ocpp.Ptr(v); q == p {
		t.Fatal("two calls returned the same pointer")
	}
}
