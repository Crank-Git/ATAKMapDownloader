package datapackage_test

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/datapackage"
)

// manifestXML is the part of MANIFEST/manifest.xml that the tests read.
type manifestXML struct {
	XMLName    xml.Name `xml:"MissionPackageManifest"`
	Version    string   `xml:"version,attr"`
	Parameters []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"Configuration>Parameter"`
	Contents []struct {
		ZipEntry string `xml:"zipEntry,attr"`
		Ignore   string `xml:"ignore,attr"`
	} `xml:"Contents>Content"`
}

// param returns the value of the Configuration parameter name, and false when the manifest has no such parameter.
func (m manifestXML) param(name string) (string, bool) {
	for _, p := range m.Parameters {
		if p.Name == name {
			return p.Value, true
		}
	}
	return "", false
}

// uuidV4 matches a UUID of version 4 and the RFC 9562 variant.
var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// bytesFile returns a File with the entry name and the content data.
func bytesFile(entry, data string) datapackage.File {
	return datapackage.File{Entry: entry, Open: func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(data)), nil
	}}
}

// readZip returns the content of each entry of the zip at path, by entry name.
func readZip(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open the zip %s: %v", path, err)
	}
	defer func() { _ = r.Close() }() // The zip is read-only here, so a close error loses nothing.
	entries := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open the entry %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close() // The entry is read-only here, so a close error loses nothing.
		if err != nil {
			t.Fatalf("read the entry %s: %v", f.Name, err)
		}
		entries[f.Name] = string(data)
	}
	return entries
}

// parseManifest returns the manifest of entries and fails the test when it does not parse.
func parseManifest(t *testing.T, entries map[string]string) manifestXML {
	t.Helper()
	raw, ok := entries[datapackage.ManifestEntry]
	if !ok {
		t.Fatalf("zip has no %s", datapackage.ManifestEntry)
	}
	var m manifestXML
	if err := xml.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("parse the manifest: %v\n%s", err, raw)
	}
	return m
}

func TestWriteCreatesAZipWithTheManifestAndEachFile(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "Mt_Hood_SAR.zip")
	files := []datapackage.File{
		bytesFile("hood.mbtiles", "tiles"),
		bytesFile("opentopomap.xml", "<customMapSource/>"),
	}
	if err := datapackage.Write(zipPath, "Mt Hood SAR", files); err != nil {
		t.Fatalf("Write error = %v", err)
	}

	entries := readZip(t, zipPath)
	if got := entries["hood.mbtiles"]; got != "tiles" {
		t.Errorf("entry hood.mbtiles = %q, want tiles", got)
	}
	if got := entries["opentopomap.xml"]; got != "<customMapSource/>" {
		t.Errorf("entry opentopomap.xml = %q, want <customMapSource/>", got)
	}
	if len(entries) != 3 {
		t.Errorf("zip entries = %d, want 3: the manifest and the two files", len(entries))
	}

	m := parseManifest(t, entries)
	if m.Version != "2" {
		t.Errorf("manifest version = %q, want 2", m.Version)
	}
	if uid, _ := m.param("uid"); !uuidV4.MatchString(uid) {
		t.Errorf("uid = %q, want a UUID v4", uid)
	}
	if name, _ := m.param("name"); name != "Mt Hood SAR" {
		t.Errorf("name = %q, want Mt Hood SAR", name)
	}
	if v, _ := m.param("onReceiveImport"); v != "true" {
		t.Errorf("onReceiveImport = %q, want true", v)
	}
	var listed []string
	for _, c := range m.Contents {
		listed = append(listed, c.ZipEntry)
		if c.Ignore != "false" {
			t.Errorf("Content %s ignore = %q, want false", c.ZipEntry, c.Ignore)
		}
	}
	if want := []string{"hood.mbtiles", "opentopomap.xml"}; !slices.Equal(listed, want) {
		t.Errorf("manifest zipEntry values = %v, want %v", listed, want)
	}
}

func TestWriteEscapesThePackageNameInTheManifest(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "p.zip")
	if err := datapackage.Write(zipPath, `a "b" & <c>`, []datapackage.File{bytesFile("a.mbtiles", "x")}); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if name, _ := parseManifest(t, readZip(t, zipPath)).param("name"); name != `a "b" & <c>` {
		t.Errorf("name = %q, want the name unchanged after the parse", name)
	}
}

func TestWriteGivesEachDataPackageANewUID(t *testing.T) {
	dir := t.TempDir()
	uids := map[string]bool{}
	for _, name := range []string{"a.zip", "b.zip"} {
		path := filepath.Join(dir, name)
		if err := datapackage.Write(path, "p", []datapackage.File{bytesFile("a.mbtiles", "x")}); err != nil {
			t.Fatalf("Write error = %v", err)
		}
		uid, _ := parseManifest(t, readZip(t, path)).param("uid")
		uids[uid] = true
	}
	if len(uids) != 2 {
		t.Errorf("uids = %v, want two different values", uids)
	}
}

func TestWriteGivesTheZipMode0644(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "p.zip")
	if err := datapackage.Write(zipPath, "p", []datapackage.File{bytesFile("a.mbtiles", "x")}); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	info, err := os.Stat(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("zip mode = %v, want 0644, the mode of the output file that the zip replaces", got)
	}
}

func TestWriteReplacesAnExistingZip(t *testing.T) {
	zipPath := filepath.Join(t.TempDir(), "p.zip")
	if err := os.WriteFile(zipPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := datapackage.Write(zipPath, "p", []datapackage.File{bytesFile("a.mbtiles", "new")}); err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if got := readZip(t, zipPath)["a.mbtiles"]; got != "new" {
		t.Errorf("entry a.mbtiles = %q, want new", got)
	}
}

func TestWriteLeavesNoFileWhenAFileCannotBeRead(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "p.zip")
	errRead := errors.New("disk read error")
	files := []datapackage.File{
		bytesFile("a.mbtiles", "x"),
		{Entry: "b.xml", Open: func() (io.ReadCloser, error) { return nil, errRead }},
	}
	if err := datapackage.Write(zipPath, "p", files); !errors.Is(err, errRead) {
		t.Errorf("Write error = %v, want it to wrap %v", err, errRead)
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("directory after the failed Write holds %v, want nothing", left)
	}
}

func TestValidName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"accepts letters, digits, spaces, _ and -", "Mt Hood_SAR-2", true},
		{"accepts 1 character", "a", true},
		{"accepts 64 characters", strings.Repeat("a", 64), true},
		{"rejects an empty name", "", false},
		{"rejects 65 characters", strings.Repeat("a", 65), false},
		{"rejects a slash", "a/b", false},
		{"rejects a dot", "a.b", false},
		{"rejects a letter outside ASCII", "Zürich", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := datapackage.ValidName(tt.in); got != tt.want {
				t.Errorf("ValidName(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFileNameReplacesSpacesWithUnderscores(t *testing.T) {
	if got := datapackage.FileName("Mt Hood SAR"); got != "Mt_Hood_SAR.zip" {
		t.Errorf("FileName(Mt Hood SAR) = %q, want Mt_Hood_SAR.zip", got)
	}
}

func TestDetect(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "p.zip")
	if err := datapackage.Write(pkg, "p", []datapackage.File{bytesFile("a.mbtiles", "x")}); err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	zw := zip.NewWriter(&plain)
	if _, err := zw.Create("a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	pkgData, err := os.ReadFile(pkg) //nolint:gosec // G304: The test wrote the file at this path.
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"accepts a zip with the manifest", pkgData, true},
		{"rejects a zip without the manifest", plain.Bytes(), false},
		{"rejects a file that is not a zip", []byte("SQLite format 3\x00"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := datapackage.Detect(bytes.NewReader(tt.data), int64(len(tt.data))); got != tt.want {
				t.Errorf("Detect = %v, want %v", got, tt.want)
			}
		})
	}
}
