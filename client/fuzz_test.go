// SPDX-License-Identifier: MIT

package client

import (
	"net/url"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// FuzzNew checks that New never panics on a URL, a header and Basic
// credentials from configuration (a CSMS can set them on a station:
// SetNetworkProfile, ChangeConfiguration), and that what it accepts holds:
// a ws or wss URL, ws only when allowed, no credentials in the URL, the
// identity the last path segment, no header that could split the request.
func FuzzNew(f *testing.F) {
	for _, u := range []string{
		"wss://csms.example/ocpp/CS1", "ws://127.0.0.1:9000/CS%201", "wss://h/ocpp/", "wss://u:p@h/ocpp/CS1",
		"http://h/CS1", "wss://h/a/b%2Fc", "wss://h/%zz", "wss://[::1]:1/x?y=1#z", "", "::",
	} {
		f.Add(u, "X-Tenant", "t1", "CS1", false)
		f.Add(u, "Authorization", "x\r\ny", "other", true)
	}
	f.Fuzz(func(t *testing.T, raw, name, value, user string, insecure bool) {
		opts := []Option{WithVersions(ocpp.V16), WithHeader(name, value)}
		if insecure {
			opts = append(opts, WithInsecureTransport())
		}
		if user != "" {
			opts = append(opts, WithBasicAuth(user, "pw"))
		}
		c, err := New(raw, opts...)
		if err != nil {
			return
		}
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || (u.Scheme != "wss" && (u.Scheme != "ws" || !insecure)) {
			t.Fatalf("accepted %q (insecure %v)", raw, insecure)
		}
		p := u.EscapedPath()
		if id, _ := url.PathUnescape(p[strings.LastIndex(p, "/")+1:]); c.ID() == "" || c.ID() != id {
			t.Fatalf("identity %q of %q", c.ID(), raw)
		}
		if user != "" && (user != c.ID() || strings.Contains(user, ":")) {
			t.Fatalf("user %q accepted for identity %q", user, c.ID())
		}
		for k, vs := range c.header {
			for _, v := range vs {
				if strings.ContainsAny(k+v, "\r\n\x00") || strings.ContainsAny(k, " :") {
					t.Fatalf("header %q: %q", k, v)
				}
			}
		}
	})
}
