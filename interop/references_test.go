//go:build interop

// SPDX-License-Identifier: MIT

package interop

import "testing"

// TestReferences checks that the pinned images are the ocpp-interop release
// the documentation says this version is qualified against.
func TestReferences(t *testing.T) {
	for _, a := range adapters(t) {
		c := a.caps(t)
		t.Logf("%s: ocpp-interop %s (contract %s), %s %s, %s", a.name, c.AdapterVersion, c.ContractVersion,
			c.Upstream.Name, c.Upstream.Version, a.image)
		if a.pinned && c.AdapterVersion != interopRelease {
			t.Errorf("%s is pinned as ocpp-interop %s but reports %s", a.name, interopRelease, c.AdapterVersion)
		}
	}
}
