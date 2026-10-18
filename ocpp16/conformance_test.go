// SPDX-License-Identifier: MIT

package ocpp16

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/otfabric/go-ocpp"
	"github.com/otfabric/go-ocpp/internal/schematest"
)

const (
	testVersion    = ocpp.V16
	testSchemaDirs = "../schemas/ocpp16/core ../schemas/ocpp16/security"
)

type manifest struct {
	Version string `json:"version"`
	Actions []struct {
		Action, From, Request, Response, RequestSchema, ResponseSchema string
		Typed, Validated, Dispatchable                                 bool
	} `json:"actions"`
}

func readManifest(t testing.TB) manifest {
	t.Helper()
	data, err := os.ReadFile("coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestCoverageManifest checks that coverage.json lists every action of the
// schema directories, and only those, as Actions does (REQUIREMENTS.md R6).
func TestCoverageManifest(t *testing.T) {
	m := readManifest(t)
	if m.Version != testVersion.String() {
		t.Fatalf("manifest version %q", m.Version)
	}
	var fromSchemas []string
	for _, dir := range strings.Fields(testSchemaDirs) {
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			base := strings.TrimSuffix(filepath.Base(f), ".json")
			if strings.HasSuffix(base, "Response") {
				continue
			}
			fromSchemas = append(fromSchemas, strings.TrimSuffix(base, "Request"))
		}
	}
	sort.Strings(fromSchemas)
	var listed, table []string
	for _, a := range m.Actions {
		listed = append(listed, a.Action)
		if !a.Typed || !a.Validated || !a.Dispatchable {
			t.Errorf("%s is not fully implemented in the manifest", a.Action)
		}
	}
	for _, a := range Actions {
		table = append(table, a.Name)
	}
	if strings.Join(listed, ",") != strings.Join(fromSchemas, ",") {
		t.Errorf("manifest lists %v,\nthe schemas define %v", listed, fromSchemas)
	}
	if strings.Join(table, ",") != strings.Join(listed, ",") {
		t.Errorf("Actions lists %v, the manifest %v", table, listed)
	}
}

// TestSchemaConformance checks every message against its schema and the
// reference validator (ADR 0003).
func TestSchemaConformance(t *testing.T) {
	m := readManifest(t)
	files := map[string][2]string{}
	var actions []string
	for _, a := range m.Actions {
		files[a.Action] = [2]string{filepath.Join("..", a.RequestSchema), filepath.Join("..", a.ResponseSchema)}
		actions = append(actions, a.Action)
	}
	schematest.Run(t, schematest.Config{
		Version: testVersion,
		Actions: actions,
		Files:   func(action string) (string, string) { return files[action][0], files[action][1] },
		Validate: func(action string, request bool, payload json.RawMessage) error {
			if request {
				return ValidateRequest(action, payload)
			}
			return ValidateResponse(action, payload)
		},
		RoundTrip: func(action string, request bool, payload json.RawMessage) (json.RawMessage, error) {
			msg := newResponse(action)
			if request {
				msg = newRequest(action)
			}
			if err := json.Unmarshal(payload, msg); err != nil {
				return nil, err
			}
			return json.Marshal(msg)
		},
	})
}

// otherVersion is a version these messages do not belong to.
const otherVersion = ocpp.V201
