// Package datapackage writes an ATAK data package: a zip with MANIFEST/manifest.xml and the files it lists (FR-output-formats-19 to 22).
// The manifest follows the Mission Package v2 example of ATAK-Maps,
// https://github.com/joshuafuller/ATAK-Maps/blob/master/mapvalidator/catalog.py.
package datapackage

import (
	"archive/zip"
	"crypto/rand"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ManifestEntry is the path of the manifest in the zip. ATAK finds the manifest by this path.
const ManifestEntry = "MANIFEST/manifest.xml"

// File is one file that a data package carries.
type File struct {
	// Entry is the path of the file in the zip.
	Entry string
	// Open returns the content of the file. Write closes it.
	Open func() (io.ReadCloser, error)
}

// namePattern is the package name rule of docs/specs/features/10-output-formats.md.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9 _-]{1,64}$`)

// ValidName reports whether name is a valid package name: 1 to 64 characters of A-Z, a-z, 0-9, space, _ and -.
func ValidName(name string) bool {
	return namePattern.MatchString(name)
}

// FileName returns the zip file name of the package name name, with each space as _.
func FileName(name string) string {
	return strings.ReplaceAll(name, " ", "_") + ".zip"
}

// Detect reports whether r, of size bytes, is a zip that holds ManifestEntry.
func Detect(r io.ReaderAt, size int64) bool {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return false
	}
	for _, f := range zr.File {
		if f.Name == ManifestEntry {
			return true
		}
	}
	return false
}

// manifest is the MissionPackageManifest element of the manifest.
type manifest struct {
	XMLName    xml.Name    `xml:"MissionPackageManifest"`
	Version    string      `xml:"version,attr"`
	Parameters []parameter `xml:"Configuration>Parameter"`
	Contents   []content   `xml:"Contents>Content"`
}

type parameter struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type content struct {
	Ignore   bool   `xml:"ignore,attr"`
	ZipEntry string `xml:"zipEntry,attr"`
}

// Write creates the data package zipPath with the package name name, the manifest and files.
// The manifest has a new UUID v4 as its uid, and it lists one Content for each of files.
// It writes a temporary file in the directory of zipPath and renames it, so zipPath is never a partial zip.
// It replaces an existing file at zipPath. When it fails, it leaves no temporary file and does not change zipPath.
func Write(zipPath, name string, files []File) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(zipPath), filepath.Base(zipPath)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create the data package: %w", err)
	}
	defer func() {
		if err != nil {
			// The temporary file holds a partial zip, so it goes with the error.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	zw := zip.NewWriter(tmp)
	if err := writeManifest(zw, name, files); err != nil {
		return err
	}
	for _, f := range files {
		if err := addFile(zw, f); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finish the data package: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close the data package: %w", err)
	}
	if err := os.Rename(tmp.Name(), zipPath); err != nil {
		return fmt.Errorf("rename the data package: %w", err)
	}
	return nil
}

// writeManifest adds ManifestEntry to zw, for the package name name and files.
func writeManifest(zw *zip.Writer, name string, files []File) error {
	m := manifest{
		Version: "2",
		Parameters: []parameter{
			{"uid", newUUID()},
			{"name", name},
			// ATAK imports each listed file when it receives the package.
			{"onReceiveImport", "true"},
		},
	}
	for _, f := range files {
		m.Contents = append(m.Contents, content{ZipEntry: f.Entry})
	}
	data, err := xml.MarshalIndent(m, "", "   ")
	if err != nil {
		return fmt.Errorf("encode the manifest: %w", err)
	}
	w, err := create(zw, ManifestEntry)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return fmt.Errorf("write the manifest: %w", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write the manifest: %w", err)
	}
	return nil
}

// addFile copies the content of f into zw.
func addFile(zw *zip.Writer, f File) (err error) {
	r, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", f.Entry, err)
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	w, err := create(zw, f.Entry)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		return fmt.Errorf("copy %s: %w", f.Entry, err)
	}
	return nil
}

// create adds the entry name to zw with the current time.
func create(zw *zip.Writer, name string) (io.Writer, error) {
	w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return nil, fmt.Errorf("add %s: %w", name, err)
	}
	return w, nil
}

// newUUID returns a random UUID v4 in the RFC 9562 text form.
func newUUID() string {
	var b [16]byte
	// rand.Read never returns an error. It ends the program when the system source fails.
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
