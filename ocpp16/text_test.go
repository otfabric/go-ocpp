// SPDX-License-Identifier: MIT

package ocpp16

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp"
)

// A payload whose text is not well-formed (bytes that are not UTF-8, the
// escape of a surrogate without its pair) is refused with FormationViolation,
// as a request, as a response and by the dispatcher: encoding/json would
// decode it to U+FFFD. Well-formed text is accepted, U+FFFD included when
// the payload writes it.
func TestTextNotWellFormed(t *testing.T) {
	const request, response = `{"idTag":"A%sB"}`, `{"idTagInfo":{"status":"Accepted","parentIdTag":"A%sB"}}`
	d := CentralSystem(UnimplementedCentralSystemHandler{})
	peer := &fakePeer{v: ocpp.V16}
	for name, text := range map[string]string{
		"a byte that is not UTF-8":      "\xff",
		"a truncated UTF-8 sequence":    "\xe2\x82",
		"escape of a high surrogate":    `\ud83d`,
		"escape of a low surrogate":     `\ude00`,
		"low surrogate before high":     `\ude00\ud83d`,
		"high surrogate, then a letter": `\ud83dx`,
	} {
		var ve *ocpp.ValidationError
		req := json.RawMessage(strings.Replace(request, "%s", text, 1))
		if err := ValidateRequest("Authorize", req); !errors.As(err, &ve) || ve.Code != ocpp.FormationViolation {
			t.Errorf("%s: ValidateRequest(%q) = %v, want FormationViolation", name, req, err)
		}
		resp := json.RawMessage(strings.Replace(response, "%s", text, 1))
		if err := ValidateResponse("Authorize", resp); !errors.As(err, &ve) || ve.Code != ocpp.FormationViolation {
			t.Errorf("%s: ValidateResponse(%q) = %v, want FormationViolation", name, resp, err)
		}
		_, err := d.Dispatch(context.Background(), peer, "Authorize", req)
		if ce, ok := errors.AsType[*ocpp.CallError](err); !ok || ce.Code != ocpp.FormationViolation {
			t.Errorf("%s: Dispatch(%q) = %v, want CALLERROR FormationViolation", name, req, err)
		}
	}
	for name, text := range map[string]string{
		"an escaped pair":       `\ud83d\ude00`,
		"a four byte character": "\U0001F600",
		"U+FFFD literally":      "\uFFFD",
		"U+FFFD escaped":        `\ufffd`,
		"an escaped backslash":  `\\u`,
	} {
		if err := ValidateRequest("Authorize", json.RawMessage(strings.Replace(request, "%s", text, 1))); err != nil {
			t.Errorf("%s: request refused: %v", name, err)
		}
		if err := ValidateResponse("Authorize", json.RawMessage(strings.Replace(response, "%s", text, 1))); err != nil {
			t.Errorf("%s: response refused: %v", name, err)
		}
	}
}
