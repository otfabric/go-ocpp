// SPDX-License-Identifier: MIT

// Command gen generates the message types, validation, call functions,
// handler interfaces, dispatchers and coverage manifest of a version
// package from the OCA JSON schemas (ADR 0003). Each version package runs
// it from a generate directive, for example:
//
//	go run ../internal/gen -version 1.6 -package ocpp16 -schemas ../schemas/ocpp16/core,../schemas/ocpp16/security
package main

import (
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	version := flag.String("version", "", "OCPP version: 1.6, 2.0.1 or 2.1")
	pkg := flag.String("package", "", "package name")
	schemas := flag.String("schemas", "", "comma-separated schema directories")
	out := flag.String("out", ".", "output directory")
	flag.Parse()
	if err := run(*version, *pkg, strings.Split(*schemas, ","), *out); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(version, pkg string, dirs []string, out string) error {
	if version == "" || pkg == "" || len(dirs) == 0 || dirs[0] == "" {
		return fmt.Errorf("usage: gen -version V -package P -schemas DIR[,DIR] [-out DIR]")
	}
	m, err := loadVersion(version, pkg, dirs)
	if err != nil {
		return err
	}
	for name := range m.Types {
		typeNames[name] = true
	}
	for _, msg := range m.Messages {
		typeNames[msg.Request.Name] = true
		if msg.Response != nil {
			typeNames[msg.Response.Name] = true
		}
	}
	if err := checkNames(m); err != nil {
		return err
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	rel := func(p string) string {
		a, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		r, err := filepath.Rel(filepath.Dir(abs), a)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}
	files := map[string]string{
		"zz_generated_types.go":   emitTypes(m),
		"zz_generated_actions.go": emitActions(m),
	}
	for name, src := range files {
		formatted, err := format.Source([]byte(src))
		if err != nil {
			_ = os.WriteFile(filepath.Join(out, name+".broken"), []byte(src), 0o644)
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(out, name), formatted, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(out, "coverage.json"), []byte(emitCoverage(m, rel)), 0o644)
}

// checkNames fails when two generated identifiers would collide.
func checkNames(m *model) error {
	ids := map[string]string{}
	add := func(id, what string) error {
		if old, ok := ids[id]; ok {
			return fmt.Errorf("identifier %s is both %s and %s", id, old, what)
		}
		ids[id] = what
		return nil
	}
	for _, msg := range m.Messages {
		ids := map[string]string{msg.Action: "a call function", msg.Request.Name: "a request type"}
		if msg.Response != nil {
			ids[msg.Response.Name] = "a response type"
		}
		for id, what := range ids {
			if err := add(id, what); err != nil {
				return err
			}
		}
	}
	for _, t := range m.Types {
		if err := add(t.Name, "a type"); err != nil {
			return err
		}
		for _, v := range t.Values {
			if err := add(constName(t.Name, v), "a constant of "+t.Name); err != nil {
				return err
			}
		}
	}
	r := rolesOf(m.Version)
	for _, id := range []string{"Action", "Actions", r.station, r.csms, r.station + "Handler", r.csms + "Handler",
		"Unimplemented" + r.station + "Handler", "Unimplemented" + r.csms + "Handler", "ValidateRequest", "ValidateResponse"} {
		if err := add(id, "a fixed identifier"); err != nil {
			return err
		}
	}
	return nil
}
