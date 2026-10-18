// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// FuzzHandshake sends arbitrary upgrade requests (path, subprotocols,
// Authorization header) to a server with Basic authentication. It must
// never panic, never admit a station without the right password, and
// answer every refusal with an HTTP error.
func FuzzHandshake(f *testing.F) {
	for _, s := range [][3]string{
		{"/ocpp/CS1", "ocpp1.6", "CS1:secret"},
		{"/ocpp/CS1", "ocpp2.0.1, ocpp2.1", "CS1:wrong"},
		{"/ocpp/", "ocpp1.6", ""},
		{"/ocpp/%2F%00", "x", "CS1"},
		{"/ocpp/CS%201", "OCPP1.6", "CS 1:secret"},
		{"/a/b/c/" + string(make([]byte, 60)), "", ":"},
	} {
		f.Add(s[0], s[1], s[2], true)
	}
	echo := ocpp.DispatcherFunc(func(context.Context, ocpp.Peer, string, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	srv, err := New(WithHandler(ocpp.V16, echo), WithHandler(ocpp.V201, echo), WithHandler(ocpp.V21, echo),
		WithAuthenticator(BasicAuth(func(ctx context.Context, id string) (string, bool) { return "secret", id == "CS1" })))
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, path, protocols, userpass string, sendAuth bool) {
		u, err := url.Parse("http://csms.example" + path)
		if err != nil || u.Host != "csms.example" {
			return
		}
		r := httptest.NewRequest(http.MethodGet, "http://csms.example/", nil)
		r.URL = u
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		r.Header.Set("Sec-WebSocket-Protocol", protocols)
		if sendAuth {
			r.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(userpass)))
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r) // the recorder cannot be hijacked: an accepted upgrade fails there

		id, idOK := stationID(u)
		user, pass, _ := r.BasicAuth()
		authorized := idOK && id == "CS1" && user == "CS1" && pass == "secret"
		if idOK && !validIdentity(id) && w.Code < 400 {
			t.Fatalf("identity %q admitted", id)
		}
		if !authorized && (w.Code < 400 || w.Code >= 500) {
			t.Fatalf("path %q user %q: answered %d without the right credentials", path, user, w.Code)
		}
		if len(srv.Stations()) != 0 {
			t.Fatal("a session was registered without a WebSocket")
		}
	})
}

func TestValidIdentity(t *testing.T) {
	for id, want := range map[string]bool{
		"CS001": true, "RDAM|123": true, "a*-_=+|@.Z9": true, string(make([]byte, 48)): false,
		"": false, "CS:1": false, "RDAM 123": false, "é": false, "a/b": false,
		"123456789012345678901234567890123456789012345678":  true,
		"1234567890123456789012345678901234567890123456789": false,
	} {
		if validIdentity(id) != want {
			t.Errorf("validIdentity(%q) = %v", id, !want)
		}
	}
}
