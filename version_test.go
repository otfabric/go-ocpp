// SPDX-License-Identifier: MIT

package ocpp

import "testing"

func TestVersions(t *testing.T) {
	tests := []struct {
		v           Version
		name        string
		subprotocol string
	}{
		{V16, "1.6", "ocpp1.6"},
		{V201, "2.0.1", "ocpp2.0.1"},
		{V21, "2.1", "ocpp2.1"},
	}
	if got := Versions(); len(got) != len(tests) {
		t.Fatalf("Versions() = %v, want %d versions", got, len(tests))
	}
	for i, tt := range tests {
		if got := Versions()[i]; got != tt.v {
			t.Errorf("Versions()[%d] = %v, want %v", i, got, tt.v)
		}
		if !tt.v.Valid() {
			t.Errorf("%v.Valid() = false", tt.v)
		}
		if got := tt.v.String(); got != tt.name {
			t.Errorf("String() = %q, want %q", got, tt.name)
		}
		if got := tt.v.Subprotocol(); got != tt.subprotocol {
			t.Errorf("Subprotocol() = %q, want %q", got, tt.subprotocol)
		}
		if got, ok := ParseVersion(tt.name); !ok || got != tt.v {
			t.Errorf("ParseVersion(%q) = %v, %v", tt.name, got, ok)
		}
		if got, ok := ParseSubprotocol(tt.subprotocol); !ok || got != tt.v {
			t.Errorf("ParseSubprotocol(%q) = %v, %v", tt.subprotocol, got, ok)
		}
	}
}

func TestInvalidVersion(t *testing.T) {
	for _, v := range []Version{0, V21 + 1, 255} {
		if v.Valid() {
			t.Errorf("Version(%d).Valid() = true", uint8(v))
		}
		if got := v.Subprotocol(); got != "" {
			t.Errorf("Version(%d).Subprotocol() = %q, want empty", uint8(v), got)
		}
	}
	for v, want := range map[Version]string{0: "Version(0)", 4: "Version(4)", 255: "Version(255)"} {
		if got := v.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	// Not supported (SOAP, older versions), or not written exactly as the
	// specifications write them. Subprotocol tokens are case-sensitive.
	for _, s := range []string{"", "1.5", "1.6s", "2.0", "2.0.1 ", "ocpp1.6", "OCPP2.0.1"} {
		if v, ok := ParseVersion(s); ok {
			t.Errorf("ParseVersion(%q) = %v, want failure", s, v)
		}
	}
	for _, s := range []string{"", "ocpp1.5", "ocpp2.0", "OCPP1.6", "ocpp1.6 ", "1.6"} {
		if v, ok := ParseSubprotocol(s); ok {
			t.Errorf("ParseSubprotocol(%q) = %v, want failure", s, v)
		}
	}
}
