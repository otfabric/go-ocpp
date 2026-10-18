// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"net/http"
	"strconv"
)

// ConnectRequest is what an Authenticator decides on: the identity from
// the URL and the credentials the station presented.
type ConnectRequest struct {
	// StationID is the identity: the last path segment of the URL.
	StationID string
	// Username and Password are the HTTP Basic credentials, if any
	// (HasBasicAuth). Password never prints.
	Username     string
	Password     Secret
	HasBasicAuth bool
	// TLS is the connection state when the request arrived over TLS; for
	// security profile 3 its PeerCertificates hold the verified client
	// certificate chain.
	TLS        *tls.ConnectionState
	RemoteAddr string
	// Request is the HTTP request of the opening handshake, for anything
	// else an authenticator needs. It must not be retained.
	Request *http.Request
}

// Secret is a string that does not print: String and GoString return
// "[redacted]", so fmt's %v, %s, %q and %#v show that instead of the value.
// Convert it with string(s) to use the value.
type Secret string

// String returns "[redacted]".
func (Secret) String() string { return "[redacted]" }

// GoString returns "[redacted]".
func (Secret) GoString() string { return "[redacted]" }

// Authenticator decides whether a station may connect. It runs before the
// WebSocket upgrade. A nil error accepts; any error refuses with HTTP 401,
// and the error is logged but not sent.
type Authenticator interface {
	Authenticate(ctx context.Context, r ConnectRequest) error
}

// AuthenticatorFunc adapts a function to an Authenticator.
type AuthenticatorFunc func(ctx context.Context, r ConnectRequest) error

// Authenticate calls f.
func (f AuthenticatorFunc) Authenticate(ctx context.Context, r ConnectRequest) error {
	return f(ctx, r)
}

// StatusError lets an Authenticator refuse with an HTTP status of its own
// choice, for example 403 for a station that must use a higher security
// profile, or 503 while a backend it needs is down. Code must be 400–599;
// another code is treated as 401. Err is logged, not sent.
type StatusError struct {
	Code int
	Err  error
}

// Error implements error.
func (e *StatusError) Error() string {
	if e.Err == nil {
		return "HTTP " + strconv.Itoa(e.Code)
	}
	return "HTTP " + strconv.Itoa(e.Code) + ": " + e.Err.Error()
}

// Unwrap returns Err.
func (e *StatusError) Unwrap() error { return e.Err }

// ErrUnauthorized is what BasicAuth returns for refused credentials.
var ErrUnauthorized = errors.New("ocpp: unauthorized")

// ErrUnknownStation is what an Authenticator returns, wrapped or not, for
// an identity the CSMS does not recognise. The connection is then refused
// with HTTP 404, as OCPP-J recommends, instead of 401.
var ErrUnknownStation = errors.New("ocpp: unknown station")

// BasicAuth returns an Authenticator for security profiles 1 and 2: the
// station must send HTTP Basic credentials whose user name is its identity,
// with the password that lookup returns for that identity (1.6 Security
// Whitepaper A00.FR.204, .207, .324; 2.x Part 2 A00). Passwords are
// compared in constant time. An identity lookup does not know is refused
// with ErrUnknownStation (HTTP 404).
func BasicAuth(lookup func(ctx context.Context, stationID string) (password string, ok bool)) Authenticator {
	return AuthenticatorFunc(func(ctx context.Context, r ConnectRequest) error {
		want, known := lookup(ctx, r.StationID)
		if !known {
			return ErrUnknownStation
		}
		if !r.HasBasicAuth || r.Username != r.StationID || subtle.ConstantTimeCompare([]byte(want), []byte(r.Password)) != 1 {
			return ErrUnauthorized
		}
		return nil
	})
}

// CertificateAuth returns an Authenticator for security profile 3: the
// station must present a client certificate, which the TLS handshake has
// already verified against the server's ClientCAs, whose subject
// CommonName is its identity (2.x Part 2 A00; 1.6 Security Whitepaper).
// Otherwise it returns ErrUnauthorized.
func CertificateAuth() Authenticator {
	return AuthenticatorFunc(func(ctx context.Context, r ConnectRequest) error {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 ||
			r.TLS.PeerCertificates[0].Subject.CommonName != r.StationID {
			return ErrUnauthorized
		}
		return nil
	})
}

// AnyOf returns an Authenticator that accepts a station when one of auths
// accepts it, trying them in order: for a CSMS that admits stations with
// different security profiles, AnyOf(CertificateAuth(), BasicAuth(lookup)).
// When all refuse, it returns ErrUnknownStation (HTTP 404) if every one
// returned it, and otherwise the first other error: a station one of them
// might know is not told it is unknown.
func AnyOf(auths ...Authenticator) Authenticator {
	return AuthenticatorFunc(func(ctx context.Context, r ConnectRequest) error {
		var refused error
		for _, a := range auths {
			err := a.Authenticate(ctx, r)
			if err == nil {
				return nil
			}
			if refused == nil && !errors.Is(err, ErrUnknownStation) {
				refused = err
			}
		}
		switch {
		case refused != nil:
			return refused
		case len(auths) > 0:
			return ErrUnknownStation
		}
		return ErrUnauthorized
	})
}
