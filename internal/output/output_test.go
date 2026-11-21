package output

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// textFormat is a test output format: a text file whose first line is textHeader.
const textFormat = "text"

// textHeader is the first line of a textFormat file. The detector reads it in place of a schema.
var textHeader = []byte("TEXT OUTPUT FILE\n")

// textWriter is the Writer of textFormat. Open writes the header, and the other methods do nothing.
type textWriter struct{}

func (textWriter) Has(int, int, int) bool           { return false }
func (textWriter) Put(int, int, int, []byte) error  { return nil }
func (textWriter) Fail(int, int, int, string) error { return nil }
func (textWriter) Failed() ([]FailedTile, error)    { return nil, nil }
func (textWriter) Close() error                     { return nil }
func openText(path string, _ map[string]string) (Writer, error) {
	return textWriter{}, os.WriteFile(path, textHeader, 0o600)
}

// readText returns the Info of a textFormat file, and false when the file at path does not start with textHeader.
func readText(path string) (Info, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The test reads a file in its own temporary directory.
	if err != nil {
		return Info{}, false, err
	}
	if !bytes.HasPrefix(data, textHeader) {
		return Info{}, false, nil
	}
	return Info{Source: "Text Source", MinZoom: 3, MaxZoom: 5}, true, nil
}

// readTextTile returns the content of the textFormat file at path after textHeader as the tile 0, 0, 0.
func readTextTile(path string, z, x, y int) ([]byte, bool, error) {
	data, err := os.ReadFile(path) //nolint:gosec // The test reads a file in its own temporary directory.
	if err != nil || z != 0 || x != 0 || y != 0 {
		return nil, false, err
	}
	return data[len(textHeader):], true, nil
}

func init() {
	Register(textFormat, readText, openText, readTextTile)
}

func TestExtensionFollowsTheOutputFormat(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{MBTiles, ".mbtiles"},
		{OSMDroid, ".sqlite"},
		{GeoPackage, ".gpkg"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			if got := Extension(tt.format); got != tt.want {
				t.Errorf("Extension(%q) = %q, want %q", tt.format, got, tt.want)
			}
		})
	}
}

func TestFormatsListsTheThreeOutputFormatsWithMBTilesFirst(t *testing.T) {
	want := []string{"mbtiles", "osmdroid", "gpkg"}
	if len(Formats) != len(want) {
		t.Fatalf("Formats = %v, want %v", Formats, want)
	}
	for i := range want {
		if Formats[i] != want[i] {
			t.Errorf("Formats = %v, want %v", Formats, want)
		}
	}
}

func TestOpenUsesTheOpenerOfTheRegisteredFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	w, err := Open(textFormat, path, nil)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", textFormat, err)
	}
	if _, ok := w.(textWriter); !ok {
		t.Errorf("Open(%q) = %T, want textWriter", textFormat, w)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, textHeader) { //nolint:gosec // See readText.
		t.Errorf("file after Open = %q, %v; want the text header", data, err)
	}
}

func TestOpenRejectsAFormatThatIsNotRegistered(t *testing.T) {
	_, err := Open("kmz", filepath.Join(t.TempDir(), "out.kmz"), nil)
	if !errors.Is(err, ErrNotRegistered) {
		t.Errorf("Open(%q) error = %v, want ErrNotRegistered", "kmz", err)
	}
}

func TestRegisteredReportsOnlyTheRegisteredFormats(t *testing.T) {
	tests := []struct {
		format string
		want   bool
	}{
		{textFormat, true},
		{"kmz", false},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			if got := Registered(tt.format); got != tt.want {
				t.Errorf("Registered(%q) = %v, want %v", tt.format, got, tt.want)
			}
		})
	}
}

func TestReadInfoNamesTheFormatThatTheDetectorFinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, textHeader, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatalf("ReadInfo error = %v", err)
	}
	if info.Format != textFormat || info.Source != "Text Source" || info.MinZoom != 3 || info.MaxZoom != 5 {
		t.Errorf("ReadInfo = %+v, want format %q, source Text Source, zoom levels 3-5", info, textFormat)
	}
}

func TestReadInfoRejectsAFileThatNoDetectorFinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("my notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInfo(path); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("ReadInfo error = %v, want ErrUnknownFormat", err)
	}
}

func TestRegisterPanicsOnASecondRegistrationOfAFormat(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("a second Register of one format did not panic")
		}
	}()
	Register(textFormat, readText, openText, readTextTile)
}

func TestReadTileUsesTheReaderOfTheFormatThatReadInfoFinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, append(textHeader, "tile"...), 0o600); err != nil {
		t.Fatal(err)
	}
	data, ok, err := ReadTile(path, 0, 0, 0)
	if err != nil || !ok || string(data) != "tile" {
		t.Errorf("ReadTile = %q, %v, %v; want \"tile\", true, nil", data, ok, err)
	}
}

func TestReadTileRejectsAFileThatNoDetectorFinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(path, []byte("no header"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadTile(path, 0, 0, 0); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("ReadTile error = %v, want ErrUnknownFormat", err)
	}
}
