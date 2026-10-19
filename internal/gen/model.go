// SPDX-License-Identifier: MIT

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// schema is the part of JSON Schema (draft-04 and draft-06) the OCA
// schemas use. load rejects anything else, so that a new keyword cannot be
// ignored silently.
type schema struct {
	Schema               string             `json:"$schema"`
	ID                   string             `json:"id"`
	DollarID             string             `json:"$id"`
	Ref                  string             `json:"$ref"`
	Title                string             `json:"title"`
	Comment              string             `json:"comment"`
	Description          string             `json:"description"`
	JavaType             string             `json:"javaType"`
	Type                 string             `json:"type"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	AdditionalProperties *bool              `json:"additionalProperties"`
	Definitions          map[string]*schema `json:"definitions"`
	Enum                 []string           `json:"enum"`
	MaxLength            *int               `json:"maxLength"`
	Format               string             `json:"format"`
	Items                *schema            `json:"items"`
	AdditionalItems      *bool              `json:"additionalItems"`
	MinItems             *int               `json:"minItems"`
	MaxItems             *int               `json:"maxItems"`
	Minimum              *float64           `json:"minimum"`
	Maximum              *float64           `json:"maximum"`
	MultipleOf           *float64           `json:"multipleOf"`
	Default              any                `json:"default"`
}

var knownKeywords = map[string]bool{}

func init() {
	for _, k := range []string{"$schema", "id", "$id", "$ref", "title", "comment", "description", "javaType", "type",
		"properties", "required", "additionalProperties", "definitions", "enum", "maxLength", "format", "items",
		"additionalItems", "minItems", "maxItems", "minimum", "maximum", "multipleOf", "default"} {
		knownKeywords[k] = true
	}
}

// checkKeywords fails on a keyword this generator does not implement.
func checkKeywords(raw any, where string) error {
	switch n := raw.(type) {
	case map[string]any:
		for k, v := range n {
			if !knownKeywords[k] {
				return fmt.Errorf("%s: keyword %q is not supported by the generator", where, k)
			}
			switch k {
			case "properties", "definitions":
				for name, sub := range v.(map[string]any) {
					if err := checkKeywords(sub, where+"/"+k+"/"+name); err != nil {
						return err
					}
				}
			case "items":
				if err := checkKeywords(v, where+"/items"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Kind of a Go type.
type kind int

const (
	kindStruct kind = iota
	kindEnum
)

// goType is a named type of the generated package.
type goType struct {
	Name       string
	Kind       kind
	Doc        string
	Fields     []*field // structs
	Values     []string // enums
	Strict     bool     // additionalProperties false
	CustomData bool     // the 2.x CustomDataType: kept open
	VendorMax  int      // CustomDataType: maxLength of vendorId
	// Draft04 is set for a type of a JSON Schema draft-04 file (OCPP 1.6
	// core), in which an integer has no fraction and no exponent; in draft-06
	// (1.6 Security Whitepaper, 2.x) 20000.0 is an integer too. A type that
	// both define keeps draft-04's rule.
	Draft04 bool
	key     string // canonical definition, to detect conflicts
}

// ref is the type of a field or of array elements.
type ref struct {
	Base   string // "string", "int", "float64", "bool", "time", or a generated type name
	Named  *goType
	Format string // "uri"
	// Constraints of scalars.
	MaxLength                    *int
	Minimum, Maximum, MultipleOf *float64
}

type field struct {
	JSON     string
	Name     string
	Doc      string
	Required bool
	Array    bool
	MinItems int
	MaxItems int // -1: no maximum
	Elem     ref
}

// message is one action.
type message struct {
	Action       string
	From         string // fromStation, fromCSMS, fromBoth
	Send         bool   // a SEND (OCPP 2.1): no response, Response is nil
	Request      *goType
	Response     *goType
	RequestFile  string
	ResponseFile string
	RequestHash  string
	ResponseHash string
}

// model is everything generated for one version.
type model struct {
	Version  string
	Package  string
	Messages []*message
	Types    map[string]*goType
}

// loadVersion reads the schema files of a version: name.json/nameResponse.json
// (1.6) or nameRequest.json/nameResponse.json (2.x).
func loadVersion(version, pkg string, dirs []string) (*model, error) {
	m := &model{Version: version, Package: pkg, Types: map[string]*goType{}}
	dir16 := version == "1.6"
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			return nil, err
		}
		sort.Strings(files)
		for _, f := range files {
			base := strings.TrimSuffix(filepath.Base(f), ".json")
			if strings.HasSuffix(base, "Response") {
				continue
			}
			action := strings.TrimSuffix(base, "Request")
			respFile := filepath.Join(dir, action+"Response.json")
			_, statErr := os.Stat(respFile)
			// A 2.1 SEND, NotifyPeriodicEventStream, has one schema named
			// after the action and no response.
			send := version == "2.1" && action == base && statErr != nil
			if !dir16 && action == base && !send {
				return nil, fmt.Errorf("%s: not a Request or Response schema", f)
			}
			msg := &message{Action: action, Send: send}
			from, ok := directions[version][action]
			if !ok {
				return nil, fmt.Errorf("%s: no direction known for %s in OCPP %s (directions.go)", f, action, version)
			}
			msg.From = from
			if statErr != nil && !send {
				return nil, fmt.Errorf("%s: no response schema %s", f, respFile)
			}
			gen := &generator{m: m, version: version}
			if msg.Request, msg.RequestHash, err = gen.message(f, action+"Request", base); err != nil {
				return nil, err
			}
			msg.RequestFile = f
			if !send {
				if msg.Response, msg.ResponseHash, err = gen.message(respFile, action+"Response", action+"Response"); err != nil {
					return nil, err
				}
				msg.ResponseFile = respFile
			}
			m.Messages = append(m.Messages, msg)
		}
	}
	sort.Slice(m.Messages, func(i, j int) bool { return m.Messages[i].Action < m.Messages[j].Action })
	return m, nil
}

type generator struct {
	m       *model
	version string
	file    string // base name of the schema, for names16
	root    *schema
	draft04 bool // the schema file is JSON Schema draft-04
}

// message builds the type of one schema file.
func (g *generator) message(path, typeName, file string) (*goType, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	data = trimBOM(data)
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	if err := checkKeywords(raw, path); err != nil {
		return nil, "", err
	}
	var s schema
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, "", fmt.Errorf("%s: %w", path, err)
	}
	g.file, g.root = file, &s
	g.draft04 = strings.Contains(s.Schema, "draft-04")
	t, err := g.object(&s, typeName, "", path)
	if err != nil {
		return nil, "", err
	}
	return t, sha256Hex(data), nil
}

func trimBOM(b []byte) []byte { return []byte(strings.TrimPrefix(string(b), "\ufeff")) }

// resolve follows a "#/definitions/X" reference.
func (g *generator) resolve(s *schema) (*schema, string, error) {
	if s.Ref == "" {
		return s, "", nil
	}
	name, ok := strings.CutPrefix(s.Ref, "#/definitions/")
	if !ok {
		return nil, "", fmt.Errorf("unsupported $ref %q", s.Ref)
	}
	d := g.root.Definitions[name]
	if d == nil {
		return nil, "", fmt.Errorf("$ref %q: no such definition", s.Ref)
	}
	return d, name, nil
}

// register adds a named type, or returns the existing identical one.
func (g *generator) register(t *goType) (*goType, error) {
	if old, ok := g.m.Types[t.Name]; ok {
		if old.key != t.key {
			return nil, fmt.Errorf("type %s is defined twice, differently", t.Name)
		}
		old.Draft04 = old.Draft04 || t.Draft04
		return old, nil
	}
	g.m.Types[t.Name] = t
	return t, nil
}

// typeName names an inline or defined object or enumeration.
func (g *generator) typeName(defName, path string) (string, error) {
	if defName != "" {
		return goName(defName), nil
	}
	if g.version == "1.6" {
		if n, ok := names16[g.file+"#"+path]; ok {
			return n, nil
		}
	}
	return "", fmt.Errorf("%s#%s: inline type without a name (names16 in names.go)", g.file, path)
}

// object builds a struct type. typeName is set for the top level of a
// message; otherwise the name comes from the definition or names16.
func (g *generator) object(s *schema, typeName, path, where string) (*goType, error) {
	if s.Type != "object" {
		return nil, fmt.Errorf("%s%s: type %q, want object", where, path, s.Type)
	}
	t := &goType{Name: typeName, Kind: kindStruct, Doc: s.Description, Draft04: g.draft04,
		Strict: s.AdditionalProperties != nil && !*s.AdditionalProperties}
	names := make([]string, 0, len(s.Properties))
	for k := range s.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	// Keep the schema's order where it has one: required fields first in the
	// order listed, then the rest alphabetically. Go maps lose JSON order.
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
		if s.Properties[r] == nil {
			return nil, fmt.Errorf("%s%s: required %q is not a property", where, path, r)
		}
	}
	ordered := append([]string(nil), s.Required...)
	for _, n := range names {
		if !required[n] {
			ordered = append(ordered, n)
		}
	}
	var key strings.Builder
	fmt.Fprintf(&key, "struct strict=%v{", t.Strict)
	for _, name := range ordered {
		f, err := g.field(s.Properties[name], name, required[name], path+"/"+name, where)
		if err != nil {
			return nil, err
		}
		t.Fields = append(t.Fields, f)
		fmt.Fprintf(&key, "%s:%v:%v:%d:%d:%s;", f.JSON, f.Required, f.Array, f.MinItems, f.MaxItems, refKey(f.Elem))
	}
	key.WriteString("}")
	t.key = key.String()
	return t, nil
}

func refKey(r ref) string {
	k := r.Base + "/" + r.Format
	if r.MaxLength != nil {
		k += fmt.Sprintf("/len%d", *r.MaxLength)
	}
	for _, p := range []*float64{r.Minimum, r.Maximum, r.MultipleOf} {
		if p != nil {
			k += fmt.Sprintf("/%v", *p)
		} else {
			k += "/-"
		}
	}
	return k
}

func (g *generator) field(s *schema, name string, required bool, path, where string) (*field, error) {
	f := &field{JSON: name, Name: goName(name), Required: required, MaxItems: -1}
	if name == "customData" {
		f.Name = "CustomData"
	}
	resolved, defName, err := g.resolve(s)
	if err != nil {
		return nil, fmt.Errorf("%s%s: %w", where, path, err)
	}
	f.Doc = firstNonEmpty(s.Description, resolved.Description)
	elem := resolved
	elemDef := defName
	elemPath := path
	if resolved.Type == "array" {
		f.Array = true
		if resolved.MinItems != nil {
			f.MinItems = *resolved.MinItems
		}
		if resolved.MaxItems != nil {
			f.MaxItems = *resolved.MaxItems
		}
		if resolved.Items == nil {
			return nil, fmt.Errorf("%s%s: array without items", where, path)
		}
		elem, elemDef, err = g.resolve(resolved.Items)
		if err != nil {
			return nil, fmt.Errorf("%s%s: %w", where, path, err)
		}
		elemPath = path + "/[]"
		if elem.Type == "array" {
			return nil, fmt.Errorf("%s%s: arrays of arrays are not supported", where, path)
		}
	}
	f.Elem, err = g.scalar(elem, elemDef, elemPath, where)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// scalar builds the type of a value: a Go scalar, an enumeration or a struct.
func (g *generator) scalar(s *schema, defName, path, where string) (ref, error) {
	r := ref{MaxLength: s.MaxLength, Minimum: s.Minimum, Maximum: s.Maximum, MultipleOf: s.MultipleOf}
	switch {
	case defName == "CustomDataType":
		t := &goType{Name: "CustomData", Kind: kindStruct, CustomData: true, Doc: s.Description, key: "customdata"}
		if v := s.Properties["vendorId"]; v != nil && v.MaxLength != nil {
			t.VendorMax = *v.MaxLength
		} else {
			return r, fmt.Errorf("%s%s: CustomDataType without a vendorId maxLength", where, path)
		}
		named, err := g.register(t)
		if err != nil {
			return r, err
		}
		r.Base, r.Named = named.Name, named
	case len(s.Enum) > 0:
		if s.Type != "string" {
			return r, fmt.Errorf("%s%s: enumeration of type %q", where, path, s.Type)
		}
		name, err := g.typeName(defName, path)
		if err != nil {
			return r, err
		}
		t := &goType{Name: name, Kind: kindEnum, Doc: s.Description, Values: s.Enum,
			key: "enum:" + strings.Join(s.Enum, "|")}
		named, err := g.register(t)
		if err != nil {
			return r, err
		}
		r.Base, r.Named = named.Name, named
		r.MaxLength = nil
	case s.Type == "object":
		name, err := g.typeName(defName, path)
		if err != nil {
			return r, err
		}
		t, err := g.object(s, name, path, where)
		if err != nil {
			return r, err
		}
		named, err := g.register(t)
		if err != nil {
			return r, fmt.Errorf("%s%s: %w", where, path, err)
		}
		r.Base, r.Named = named.Name, named
	case s.Type == "string":
		switch s.Format {
		case "":
			r.Base = "string"
		case "date-time":
			r.Base = "time"
		case "uri":
			r.Base, r.Format = "string", "uri"
		default:
			return r, fmt.Errorf("%s%s: format %q", where, path, s.Format)
		}
	case s.Type == "" && s.Ref == "" && len(s.Properties) == 0:
		// Untyped: any JSON value (DataTransfer's data).
		r.Base = "raw"
	case s.Type == "integer":
		r.Base = "int"
	case s.Type == "number":
		r.Base = "float64"
	case s.Type == "boolean":
		r.Base = "bool"
	default:
		return r, fmt.Errorf("%s%s: type %q", where, path, s.Type)
	}
	return r, nil
}
