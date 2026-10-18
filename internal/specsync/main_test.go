// SPDX-License-Identifier: MIT

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const page = `<ul>
<li><a class="x" href="https://openchargealliance.org/download/aaa1"><i class="fa"></i> OCPP 2.0.1 Edition 3 (all files &amp; errata)</a></li>
<li><a href="https://openchargealliance.org/download/bbb2"> OCPP 2.0.1 Edition 4 (all files)</a></li>
<li><a href="https://openchargealliance.org/download/ccc3">OCPP 2.0.1 Edition 4 Errata 2026-06</a></li>
<li><a href="https://openchargealliance.org/download/ddd4">OCPP 2.0.1 Edition 4 Errata 2026-09</a></li>
<li><a href="https://openchargealliance.org/download/eee5">OCPP 1.6 Security Whitepaper Edition 3</a></li>
<li><a href="https://openchargealliance.org/download/fff6">OCPP 1.6 Security Whitepaper Edition 4</a></li>
<li><a href="https://example.com/elsewhere">OCPP 2.0.1 Edition 9 (all files)</a></li>
</ul>`

func TestNewest(t *testing.T) {
	labels := links([]byte(page))
	if labels["OCPP 2.0.1 Edition 3 (all files & errata)"] != "https://openchargealliance.org/download/aaa1" {
		t.Fatalf("links: %v", labels)
	}
	for name, want := range map[string]string{
		"ocpp201":                    "OCPP 2.0.1 Edition 4 (all files)",
		"ocpp201-errata":             "OCPP 2.0.1 Edition 4 Errata 2026-09",
		"ocpp16-security-whitepaper": "OCPP 1.6 Security Whitepaper Edition 4",
	} {
		f := familyNamed(t, name)
		got, err := newest(f, labels)
		if err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", name, got, err, want)
		}
	}
	if _, err := newest(familyNamed(t, "ocpp21"), labels); err == nil || !strings.Contains(err.Error(), "has the page changed") {
		t.Errorf("a missing family must fail: %v", err)
	}
}

func familyNamed(t *testing.T, name string) family {
	t.Helper()
	for _, f := range families {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no family %s", name)
	return family{}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write(data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The 2.x archives hold the schemas in a nested archive.
func TestCollectNested(t *testing.T) {
	inner := zipOf(t, map[string][]byte{
		"OCPP-2.0.1_part3_JSON_schemas/AuthorizeRequest.json":  []byte(`{"a":1}`),
		"OCPP-2.0.1_part3_JSON_schemas/AuthorizeResponse.json": []byte(`{"b":2}`),
		"OCPP-2.0.1_part3_JSON_schemas/readme.txt":             []byte(`x`),
	})
	outer := zipOf(t, map[string][]byte{
		"OCPP-2.0.1_all_files/OCPP-2.0.1_part3_JSON_schemas.zip": inner,
		"OCPP-2.0.1_all_files/part2.pdf":                         []byte(`%PDF`),
	})
	files := map[string][]byte{}
	if err := collect(outer, "", familyNamed(t, "ocpp201").SchemaPath, files, 0); err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || string(files["AuthorizeRequest.json"]) != `{"a":1}` {
		t.Fatalf("collected %v", files)
	}
	bad := zipOf(t, map[string][]byte{"OCPP-2.0.1_part3_JSON_schemas/../evil name.json": []byte(`{}`)})
	if err := collect(bad, "", regexp.MustCompile(`\.json$`), map[string][]byte{}, 0); err == nil {
		t.Fatal("an unexpected file name was accepted")
	}
	if err := collect([]byte("not a zip"), "", regexp.MustCompile(`x`), map[string][]byte{}, 0); err == nil {
		t.Fatal("garbage was accepted as an archive")
	}
}

func TestReplaceAndSums(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "ocpp201")
	if err := replace(sub, map[string][]byte{"A.json": []byte("1"), "B.json": []byte("2")}); err != nil {
		t.Fatal(err)
	}
	if err := replace(sub, map[string][]byte{"A.json": []byte("3")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sub, "B.json")); !os.IsNotExist(err) {
		t.Fatal("a schema removed upstream was kept")
	}
	if err := os.WriteFile(filepath.Join(dir, "UPSTREAM.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSums(dir); err != nil {
		t.Fatal(err)
	}
	sums, _ := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	want := "4e07408562bedb8b60ce05c1decfe3ad16b72230967de01f640b7e4729b49fce  ocpp201/A.json\n"
	if string(sums) != want {
		t.Fatalf("SHA256SUMS:\n%s", sums)
	}
}
