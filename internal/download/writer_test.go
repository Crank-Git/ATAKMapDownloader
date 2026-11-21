package download

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// memoryFormat is a test output format that keeps the tiles in memory and writes no file.
const memoryFormat = "memory"

// memoryWriters holds each memoryWriter that the opener of memoryFormat returns, keyed by path.
var memoryWriters sync.Map

// memoryWriter is the output.Writer of memoryFormat.
type memoryWriter struct {
	mu       sync.Mutex
	metadata map[string]string
	tiles    map[[3]int][]byte
	closed   bool
}

func (w *memoryWriter) Has(z, x, y int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.tiles[[3]int{z, x, y}]
	return ok
}

func (w *memoryWriter) Put(z, x, y int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tiles[[3]int{z, x, y}] = data
	return nil
}

func (w *memoryWriter) Fail(int, int, int, string) error { return nil }

func (w *memoryWriter) Failed() ([]output.FailedTile, error) { return nil, nil }

func (w *memoryWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

func init() {
	output.Register(memoryFormat,
		func(string) (output.Info, bool, error) { return output.Info{}, false, nil },
		func(path string, metadata map[string]string) (output.Writer, error) {
			w := &memoryWriter{metadata: metadata, tiles: map[[3]int][]byte{}}
			memoryWriters.Store(path, w)
			return w, nil
		},
		func(string, int, int, int) ([]byte, bool, error) { return nil, false, nil })
}

func TestEngineWritesEveryTileThroughTheWriterOfTheOutputFormat(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	path := filepath.Join(t.TempDir(), "out.memory")
	cfg := testConfig()
	cfg.Format = memoryFormat

	res, err := politeEngine(cfg, 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), path, nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	v, ok := memoryWriters.Load(path)
	if !ok {
		t.Fatal("the engine did not open a memoryFormat writer")
	}
	w := v.(*memoryWriter) //nolint:forcetypeassert // The init function stores only *memoryWriter values.
	if n := len(w.tiles); n != area.TileCount() {
		t.Errorf("the writer holds %d tiles, want %d", n, area.TileCount())
	}
	if !w.closed {
		t.Error("the engine did not close the writer")
	}
	if w.metadata["atakdl_source"] != "Test XYZ" {
		t.Errorf("metadata atakdl_source = %q, want Test XYZ", w.metadata["atakdl_source"])
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat(%s) error = %v, want no file, because the engine writes only through the writer", path, err)
	}
}

func TestEngineRejectsAnOutputFormatThatIsNotRegistered(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	cfg := testConfig()
	// The name is in no registry, so the test does not depend on the formats that the build registers.
	cfg.Format = "not-a-format"

	_, err := politeEngine(cfg, 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), filepath.Join(t.TempDir(), "out"), nil)
	if !errors.Is(err, output.ErrNotRegistered) {
		t.Errorf("DownloadTiles error = %v, want ErrNotRegistered", err)
	}
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0", n)
	}
}

func TestGenerateOutputFilenameUsesTheExtensionOfTheOutputFormat(t *testing.T) {
	bounds, err := tilecalc.NewGeographicBounds(45, 46, -122, -121)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		customName string
		format     string
		want       string
	}{
		{"names an MBTiles file .mbtiles", "Mt Hood", output.MBTiles, "Mt_Hood_z8-15.mbtiles"},
		{"names an OSMDroid file .sqlite", "Mt Hood", output.OSMDroid, "Mt_Hood_z8-15.sqlite"},
		{"names a GeoPackage file .gpkg", "Mt Hood", output.GeoPackage, "Mt_Hood_z8-15.gpkg"},
		{"names a file without a custom name from the map source and the bounds", "", output.MBTiles, "OpenTopoMap_45.0000_-122.0000_to_46.0000_-121.0000_z8-15.mbtiles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateOutputFilename("OpenTopoMap", bounds, 8, 15, tt.customName, tt.format)
			if got != tt.want {
				t.Errorf("GenerateOutputFilename = %q, want %q", got, tt.want)
			}
			if !strings.HasSuffix(got, output.Extension(tt.format)) {
				t.Errorf("GenerateOutputFilename = %q, want the extension %q", got, output.Extension(tt.format))
			}
		})
	}
}
