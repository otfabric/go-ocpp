// SPDX-License-Identifier: MIT

package server

import (
	"bytes"
	"encoding"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// xmlForm holds a Secret as an element and as an attribute.
type xmlForm struct {
	Attr     Secret `xml:"attr,attr,omitempty"`
	Password Secret `xml:",omitempty"`
}

// A Secret writes "[redacted]" wherever it is formatted, marshalled or
// logged, on its own, through a pointer and inside a ConnectRequest; only
// string(s) gives the value, and decoding into a Secret is unchanged.
func TestSecretIsRedacted(t *testing.T) {
	const value = "pass-0123456789"
	s := Secret(value)
	req := ConnectRequest{StationID: "CS1", Username: "CS1", Password: s, HasBasicAuth: true, RemoteAddr: "192.0.2.1:1234"}
	// The request as an application hands it to an encoder: Request is
	// nil, so encoding/json writes null for it.
	var anyReq any = req
	marshal := func(f func() ([]byte, error)) func() string {
		return func() string {
			b, err := f()
			if err != nil {
				return "marshal: " + err.Error()
			}
			return string(b)
		}
	}
	logged := func(json bool, args ...any) func() string {
		return func() string {
			var b bytes.Buffer
			var h slog.Handler = slog.NewTextHandler(&b, nil)
			if json {
				h = slog.NewJSONHandler(&b, nil)
			}
			slog.New(h).Info("authenticate", args...)
			return b.String()
		}
	}
	for name, out := range map[string]func() string{
		"fmt %v":                      func() string { return fmt.Sprintf("%v", s) },
		"fmt %+v":                     func() string { return fmt.Sprintf("%+v", s) },
		"fmt %s":                      func() string { return fmt.Sprintf("password=%s", s) },
		"fmt %q":                      func() string { return fmt.Sprintf("%q", s) },
		"fmt %#v":                     func() string { return fmt.Sprintf("%#v", s) },
		"fmt %v of the request":       func() string { return fmt.Sprintf("%v", req) },
		"fmt %+v of the request":      func() string { return fmt.Sprintf("%+v", &req) },
		"fmt %#v of the request":      func() string { return fmt.Sprintf("%#v", req) },
		"json.Marshal":                marshal(func() ([]byte, error) { return json.Marshal(s) }),
		"json.Marshal of a pointer":   marshal(func() ([]byte, error) { return json.Marshal(&s) }),
		"json.Marshal of the request": marshal(func() ([]byte, error) { return json.Marshal(anyReq) }),
		"json.Marshal of a slice":     marshal(func() ([]byte, error) { return json.Marshal([]Secret{s}) }),
		"MarshalText": marshal(func() ([]byte, error) {
			if tm, ok := any(s).(encoding.TextMarshaler); ok {
				return tm.MarshalText()
			}
			return []byte("not an encoding.TextMarshaler"), nil
		}),
		"xml.Marshal":                  marshal(func() ([]byte, error) { return xml.Marshal(xmlForm{Password: s}) }),
		"xml.Marshal of an attribute":  marshal(func() ([]byte, error) { return xml.Marshal(xmlForm{Attr: s}) }),
		"slog text, the value":         logged(false, "password", s),
		"slog text, a pointer":         logged(false, "password", &s),
		"slog text, the request":       logged(false, "request", req),
		"slog text, a group":           logged(false, slog.Group("basic", "password", s)),
		"slog JSON, the value":         logged(true, "password", s),
		"slog JSON, a pointer":         logged(true, "password", &s),
		"slog JSON, the request":       logged(true, "request", req),
		"slog JSON, a request pointer": logged(true, "request", &req),
		"slog JSON, a group":           logged(true, slog.Group("basic", "password", s)),
	} {
		got := out()
		if strings.Contains(got, value) || !strings.Contains(got, "[redacted]") {
			t.Errorf("%s: %s, want [redacted] and not the value", name, got)
		}
	}

	if got := string(s); got != value {
		t.Errorf("string(s) = %q, want the value", got)
	}
	var back ConnectRequest
	if err := json.Unmarshal([]byte(`{"Password":"`+value+`"}`), &back); err != nil || string(back.Password) != value {
		t.Errorf("json.Unmarshal into a Secret: %q, %v; want the value", string(back.Password), err)
	}
}
