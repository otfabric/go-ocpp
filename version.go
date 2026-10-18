// SPDX-License-Identifier: MIT

package ocpp

// Version is an OCPP protocol version that go-ocpp implements over JSON
// and WebSocket (OCPP-J). The zero value is not a valid version.
type Version uint8

// The versions in scope. OCPP 1.6 SOAP and versions before 1.6 are not
// supported. SPECIFICATIONS.md lists the editions and errata followed.
const (
	V16  Version = iota + 1 // OCPP 1.6J, with the Security Whitepaper extensions
	V201                    // OCPP 2.0.1
	V21                     // OCPP 2.1
)

var versions = [...]struct {
	name        string
	subprotocol string
}{
	V16:  {"1.6", "ocpp1.6"},
	V201: {"2.0.1", "ocpp2.0.1"},
	V21:  {"2.1", "ocpp2.1"},
}

// Versions returns the supported versions, oldest first.
func Versions() []Version {
	return []Version{V16, V201, V21}
}

// Valid reports whether v is one of the supported versions.
func (v Version) Valid() bool {
	return v >= V16 && v <= V21
}

// String returns the version as the specifications write it: "1.6",
// "2.0.1" or "2.1". An invalid version is written as "Version(n)".
func (v Version) String() string {
	if !v.Valid() {
		return "Version(" + itoa(uint8(v)) + ")"
	}
	return versions[v].name
}

// Subprotocol returns the WebSocket subprotocol that selects v during the
// opening handshake ("ocpp1.6", "ocpp2.0.1" or "ocpp2.1"). It returns ""
// for an invalid version.
func (v Version) Subprotocol() string {
	if !v.Valid() {
		return ""
	}
	return versions[v].subprotocol
}

// ParseVersion parses a version as String writes it.
func ParseVersion(s string) (Version, bool) {
	for _, v := range Versions() {
		if versions[v].name == s {
			return v, true
		}
	}
	return 0, false
}

// ParseSubprotocol returns the version a WebSocket subprotocol selects.
// The comparison is exact: RFC 6455 subprotocol tokens are case-sensitive.
func ParseSubprotocol(s string) (Version, bool) {
	for _, v := range Versions() {
		if versions[v].subprotocol == s {
			return v, true
		}
	}
	return 0, false
}

func itoa(n uint8) string {
	if n == 0 {
		return "0"
	}
	var b [3]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = '0' + n%10
		n /= 10
	}
	return string(b[i:])
}
