// SPDX-License-Identifier: MIT

package client

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// The WithHeader headers go only to the host of the URL and its
// subdomains: a redirect that keeps TLS but leaves the host does not carry
// them, and once a chain of redirects has left the host they stay removed.
// That is the rule net/http applies to Authorization.
func TestRedirectHeaders(t *testing.T) {
	got := make(chan http.Header, 1)
	// One loopback server answers for every name; its certificate covers
	// example.com, *.example.com and 127.0.0.1. ?next=a,b redirects to a,
	// then b; without next the request is recorded and refused.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := r.URL.Query().Get("next")
		if next == "" {
			got <- r.Header.Clone()
			http.Error(w, "no", http.StatusBadRequest)
			return
		}
		host, rest, _ := strings.Cut(next, ",")
		_, port, _ := net.SplitHostPort(r.Host)
		to := "https://" + net.JoinHostPort(host, port) + r.URL.Path
		if rest != "" {
			to += "?next=" + url.QueryEscape(rest)
		}
		http.Redirect(w, r, to, http.StatusTemporaryRedirect)
	}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}

	for _, tt := range []struct {
		next string
		kept bool
	}{
		{"example.com", true},
		{"csms.example.com", true},       // a subdomain
		{"EXAMPLE.com", true},            // case does not matter
		{"127.0.0.1", false},             // another host
		{"127.0.0.1,example.com", false}, // once left, stays removed
	} {
		t.Run(tt.next, func(t *testing.T) {
			c, err := New("wss://example.com:"+port+"/ocpp/CS1?next="+url.QueryEscape(tt.next),
				WithVersions(ocpp.V201), WithHTTPClient(&http.Client{Transport: tr}),
				WithBasicAuth("CS1", "pw"), WithHeader("X-Api-Key", "k1"), WithHeader("X-Tenant", "t1"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if he, ok := errors.AsType[*HandshakeError](c.Connect(context.Background())); !ok || he.StatusCode != http.StatusBadRequest {
				t.Fatalf("Connect did not reach the redirect target")
			}
			h := <-got
			names := []string{"X-Api-Key", "X-Tenant", "Authorization"}
			if tt.next == "EXAMPLE.com" {
				// Whether net/http folds case here differs between Go
				// releases (Go 1.26.0 does not); only the WithHeader
				// headers are this package's.
				names = names[:2]
			}
			for _, name := range names {
				if has := h.Get(name) != ""; has != tt.kept {
					t.Errorf("%s sent: %v, want %v", name, has, tt.kept)
				}
			}
		})
	}
}

func TestIsDomainOrSubdomain(t *testing.T) {
	for _, tt := range []struct {
		sub, parent string
		want        bool
	}{
		{"a.example.com", "example.com", true},
		{"example.com", "Example.COM", true},
		{"badexample.com", "example.com", false},
		{"example.com", "a.example.com", false},
		{"::1", "1", false},
		{"fe80::1%25en0", "en0", false},
	} {
		if got := isDomainOrSubdomain(tt.sub, tt.parent); got != tt.want {
			t.Errorf("isDomainOrSubdomain(%q, %q) = %v", tt.sub, tt.parent, got)
		}
	}
}
