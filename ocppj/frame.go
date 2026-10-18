// SPDX-License-Identifier: MIT

package ocppj

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/otfabric/go-ocpp"
)

// MessageType is the first element of a frame.
type MessageType uint8

// The message types.
const (
	Call            MessageType = 2
	CallResult      MessageType = 3
	CallError       MessageType = 4
	CallResultError MessageType = 5 // OCPP 2.1 only
	Send            MessageType = 6 // OCPP 2.1 only
)

// String returns the name the specifications use, e.g. "CALLRESULT".
func (t MessageType) String() string {
	switch t {
	case Call:
		return "CALL"
	case CallResult:
		return "CALLRESULT"
	case CallError:
		return "CALLERROR"
	case CallResultError:
		return "CALLRESULTERROR"
	case Send:
		return "SEND"
	default:
		return "MessageType(" + strconv.Itoa(int(t)) + ")"
	}
}

// Allowed reports whether frames of type t may be exchanged under OCPP
// version v. Types 5 and 6 exist in OCPP 2.1 only.
func (t MessageType) Allowed(v ocpp.Version) bool {
	switch t {
	case Call, CallResult, CallError:
		return v.Valid()
	case CallResultError, Send:
		return v == ocpp.V21
	default:
		return false
	}
}

// MaxMessageIDLength is the maximum length of a message ID, in characters
// (OCPP-J 1.6 §4.1.3; OCPP 2.x Part 4 §4.1.4).
const MaxMessageIDLength = 36

// Frame is one decoded OCPP-J frame. Which fields are set depends on Type.
type Frame struct {
	Type MessageType
	ID   string

	// Action is set for Call and Send.
	Action string
	// Payload is the JSON object of a Call, CallResult or Send.
	Payload json.RawMessage

	// ErrorCode, Description and Details are set for CallError and
	// CallResultError. Details is a JSON object.
	ErrorCode   ocpp.ErrorCode
	Description string
	Details     json.RawMessage
}

// Limits bound what Decode accepts.
type Limits struct {
	// MaxFrameSize is the maximum size of a frame in bytes, checked before
	// parsing. Zero means DefaultMaxFrameSize.
	MaxFrameSize int
}

// DefaultMaxFrameSize is the frame size limit when Limits.MaxFrameSize is
// zero. The specifications set no limit; this is an implementation choice
// (ARCHITECTURE.md), large enough for the largest standard messages.
const DefaultMaxFrameSize = 64 << 10

func (l Limits) maxFrameSize() int {
	if l.MaxFrameSize > 0 {
		return l.MaxFrameSize
	}
	return DefaultMaxFrameSize
}

// ErrMalformed is wrapped by every Decode error.
var ErrMalformed = errors.New("ocppj: malformed frame")

// DecodeError is the error Decode returns. It says how far decoding got, so
// that the engine can decide whether the frame can be answered.
type DecodeError struct {
	// Type is the message type, if it was read; zero otherwise.
	Type MessageType
	// ID is the message ID, if it was read. IDKnown distinguishes an empty
	// ID from one that could not be read.
	ID      string
	IDKnown bool
	// UnknownType is true when the frame's message type is not one the
	// version allows (see Allowed).
	UnknownType bool
	// Reason describes the violation.
	Reason string
}

// Error implements error.
func (e *DecodeError) Error() string {
	if e.IDKnown {
		return fmt.Sprintf("ocppj: malformed frame (message %q): %s", e.ID, e.Reason)
	}
	return "ocppj: malformed frame: " + e.Reason
}

// Unwrap returns ErrMalformed.
func (e *DecodeError) Unwrap() error { return ErrMalformed }

// Decode parses one frame received on a connection of version v.
//
// It is strict: the frame must be a JSON array with exactly the elements
// its type defines, the message type a JSON integer the version allows, the
// message ID a string of at most MaxMessageIDLength characters, payloads and
// error details JSON objects, and nothing may follow the array.
func Decode(data []byte, v ocpp.Version, l Limits) (Frame, error) {
	if len(data) > l.maxFrameSize() {
		return Frame{}, &DecodeError{Reason: fmt.Sprintf("frame of %d bytes exceeds the limit of %d", len(data), l.maxFrameSize())}
	}
	var elems []json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&elems); err != nil {
		return Frame{}, &DecodeError{Reason: "not a JSON array: " + err.Error()}
	}
	if _, err := dec.Token(); err != io.EOF {
		return Frame{}, &DecodeError{Reason: "data after the JSON array"}
	}
	if elems == nil {
		return Frame{}, &DecodeError{Reason: "not a JSON array"}
	}
	if len(elems) < 2 {
		return Frame{}, &DecodeError{Reason: fmt.Sprintf("%d elements; a frame has at least 2", len(elems))}
	}

	t, ok := parseType(elems[0])
	derr := &DecodeError{Type: t}
	if id, ok := parseString(elems[1]); ok {
		derr.ID, derr.IDKnown = id, true
	}
	if !ok {
		derr.Type = 0
		derr.Reason = "message type is not an integer"
		return Frame{}, derr
	}
	if !t.Allowed(v) {
		derr.UnknownType = true
		derr.Reason = fmt.Sprintf("message type %d is not defined in OCPP %s", int(t), v)
		return Frame{}, derr
	}
	if !derr.IDKnown {
		derr.Reason = "message ID is not a string"
		return Frame{}, derr
	}
	if n := utf8.RuneCountInString(derr.ID); n > MaxMessageIDLength {
		derr.IDKnown = false
		derr.ID = ""
		derr.Reason = fmt.Sprintf("message ID of %d characters exceeds %d", n, MaxMessageIDLength)
		return Frame{}, derr
	}

	f := Frame{Type: t, ID: derr.ID}
	want := map[MessageType]int{Call: 4, CallResult: 3, CallError: 5, CallResultError: 5, Send: 4}[t]
	if len(elems) != want {
		derr.Reason = fmt.Sprintf("%s has %d elements, want %d", t, len(elems), want)
		return Frame{}, derr
	}
	switch t {
	case Call, Send:
		action, ok := parseString(elems[2])
		if !ok || action == "" {
			derr.Reason = "action is not a non-empty string"
			return Frame{}, derr
		}
		if !isObject(elems[3]) {
			derr.Reason = "payload is not a JSON object"
			return Frame{}, derr
		}
		f.Action, f.Payload = action, elems[3]
	case CallResult:
		if !isObject(elems[2]) {
			derr.Reason = "payload is not a JSON object"
			return Frame{}, derr
		}
		f.Payload = elems[2]
	case CallError, CallResultError:
		code, ok := parseString(elems[2])
		if !ok || code == "" {
			derr.Reason = "error code is not a non-empty string"
			return Frame{}, derr
		}
		desc, ok := parseString(elems[3])
		if !ok {
			derr.Reason = "error description is not a string"
			return Frame{}, derr
		}
		if !isObject(elems[4]) {
			derr.Reason = "error details are not a JSON object"
			return Frame{}, derr
		}
		f.ErrorCode, f.Description, f.Details = ocpp.ErrorCode(code), desc, elems[4]
	}
	return f, nil
}

// Encode writes f as a frame. Nil payloads and details are written as {}.
// It checks what Decode would reject for any version, except that the
// version-dependent checks (message type, error code) are left to the
// caller.
func Encode(f Frame) ([]byte, error) {
	if utf8.RuneCountInString(f.ID) > MaxMessageIDLength {
		return nil, fmt.Errorf("ocppj: message ID %q exceeds %d characters", f.ID, MaxMessageIDLength)
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	buf.WriteString(strconv.Itoa(int(f.Type)))
	buf.WriteByte(',')
	appendString(&buf, f.ID)
	var err error
	switch f.Type {
	case Call, Send:
		if f.Action == "" {
			return nil, errors.New("ocppj: empty action")
		}
		buf.WriteByte(',')
		appendString(&buf, f.Action)
		err = appendObject(&buf, f.Payload, "payload")
	case CallResult:
		err = appendObject(&buf, f.Payload, "payload")
	case CallError, CallResultError:
		if f.ErrorCode == "" {
			return nil, errors.New("ocppj: empty error code")
		}
		buf.WriteByte(',')
		appendString(&buf, string(f.ErrorCode))
		buf.WriteByte(',')
		appendString(&buf, f.Description)
		err = appendObject(&buf, f.Details, "error details")
	default:
		return nil, fmt.Errorf("ocppj: cannot encode %s", f.Type)
	}
	if err != nil {
		return nil, err
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// appendString writes s as a JSON string, without HTML escaping. Encoding
// a string cannot fail.
func appendString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	buf.Truncate(buf.Len() - 1) // the newline Encode adds
}

// appendObject writes ",", then m compacted, or {} for an empty m. m must
// be a JSON object.
func appendObject(buf *bytes.Buffer, m json.RawMessage, what string) error {
	buf.WriteByte(',')
	if len(m) == 0 {
		buf.WriteString("{}")
		return nil
	}
	n := buf.Len()
	if !isObject(m) || json.Compact(buf, m) != nil {
		buf.Truncate(n)
		return fmt.Errorf("ocppj: %s is not a JSON object", what)
	}
	return nil
}

// parseType accepts a JSON integer only: 2, not 2.0, "2" or 2e0. Values
// above 255 are not message types; they are refused, not truncated.
func parseType(raw json.RawMessage) (MessageType, bool) {
	s := bytes.TrimSpace(raw)
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' || n > 255 {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return MessageType(n), len(s) > 0 && n <= 255
}

// parseString accepts a JSON string only. The frame as a whole is valid JSON
// when it is called, so only the type needs checking; null, which would
// unmarshal into a string without error, is refused by the quote check.
func parseString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var s string
	err := json.Unmarshal(raw, &s)
	return s, err == nil
}

func isObject(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) >= 2 && raw[0] == '{'
}
