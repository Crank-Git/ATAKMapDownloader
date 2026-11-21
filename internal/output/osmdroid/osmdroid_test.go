package osmdroid

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/output"
)

// jobMetadata holds the metadata rows that the engine gives a writer, for a job at zoom levels 2 to 10.
var jobMetadata = map[string]string{
	"name":          "Test XYZ - 45.0000,-122.0000 to 46.0000,-121.0000",
	"format":        "png",
	"bounds":        "-122.0000,45.0000,-121.0000,46.0000",
	"minzoom":       "2",
	"maxzoom":       "10",
	"atakdl_source": "Test XYZ",
}

// create returns a writer for a new output file in a temporary directory, and the path of the file.
func create(t *testing.T, metadata map[string]string) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.sqlite")
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

// tableColumns returns the columns of table in column order, each as "name type pk".
func tableColumns(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT name, type, pk FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatalf("read the columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	var cols []string
	for rows.Next() {
		var name, typ string
		var pk int
		if err := rows.Scan(&name, &typ, &pk); err != nil {
			t.Fatalf("read the columns of %s: %v", table, err)
		}
		cols = append(cols, fmt.Sprintf("%s %s %d", name, typ, pk))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the columns of %s: %v", table, err)
	}
	return strings.Join(cols, ", ")
}

func TestOpenCreatesTheTablesThatATAKReads(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db := openFile(t, path)

	// The schemas are the CREATE TABLE statements of OSMDroidTileContainer.createTables.
	tests := []struct {
		table string
		want  string
	}{
		{"tiles", "key INTEGER 1, provider TEXT 0, tile BLOB 0"},
		{"ATAK_metadata", "key TEXT 0, value TEXT 0"},
		{"ATAK_catalog", "key INTEGER 1, access INTEGER 0, expiration INTEGER 0, size INTEGER 0"},
	}
	for _, tt := range tests {
		t.Run("creates "+tt.table, func(t *testing.T) {
			if got := tableColumns(t, db, tt.table); got != tt.want {
				t.Errorf("columns of %s = %q, want %q", tt.table, got, tt.want)
			}
		})
	}
}

func TestOpenWritesTheSRIDRowOfATAKMetadata(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	// A second Open of the file must not add a second srid row.
	w, err := Open(path, jobMetadata)
	if err != nil {
		t.Fatalf("Open of the existing file: %v", err)
	}
	closeWriter(t, w)

	rows, err := openFile(t, path).QueryContext(t.Context(), "SELECT value FROM ATAK_metadata WHERE key = 'srid'")
	if err != nil {
		t.Fatalf("read ATAK_metadata: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	var values []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("read ATAK_metadata: %v", err)
		}
		values = append(values, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ATAK_metadata: %v", err)
	}
	if strings.Join(values, ",") != "3857" {
		t.Errorf("srid rows of ATAK_metadata = %v, want one row 3857", values)
	}
}

func TestPutStoresTheTileAtTheOSMDroidKeyWithTheSourceName(t *testing.T) {
	tests := []struct {
		name    string
		z, x, y int
		key     int64
	}{
		{"computes the key of zoom level 0", 0, 0, 0, 0},
		{"computes the key of the acceptance criterion", 10, 301, 385, (((10 << 10) + 301) << 10) + 385},
		{"uses the XYZ row", 3, 2, 1, (((3 << 3) + 2) << 3) + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, path := create(t, jobMetadata)
			data := []byte(fmt.Sprintf("tile %d/%d/%d", tt.z, tt.x, tt.y))
			if err := w.Put(tt.z, tt.x, tt.y, data); err != nil {
				t.Fatalf("Put: %v", err)
			}
			closeWriter(t, w)

			var provider string
			var got []byte
			err := openFile(t, path).QueryRowContext(t.Context(), "SELECT provider, tile FROM tiles WHERE key = ?", tt.key).Scan(&provider, &got)
			if err != nil {
				t.Fatalf("read the tile at key %d: %v", tt.key, err)
			}
			if provider != "Test XYZ" || !bytes.Equal(got, data) {
				t.Errorf("tile at key %d = %q, %q; want Test XYZ, %q", tt.key, provider, got, data)
			}
		})
	}
}

func TestKeyOfTheLastTileAtMaxZoomDecodesInATAK(t *testing.T) {
	last := (1 << MaxZoom) - 1
	k := tileKey(MaxZoom, last, last)
	if k <= 0 {
		t.Fatalf("tileKey(%d, %d, %d) = %d, want a positive key", MaxZoom, last, last, k)
	}
	if z := atakZoomLevel(k); z != MaxZoom {
		t.Errorf("OSMUtils.getOSMDroidSQLiteZoomLevel(%d) = %d, want %d", k, z, MaxZoom)
	}
	// One zoom level above MaxZoom, ATAK decodes no zoom level, so the writer refuses it.
	if z := atakZoomLevel(tileKey(MaxZoom+1, 0, 0)); z != -1 {
		t.Errorf("OSMUtils.getOSMDroidSQLiteZoomLevel of zoom level %d = %d, want -1", MaxZoom+1, z)
	}
}

// atakZoomLevel is OSMUtils.getOSMDroidSQLiteZoomLevel of ATAK-CIV, with getOSMDroidSQLiteMinIndex.
func atakZoomLevel(index int64) int {
	minIndex := func(z int) int64 {
		if z < 0 || z > 28 {
			return -1
		}
		return int64(z) << (z * 2)
	}
	for i := range 29 {
		if index <= minIndex(i+1)-1 {
			return i
		}
	}
	return -1
}

func TestWriterRefusesAZoomLevelAboveMaxZoom(t *testing.T) {
	t.Run("Open refuses a job above MaxZoom", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "out.sqlite")
		meta := map[string]string{"atakdl_source": "Test XYZ", "minzoom": "10", "maxzoom": fmt.Sprint(MaxZoom + 1)}
		w, err := Open(path, meta)
		if err == nil {
			closeWriter(t, w)
		}
		if !errors.Is(err, ErrZoomOutOfRange) || !strings.HasPrefix(err.Error(), "zoom_out_of_range") {
			t.Fatalf("Open error = %v, want ErrZoomOutOfRange with the code zoom_out_of_range", err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Stat(%s) error = %v, want no output file", path, err)
		}
	})
	t.Run("Put refuses a tile above MaxZoom", func(t *testing.T) {
		w, _ := create(t, map[string]string{"atakdl_source": "Test XYZ"})
		if err := w.Put(MaxZoom+1, 0, 0, []byte("tile")); !errors.Is(err, ErrZoomOutOfRange) {
			t.Errorf("Put error = %v, want ErrZoomOutOfRange", err)
		}
		if w.Has(MaxZoom+1, 0, 0) {
			t.Error("Has = true, want false for a refused tile")
		}
		closeWriter(t, w)
	})
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
		t.Error("Has(3, 2, 6) = true, want false, because the key holds the XYZ row and not the TMS row")
	}
	closeWriter(t, w)
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

func TestReadInfoDetectsAnOSMDroidFile(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)

	info, err := output.ReadInfo(path)
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	want := output.Info{Format: output.OSMDroid, TileFormat: "png", Source: "Test XYZ", MinZoom: 2, MaxZoom: 10, Bounds: []float64{-122, 45, -121, 46}}
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

func TestOpenRejectsAnExistingFileInAnotherSRID(t *testing.T) {
	w, path := create(t, jobMetadata)
	closeWriter(t, w)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	_, err = db.ExecContext(t.Context(), "UPDATE ATAK_metadata SET value = '4326' WHERE key = 'srid'")
	if cerr := db.Close(); err != nil || cerr != nil {
		t.Fatalf("change the srid: %v, %v", err, cerr)
	}

	if w, err := Open(path, jobMetadata); err == nil {
		closeWriter(t, w)
		t.Fatal("Open = nil error, want an error for a file in EPSG:4326")
	}
}
