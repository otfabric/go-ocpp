// SPDX-License-Identifier: MIT

// Package jsontext checks the text of a JSON document before it is decoded:
// package ocppj checks every frame with it, and internal/wire every payload
// that is decoded into a typed message.
package jsontext

import "unicode/utf8"

// Reason says what WellFormed refuses, for error messages.
const Reason = "text that is not well-formed Unicode: a byte sequence that is not UTF-8, or the escape of a surrogate without its pair"

// WellFormed reports whether data, a JSON text, is text that encoding/json
// decodes to itself: valid UTF-8 throughout (RFC 8259 §8.1), and in its
// strings no \u escape of a surrogate that is not half of a pair, which is
// the escape of a high surrogate (U+D800 to U+DBFF) followed at once by the
// escape of a low one (U+DC00 to U+DFFF).
//
// encoding/json accepts both and decodes them to U+FFFD, so that the string
// a handler receives would not be the text the peer sent (RFC 8259 §8.2
// leaves the second case to the receiver). go-ocpp refuses such a document
// instead of decoding it. The literal character U+FFFD and its own escape
// are well-formed.
//
// WellFormed does not allocate. data is expected to be valid JSON; for
// anything else the result is unspecified.
func WellFormed(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case !inString:
			inString = c == '"'
		case c == '"':
			inString = false
		case c == '\\':
			i++ // the escaped character
			if i >= len(data) || data[i] != 'u' {
				continue
			}
			u, ok := hex4(data, i+1)
			if !ok {
				return false
			}
			i += 4
			switch {
			case u >= 0xDC00 && u <= 0xDFFF:
				return false // a low surrogate that follows no high one
			case u >= 0xD800 && u <= 0xDBFF:
				if i+2 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
					return false
				}
				low, ok := hex4(data, i+3)
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return false
				}
				i += 6
			}
		}
	}
	return true
}

// hex4 reads the four hexadecimal digits at data[i:i+4].
func hex4(data []byte, i int) (uint16, bool) {
	if i < 0 || len(data)-i < 4 {
		return 0, false
	}
	var u uint16
	for _, c := range data[i : i+4] {
		switch {
		case c >= '0' && c <= '9':
			u = u<<4 | uint16(c-'0')
		case c >= 'a' && c <= 'f':
			u = u<<4 | uint16(c-'a'+10)
		case c >= 'A' && c <= 'F':
			u = u<<4 | uint16(c-'A'+10)
		default:
			return 0, false
		}
	}
	return u, true
}
