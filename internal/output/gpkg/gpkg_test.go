package gpkg

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/output"
)

// jobMetadata holds the metadata rows that the engine gives a writer, for a job at zoom levels 2 to 4.
var jobMetadata = map[string]string{
	"name":          "Test XYZ - 45.0000,-122.0000 to 46.0000,-121.0000",
	"description":   "Downloaded from Test XYZ",
	"format":        "png",
	"bounds":        "-122.0000,45.0000,-121.0000,46.0000",
	"minzoom":       "2",
	"maxzoom":       "4",
	"atakdl_source": "Test XYZ",
}

// create returns a writer for a new output file in a temporary directory, and the path of the file.
func create(t *testing.T, metadata map[string]string) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.gpkg")
	w, err := Open(path, metadata)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return w, path
}

// closeWriter closes w and fails the test on an error.
func closeWriter(t *testing.T, w *Writer) {
	t.Helper()
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// openFile opens the output file at path read-only, and closes it when the test ends.
func openFile(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() }) // The test only reads the file, so a close error loses no data.
	return db
}

// queryInt returns the integer result of query, and fails the test on an error.
func queryInt(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestOpenSetsTheGeoPackageHeader(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db := openFile(t, path)

	tests := []struct {
		pragma string
		want   int
	}{
		{"application_id", 0x47504B47},
		{"user_version", 10300},
	}
	for _, tt := range tests {
		t.Run("sets "+tt.pragma, func(t *testing.T) {
			if got := queryInt(t, db, "PRAGMA "+tt.pragma); got != tt.want {
				t.Errorf("PRAGMA %s = %#x, want %#x", tt.pragma, got, tt.want)
			}
		})
	}
}

func TestOpenCreatesTheRequiredGeoPackageTables(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db := openFile(t, path)

	for _, table := range []string{"gpkg_spatial_ref_sys", "gpkg_contents", "gpkg_tile_matrix_set", "gpkg_tile_matrix", "Test_XYZ"} {
		t.Run("creates "+table, func(t *testing.T) {
			if n := queryInt(t, db, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table); n != 1 {
				t.Errorf("sqlite_master holds %d tables named %s, want 1", n, table)
			}
		})
	}
}

func TestOpenWritesTheRequiredSpatialReferenceSystems(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db := openFile(t, path)

	tests := []struct {
		srsID        int
		organization string
		coordsysID   int
	}{
		{4326, "EPSG", 4326},
		{-1, "NONE", -1},
		{0, "NONE", 0},
		{3857, "EPSG", 3857},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("writes srs_id %d", tt.srsID), func(t *testing.T) {
			var org, definition string
			var coordsys int
			err := db.QueryRowContext(t.Context(),
				"SELECT organization, organization_coordsys_id, definition FROM gpkg_spatial_ref_sys WHERE srs_id = ?", tt.srsID).
				Scan(&org, &coordsys, &definition)
			if err != nil {
				t.Fatalf("read srs_id %d: %v", tt.srsID, err)
			}
			if org != tt.organization || coordsys != tt.coordsysID || definition == "" {
				t.Errorf("srs_id %d = %s %d %q, want %s %d and a definition", tt.srsID, org, coordsys, definition, tt.organization, tt.coordsysID)
			}
		})
	}
}

func TestOpenWritesTheContentsRowInEPSG3857(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db := openFile(t, path)

	var table, dataType, identifier string
	var srsID int
	var minX, minY, maxX, maxY float64
	err := db.QueryRowContext(t.Context(),
		"SELECT table_name, data_type, identifier, srs_id, min_x, min_y, max_x, max_y FROM gpkg_contents").
		Scan(&table, &dataType, &identifier, &srsID, &minX, &minY, &maxX, &maxY)
	if err != nil {
		t.Fatalf("read gpkg_contents: %v", err)
	}
	if table != "Test_XYZ" || dataType != "tiles" || identifier != "Test XYZ" || srsID != 3857 {
		t.Errorf("gpkg_contents = %s %s %q %d, want Test_XYZ tiles \"Test XYZ\" 3857", table, dataType, identifier, srsID)
	}
	// The values are the Web Mercator coordinates of 122 W, 45 N and 121 W, 46 N.
	want := []float64{-13580977.876779, 5621521.486192, -13469658.385986, 5780349.220256}
	for i, got := range []float64{minX, minY, maxX, maxY} {
		if math.Abs(got-want[i]) > 0.01 {
			t.Errorf("gpkg_contents bounds = %f %f %f %f, want %v", minX, minY, maxX, maxY, want)
			break
		}
	}
}

func TestOpenWritesTheGoogleMapsCompatibleTileMatrix(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db := openFile(t, path)

	var srsID int
	var minX, minY, maxX, maxY float64
	err := db.QueryRowContext(t.Context(),
		"SELECT srs_id, min_x, min_y, max_x, max_y FROM gpkg_tile_matrix_set WHERE table_name = 'Test_XYZ'").
		Scan(&srsID, &minX, &minY, &maxX, &maxY)
	if err != nil {
		t.Fatalf("read gpkg_tile_matrix_set: %v", err)
	}
	if srsID != 3857 || minX != -worldExtent || minY != -worldExtent || maxX != worldExtent || maxY != worldExtent {
		t.Errorf("gpkg_tile_matrix_set = %d %f %f %f %f, want 3857 and the world extent", srsID, minX, minY, maxX, maxY)
	}

	rows, err := db.QueryContext(t.Context(),
		"SELECT zoom_level, matrix_width, matrix_height, tile_width, tile_height, pixel_x_size, pixel_y_size FROM gpkg_tile_matrix WHERE table_name = 'Test_XYZ' ORDER BY zoom_level")
	if err != nil {
		t.Fatalf("read gpkg_tile_matrix: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	var zooms []int
	for rows.Next() {
		var z, width, height, tileWidth, tileHeight int
		var pixelX, pixelY float64
		if err := rows.Scan(&z, &width, &height, &tileWidth, &tileHeight, &pixelX, &pixelY); err != nil {
			t.Fatalf("read gpkg_tile_matrix: %v", err)
		}
		zooms = append(zooms, z)
		// GeoPackage requirement 45: the matrix width times the tile width times the pixel size is the matrix set width.
		wantPixel := 2 * worldExtent / float64(int(256)<<z)
		if width != 1<<z || height != 1<<z || tileWidth != 256 || tileHeight != 256 ||
			math.Abs(pixelX-wantPixel) > 1e-9 || math.Abs(pixelY-wantPixel) > 1e-9 {
			t.Errorf("zoom level %d = %d×%d tiles of %d×%d, pixel %f×%f; want %d×%d tiles of 256×256, pixel %f",
				z, width, height, tileWidth, tileHeight, pixelX, pixelY, 1<<z, 1<<z, wantPixel)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read gpkg_tile_matrix: %v", err)
	}
	if fmt.Sprint(zooms) != "[2 3 4]" {
		t.Errorf("gpkg_tile_matrix zoom levels = %v, want [2 3 4]", zooms)
	}
}

func TestPutStoresTheTileAtItsXYZRow(t *testing.T) {
	w, path := create(t, jobMetadata)
	data := []byte("tile 3/2/1")
	if err := w.Put(3, 2, 1, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	var got []byte
	err := openFile(t, path).QueryRowContext(t.Context(),
		"SELECT tile_data FROM Test_XYZ WHERE zoom_level = 3 AND tile_column = 2 AND tile_row = 1").Scan(&got)
	if err != nil {
		t.Fatalf("read the tile at XYZ row 1: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("tile_data = %q, want %q", got, data)
	}
}

func TestPutAddsATileMatrixRowForAZoomLevelOutsideTheMetadata(t *testing.T) {
	w, path := create(t, map[string]string{"atakdl_source": "Test XYZ"})
	if err := w.Put(7, 1, 1, []byte("tile")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	// GeoPackage requirement 44: gpkg_tile_matrix has a row for each zoom level that holds a tile.
	if n := queryInt(t, openFile(t, path), "SELECT count(*) FROM gpkg_tile_matrix WHERE zoom_level = 7"); n != 1 {
		t.Errorf("gpkg_tile_matrix holds %d rows for zoom level 7, want 1", n)
	}
}

func TestOpenOfAnExistingFileLoadsTheStoredTiles(t *testing.T) {
	w, path := create(t, jobMetadata)
	if err := w.Put(3, 2, 1, []byte("tile")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	w, err := Open(path, jobMetadata)
	if err != nil {
		t.Fatalf("Open of the existing file: %v", err)
	}
	if !w.Has(3, 2, 1) {
		t.Error("Has(3, 2, 1) = false, want true for the stored tile")
	}
	if w.Has(3, 2, 6) {
		t.Error("Has(3, 2, 6) = true, want false, because the file stores the XYZ row and not the TMS row")
	}
	closeWriter(t, w)

	db := openFile(t, path)
	if n := queryInt(t, db, "SELECT count(*) FROM gpkg_contents"); n != 1 {
		t.Errorf("gpkg_contents holds %d rows, want 1", n)
	}
	if n := queryInt(t, db, "SELECT count(*) FROM gpkg_tile_matrix"); n != 3 {
		t.Errorf("gpkg_tile_matrix holds %d rows, want 3", n)
	}
}

func TestFailRecordsTheTileAndPutRemovesTheRecord(t *testing.T) {
	w, _ := create(t, jobMetadata)
	for _, reason := range []string{"http_503", "not_image"} {
		if err := w.Fail(2, 3, 1, reason); err != nil {
			t.Fatalf("Fail: %v", err)
		}
	}
	got, err := w.Failed()
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	want := []output.FailedTile{{Z: 2, X: 3, Y: 1, Reason: "not_image", Attempts: 2}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Failed = %v, want %v", got, want)
	}

	if err := w.Put(2, 3, 1, []byte("tile")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := w.Failed(); err != nil || len(got) != 0 {
		t.Errorf("Failed after Put = %v, %v; want no failed tile", got, err)
	}
	closeWriter(t, w)
}

func TestCloseLeavesOneFileInRollbackJournalMode(t *testing.T) {
	w, path := create(t, jobMetadata)
	if err := w.Put(2, 1, 1, []byte("tile")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Stat(%s) error = %v, want no file", path+suffix, err)
		}
	}
	var mode string
	if err := openFile(t, path).QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "delete" {
		t.Errorf("journal_mode = %s, want delete", mode)
	}
}

func TestReadInfoDetectsAGeoPackageFile(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)

	info, err := output.ReadInfo(path)
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	want := output.Info{Format: output.GeoPackage, TileFormat: "png", Source: "Test XYZ", MinZoom: 2, MaxZoom: 4, Bounds: []float64{-122, 45, -121, 46}}
	if fmt.Sprint(info) != fmt.Sprint(want) {
		t.Errorf("ReadInfo = %+v, want %+v", info, want)
	}
}

func TestDetectorRejectsAnMBTilesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mbtiles")
	w, err := mbtiles.Open(path, map[string]string{"atakdl_source": "Test XYZ"})
	if err != nil {
		t.Fatalf("mbtiles.Open: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, ok, err := readInfo(path); ok || err != nil {
		t.Errorf("readInfo(MBTiles file) = %v, %v; want false, nil", ok, err)
	}
	if info, err := output.ReadInfo(path); err != nil || info.Format != output.MBTiles {
		t.Errorf("ReadInfo(MBTiles file) = %+v, %v; want format mbtiles", info, err)
	}
}

func TestTableNameMakesAValidTablePyramidName(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"replaces each character that is not a letter, a digit or an underscore", "Mt. Hood (USGS)", "Mt__Hood__USGS_"},
		{"keeps a name that is valid", "OpenTopoMap", "OpenTopoMap"},
		{"adds a prefix to a name that starts with a digit", "10m Relief", "t_10m_Relief"},
		{"adds a prefix to a name that starts with the reserved gpkg_ prefix", "GPKG_tiles", "t_GPKG_tiles"},
		{"adds a prefix to a name that starts with the atakdl_ prefix of the writer", "atakdl_failed_tiles", "t_atakdl_failed_tiles"},
		{"adds a prefix to a name that starts with the reserved sqlite_ prefix", "sqlite_master", "t_sqlite_master"},
		{"names the table tiles when the map source has no name", "", "tiles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tableName(tt.source); got != tt.want {
				t.Errorf("tableName(%q) = %q, want %q", tt.source, got, tt.want)
			}
		})
	}
}
