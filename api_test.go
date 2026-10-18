// SPDX-License-Identifier: MIT

package ocpp_test

import (
	"bytes"
	"flag"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var updateAPI = flag.Bool("update", false, "rewrite testdata/api from the current exported API")

// The packages whose exported API is recorded. The version packages are
// generated from the schemas and recorded in their coverage manifests.
var apiPackages = map[string]string{"ocpp": ".", "ocppj": "ocppj", "client": "client", "server": "server"}

// TestAPI compares the exported API of the core packages with
// testdata/api, so that every change to it shows in review and in the
// release notes. After an intended change: go test -run TestAPI -update.
func TestAPI(t *testing.T) {
	for name, dir := range apiPackages {
		t.Run(name, func(t *testing.T) {
			got := exportedAPI(t, dir)
			golden := filepath.Join("testdata", "api", name+".txt")
			if *updateAPI {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -run TestAPI -update)", err)
			}
			if got != string(want) {
				t.Errorf("the exported API of %s changed; if intended, run go test -run TestAPI -update and note it in RELEASE.md\n%s",
					name, lineDiff(string(want), got))
			}
		})
	}
}

// exportedAPI lists the exported declarations of the package in dir, one
// per line (types in full, with unexported fields left out), sorted.
func exportedAPI(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var lines []string
	src := func(node any) string {
		var b bytes.Buffer
		if err := printer.Fprint(&b, fset, node); err != nil {
			t.Fatal(err)
		}
		return strings.Join(strings.Fields(b.String()), " ")
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() || (d.Recv != nil && !exportedRecv(d.Recv)) {
					continue
				}
				d.Body, d.Doc = nil, nil
				lines = append(lines, src(d))
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						s.Doc, s.Comment = nil, nil
						dropUnexported(s.Type)
						lines = append(lines, "type "+src(s))
					case *ast.ValueSpec:
						for i, id := range s.Names {
							if !id.IsExported() {
								continue
							}
							line := d.Tok.String() + " " + id.Name
							if s.Type != nil {
								line += " " + src(s.Type)
							}
							if d.Tok == token.CONST && i < len(s.Values) {
								line += " = " + src(s.Values[i])
							}
							lines = append(lines, line)
						}
					}
				}
			}
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func exportedRecv(fl *ast.FieldList) bool {
	typ := fl.List[0].Type
	if s, ok := typ.(*ast.StarExpr); ok {
		typ = s.X
	}
	if ix, ok := typ.(*ast.IndexExpr); ok {
		typ = ix.X
	}
	id, ok := typ.(*ast.Ident)
	return ok && id.IsExported()
}

// dropUnexported removes unexported fields and methods, and comments.
func dropUnexported(typ ast.Expr) {
	var list *ast.FieldList
	switch t := typ.(type) {
	case *ast.StructType:
		list = t.Fields
	case *ast.InterfaceType:
		list = t.Methods
	default:
		return
	}
	kept := list.List[:0]
	for _, f := range list.List {
		f.Doc, f.Comment = nil, nil
		if len(f.Names) == 0 { // embedded
			kept = append(kept, f)
			continue
		}
		names := f.Names[:0]
		for _, n := range f.Names {
			if n.IsExported() {
				names = append(names, n)
			}
		}
		if f.Names = names; len(names) > 0 {
			kept = append(kept, f)
		}
	}
	list.List = kept
}

// lineDiff lists the lines only in want (-) and only in got (+).
func lineDiff(want, got string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(s, "\n") {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var b strings.Builder
	for _, l := range strings.Split(want, "\n") {
		if !g[l] {
			b.WriteString("- " + l + "\n")
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if !w[l] {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
