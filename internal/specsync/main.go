// SPDX-License-Identifier: MIT

// Command specsync refreshes schemas/ from the Open Charge Alliance: it
// finds the newest editions, errata and appendices on the OCA download page,
// downloads them, writes the JSON schemas they contain into schemas/ (as
// they are; ADR 0007), and records what it fetched in schemas/UPSTREAM.json
// and the checksums in schemas/SHA256SUMS.
//
// It changes files only; whether to take an update is a review decision.
// The Upstream specification check workflow runs it weekly and opens an
// issue when the result differs from what is committed. By hand:
//
//	go run ./internal/specsync && git diff --stat schemas
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	downloadPage = "https://openchargealliance.org/my-oca/ocpp/"
	userAgent    = "go-ocpp specsync (https://github.com/otfabric/go-ocpp)"
	maxArchive   = 64 << 20 // the largest archive is about 14 MB
	maxEntry     = 16 << 20
)

// family is one kind of document on the download page. The newest edition
// (and, for errata, the newest date) wins.
type family struct {
	Name    string         // key in UPSTREAM.json
	Label   *regexp.Regexp // anchor text; its numeric groups order the editions
	Schemas string         // directory under schemas/ that receives its JSON schemas, if any
	// SchemaPath matches, inside the archive and its nested archives, the
	// JSON schema files to take.
	SchemaPath *regexp.Regexp
}

var families = []family{
	{Name: "ocpp16", Label: regexp.MustCompile(`^OCPP 1\.6 \(all files & errata\)$`),
		Schemas: "ocpp16/core", SchemaPath: regexp.MustCompile(`(^|/)schemas/json/[^/]+\.json$`)},
	{Name: "ocpp16-security-whitepaper", Label: regexp.MustCompile(`^OCPP 1\.6 Security Whitepaper Edition (\d+)$`),
		Schemas: "ocpp16/security", SchemaPath: regexp.MustCompile(`(^|/)JSON_schemas(\.zip)?/schema/[^/]+\.json$`)},
	{Name: "ocpp201", Label: regexp.MustCompile(`^OCPP 2\.0\.1 Edition (\d+) \(all files\)$`),
		Schemas: "ocpp201", SchemaPath: regexp.MustCompile(`OCPP-2\.0\.1_part3_JSON_schemas(\.zip)?/(OCPP-2\.0\.1_part3_JSON_schemas/)?[^/]+\.json$`)},
	{Name: "ocpp201-errata", Label: regexp.MustCompile(`^OCPP 2\.0\.1 Edition (\d+) Errata (\d{4})-(\d{2})$`)},
	{Name: "ocpp201-appendices", Label: regexp.MustCompile(`^OCPP 2\.0\.1 Edition (\d+) Part 2 appendices v(\d+)\.(\d+)$`)},
	{Name: "ocpp21", Label: regexp.MustCompile(`^OCPP 2\.1 Edition (\d+) \(all files\)$`),
		Schemas: "ocpp21", SchemaPath: regexp.MustCompile(`OCPP-2\.1_part3_JSON_schemas(\.zip)?/(OCPP-2\.1_part3_JSON_schemas/)?[^/]+\.json$`)},
	{Name: "ocpp21-errata", Label: regexp.MustCompile(`^OCPP 2\.1 Edition (\d+) Errata (\d{4})-(\d{2})$`)},
	{Name: "ocpp21-appendices", Label: regexp.MustCompile(`^OCPP 2\.1 Edition (\d+) Part 2 appendices v(\d+)\.(\d+)$`)},
}

// Upstream is schemas/UPSTREAM.json.
type Upstream struct {
	Source    string            `json:"source"`
	Documents map[string]Source `json:"documents"`
}

// Source is one downloaded archive.
type Source struct {
	Label  string `json:"label"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Files  int    `json:"schemaFiles,omitempty"`
}

func main() {
	dir := flag.String("schemas", "schemas", "the schemas directory")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "specsync:", err)
		os.Exit(1)
	}
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, limit)
	}
	return b, nil
}

var anchor = regexp.MustCompile(`(?s)<a[^>]*href="(https://openchargealliance\.org/download/[0-9a-f]+)"[^>]*>(.*?)</a>`)
var tags = regexp.MustCompile(`<[^>]+>`)

// links returns the download links of the page, by anchor text.
func links(page []byte) map[string]string {
	out := map[string]string{}
	for _, m := range anchor.FindAllSubmatch(page, -1) {
		label := strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(string(m[2]), " "))), " ")
		out[label] = string(m[1])
	}
	return out
}

// newest returns the label of the newest document of a family.
func newest(f family, labels map[string]string) (string, error) {
	var best string
	var bestKey []int
	for label := range labels {
		m := f.Label.FindStringSubmatch(label)
		if m == nil {
			continue
		}
		key := make([]int, len(m)-1)
		for i, s := range m[1:] {
			key[i], _ = strconv.Atoi(s)
		}
		if best == "" || less(bestKey, key) {
			best, bestKey = label, key
		}
	}
	if best == "" {
		return "", fmt.Errorf("no document matching %s on %s: has the page changed?", f.Label, downloadPage)
	}
	return best, nil
}

func less(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func run(dir string) error {
	page, err := get(downloadPage, 8<<20)
	if err != nil {
		return err
	}
	labels := links(page)
	up := Upstream{Source: downloadPage, Documents: map[string]Source{}}
	for _, f := range families {
		label, err := newest(f, labels)
		if err != nil {
			return err
		}
		url := labels[label]
		archive, err := get(url, maxArchive)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(archive)
		src := Source{Label: label, URL: url, SHA256: hex.EncodeToString(sum[:])}
		if f.Schemas != "" {
			files := map[string][]byte{}
			if err := collect(archive, "", f.SchemaPath, files, 0); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			if len(files) == 0 {
				return fmt.Errorf("%s: no schema matching %s in the archive", label, f.SchemaPath)
			}
			if err := replace(filepath.Join(dir, filepath.FromSlash(f.Schemas)), files); err != nil {
				return err
			}
			src.Files = len(files)
		}
		up.Documents[f.Name] = src
		fmt.Printf("%-28s %s (%s…)\n", f.Name, label, src.SHA256[:12])
	}
	b, err := json.MarshalIndent(up, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "UPSTREAM.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	return writeSums(dir)
}

// collect walks a zip archive, and the zip archives inside it, and keeps the
// files whose path matches want, by base name. Nesting is bounded.
func collect(archive []byte, prefix string, want *regexp.Regexp, out map[string][]byte, depth int) error {
	if depth > 3 {
		return errors.New("archives nested too deeply")
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := prefix + f.Name
		isZip := strings.HasSuffix(strings.ToLower(f.Name), ".zip")
		if !isZip && !want.MatchString(name) {
			continue
		}
		if f.UncompressedSize64 > maxEntry {
			return fmt.Errorf("%s: larger than %d bytes", name, maxEntry)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxEntry+1))
		_ = rc.Close()
		if err != nil {
			return err
		}
		if isZip {
			if err := collect(data, strings.TrimSuffix(name, ".zip")+".zip/", want, out, depth+1); err != nil {
				return err
			}
			continue
		}
		base := path.Base(f.Name)
		if !regexp.MustCompile(`^[A-Za-z0-9]+\.json$`).MatchString(base) {
			return fmt.Errorf("unexpected schema file name %q", f.Name)
		}
		out[base] = data
	}
	return nil
}

// replace makes dir hold exactly files.
func replace(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, f := range old {
		if _, keep := files[filepath.Base(f)]; !keep {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeSums writes SHA256SUMS in the format of shasum -a 256.
func writeSums(dir string) error {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") || filepath.Base(p) == "UPSTREAM.json" {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
