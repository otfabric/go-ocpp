// SPDX-License-Identifier: MIT

package schematest

import (
	"bytes"
	"encoding/json"
	"strings"
)

// replacement is U+FFFD, which encoding/json writes for what it cannot
// decode.
const replacement = "\uFFFD"

// TextReplaced reports whether encoding/json decodes the strings of data, a
// valid JSON text, to something other than what data holds: whether the
// decoded strings, property names included, hold more U+FFFD characters
// than data writes, literally or as the escape \uFFFD.
//
// It is the reference the library's text check (internal/jsontext, through
// ocppj.Decode and the version packages' validation) is compared with, and
// is written without it: it decodes with encoding/json and counts.
func TextReplaced(data []byte) bool {
	decoded := 0
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if s, ok := tok.(string); ok {
			decoded += strings.Count(s, replacement)
		}
	}
	// In valid JSON a backslash stands in a string only, and starts an
	// escape: every second character after one is skipped.
	written := bytes.Count(data, []byte(replacement))
	for i := 0; i+1 < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if data[i+1] == 'u' && len(data)-i >= 6 && strings.EqualFold(string(data[i+2:i+6]), "fffd") {
			written++
		}
		i++
	}
	return decoded != written
}
