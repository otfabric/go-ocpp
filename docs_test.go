// SPDX-License-Identifier: MIT

package ocpp_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// apiDocs is where each recorded package is documented.
var apiDocs = map[string]string{
	"ocpp": "API.md", "ocppj": "API.md", "ocpptest": "API.md", "ocppcert": "API.md",
	"client": "API_CLIENT.md", "server": "API_SERVER.md",
}

var declName = regexp.MustCompile(`^(?:func (?:\([^)]*\) )?|type |const |var )([A-Z]\w*)`)

// TestDocsCoverAPI checks that every exported identifier recorded in
// testdata/api appears in its reference document, and every error sentinel
// in ERRORS.md: nothing exported goes undocumented.
func TestDocsCoverAPI(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	errorsDoc := read("ERRORS.md")
	for pkg, doc := range apiDocs {
		text := read(doc)
		for _, line := range strings.Split(read(filepath.Join("testdata", "api", pkg+".txt")), "\n") {
			m := declName.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			name := m[1]
			if !regexp.MustCompile(`\b` + name + `\b`).MatchString(text) {
				t.Errorf("%s.%s is not documented in %s", pkg, name, doc)
			}
			if strings.HasPrefix(name, "Err") && !strings.Contains(errorsDoc, name) {
				t.Errorf("%s.%s is not described in ERRORS.md", pkg, name)
			}
		}
	}
}
