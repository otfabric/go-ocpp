// SPDX-License-Identifier: MIT

package ocpp_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/otfabric/go-ocpp"

// allowedImports lists, per package, the module and third-party packages
// its non-test files may import (ARCHITECTURE.md, "Packages and import
// rules"). The standard library is always allowed. A package not listed may
// import nothing outside the standard library and the module root.
var allowedImports = map[string][]string{
	".":                  {module + "/internal/after"},
	"internal/after":     {},
	"ocppj":              {module},
	"internal/transport": {"github.com/coder/websocket"},
	"internal/engine":    {module, module + "/ocppj", module + "/internal/transport", module + "/internal/after"},
	"client":             {module, module + "/ocppj", module + "/internal/engine", module + "/internal/transport"},
	"server":             {module, module + "/ocppj", module + "/internal/engine", module + "/internal/transport"},
	// The test kit: a real client and server, nothing internal.
	"ocpptest": {module, module + "/client", module + "/server"},
	// Version packages: the root package and their runtime, internal/wire.
	"ocpp16":        {module, module + "/internal/wire"},
	"ocpp201":       {module, module + "/internal/wire"},
	"ocpp21":        {module, module + "/internal/wire"},
	"internal/wire": {module, module + "/ocppj"},
	// The schema conformance suite, imported by the version packages' tests
	// only: a reference JSON Schema validator (ADR 0003).
	"internal/schematest": {module, "github.com/santhosh-tekuri/jsonschema/v6"},
}

func TestImportRules(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "examples" ||
				name == "e2e" || name == "interop" || name == "docs") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		allowed, listed := allowedImports[dir]
		if !listed {
			allowed = []string{module}
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !strings.Contains(strings.Split(p, "/")[0], ".") {
				continue // standard library
			}
			ok := false
			for _, a := range allowed {
				ok = ok || p == a
			}
			if !ok {
				t.Errorf("%s imports %s, which package %s may not import", path, p, dir)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
