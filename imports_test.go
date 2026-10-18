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
	".":                  {},
	"ocppj":              {module},
	"internal/transport": {"github.com/coder/websocket"},
	"internal/engine":    {module, module + "/ocppj", module + "/internal/transport"},
	"client":             {module, module + "/ocppj", module + "/internal/engine", module + "/internal/transport"},
	"server":             {module, module + "/ocppj", module + "/internal/engine", module + "/internal/transport"},
	// Version packages (from Phase 3): the root package and ocppj only.
	"ocpp16":  {module, module + "/ocppj"},
	"ocpp201": {module, module + "/ocppj"},
	"ocpp21":  {module, module + "/ocppj"},
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
