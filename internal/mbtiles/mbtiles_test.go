package mbtiles

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The four MBTiles statements must not change with the driver (FR-foundation-4).
// The failed tile table follows them (FR-robust-downloads-21). The ATAK reader reads only tiles.
// SQLite stores each statement text in sqlite_master without its IF NOT EXISTS clause.
var wantSchema = []string{
	"CREATE TABLE metadata (name text, value text)",
	"CREATE TABLE tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob)",
	"CREATE UNIQUE INDEX name ON metadata (name)",
	"CREATE UNIQUE INDEX tile_index ON tiles (zoom_level, tile_column, tile_row)",
	"CREATE TABLE atakdl_failed_tiles (zoom_level integer, tile_column integer, tile_row integer, reason text, attempts integer)",
	"CREATE UNIQUE INDEX atakdl_failed_tile_index ON atakdl_failed_tiles (zoom_level, tile_column, tile_row)",
}

// create returns a writer for a new output file in a temporary directory, and the path of the file.
func create(t *testing.T, metadata map[string]string) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.mbtiles")
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

// openFile opens the output file at path with a connection of its own, and closes it when the test ends.
func openFile(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

// count returns the number of rows in the tiles table of db.
func count(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM tiles").Scan(&n); err != nil {
		t.Fatalf("count tiles: %v", err)
	}
	return n
}

func TestOpenWritesTheMBTilesSchemaAndTheFailedTileTable(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	closeWriter(t, w)

	rows, err := openFile(t, path).QueryContext(t.Context(), "SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY rowid")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(got) != len(wantSchema) {
		t.Fatalf("got %d schema statements, want %d: %q", len(got), len(wantSchema), got)
	}
	for i := range wantSchema {
		if got[i] != wantSchema[i] {
			t.Errorf("statement %d = %q, want %q", i, got[i], wantSchema[i])
		}
	}
}

func TestOpenStoresTheMetadataKeys(t *testing.T) {
	meta := map[string]string{"name": "test", "format": "png", "minzoom": "0", "maxzoom": "2"}
	w, path := create(t, meta)
	closeWriter(t, w)

	db := openFile(t, path)
	for k, want := range meta {
		var got string
		if err := db.QueryRowContext(t.Context(), "SELECT value FROM metadata WHERE name = ?", k).Scan(&got); err != nil {
			t.Fatalf("read metadata %s: %v", k, err)
		}
		if got != want {
			t.Errorf("metadata %s = %q, want %q", k, got, want)
		}
	}
}

func TestOpenKeepsTheTilesOfAnExistingOutputFile(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	if err := w.Put(1, 0, 0, []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	w, err := Open(path, map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeWriter(t, w)
	if n := count(t, openFile(t, path)); n != 1 {
		t.Errorf("tiles table holds %d rows, want 1", n)
	}
}

func TestReadMetadata(t *testing.T) {
	w, path := create(t, map[string]string{"atakdl_source": "OSM"})
	closeWriter(t, w)

	tests := []struct {
		name      string
		key       string
		wantValue string
		wantOK    bool
	}{
		{"returns the value of a stored key", "atakdl_source", "OSM", true},
		{"reports false for a key that the file does not hold", "format", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, ok, err := ReadMetadata(path, tt.key)
			if err != nil || value != tt.wantValue || ok != tt.wantOK {
				t.Errorf("ReadMetadata(%q) = %q, %v, %v; want %q, %v, nil", tt.key, value, ok, err, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func TestReadMetadataRejectsAFileThatIsNotAnOutputFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadMetadata(path, "atakdl_source"); err == nil {
		t.Error("ReadMetadata on a text file returned no error")
	}
}

func TestOpenSetsWALAndTheBusyTimeout(t *testing.T) {
	w, _ := create(t, map[string]string{"name": "test"})
	t.Cleanup(func() { closeWriter(t, w) })

	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"busy_timeout", "5000"},
	}
	for _, tt := range tests {
		t.Run("sets "+tt.pragma+" to "+tt.want, func(t *testing.T) {
			var got string
			if err := w.db.QueryRowContext(t.Context(), "PRAGMA "+tt.pragma).Scan(&got); err != nil {
				t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
			}
			if strings.ToLower(got) != tt.want {
				t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
			}
		})
	}
}

func TestCloseLeavesOneFileInRollbackJournalMode(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	if err := w.Put(1, 0, 0, []byte{1}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	// ATAK opens the file read-only, so a -wal or -shm file next to it is not readable to ATAK.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Errorf("stat %s after Close: %v, want no file", filepath.Base(path+suffix), err)
		}
	}
	db := openFile(t, path)
	var mode string
	if err := db.QueryRowContext(t.Context(), "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if mode != "delete" {
		t.Errorf("journal_mode after Close = %q, want delete", mode)
	}
	if n := count(t, db); n != 1 {
		t.Errorf("tiles holds %d rows after Close, want 1", n)
	}
}

func TestPutStoresTheTMSRow(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	tiles := []struct {
		z, x, y, tmsY int
		data          []byte
	}{
		{0, 0, 0, 0, []byte("tile-0-0-0")},
		{1, 1, 1, 0, []byte("tile-1-1-1")},
		{2, 3, 2, 1, []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}},
	}
	for _, tl := range tiles {
		if err := w.Put(tl.z, tl.x, tl.y, tl.data); err != nil {
			t.Fatalf("Put(%d,%d,%d): %v", tl.z, tl.x, tl.y, err)
		}
	}
	closeWriter(t, w)

	db := openFile(t, path)
	for _, tl := range tiles {
		var got []byte
		err := db.QueryRowContext(t.Context(),
			"SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?",
			tl.z, tl.x, tl.tmsY,
		).Scan(&got)
		if err != nil {
			t.Fatalf("read tile %d/%d at TMS row %d: %v", tl.z, tl.x, tl.tmsY, err)
		}
		if !bytes.Equal(got, tl.data) {
			t.Errorf("tile %d/%d at TMS row %d = %v, want %v", tl.z, tl.x, tl.tmsY, got, tl.data)
		}
	}
	if n := count(t, db); n != len(tiles) {
		t.Errorf("tiles table holds %d rows, want %d", n, len(tiles))
	}
}

// The engine calls Put from one goroutine per tile.
func TestPutAcceptsConcurrentCallers(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})

	const callers = 32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Go(func() {
			errs <- w.Put(5, i, 0, bytes.Repeat([]byte{byte(i)}, 4096)) //nolint:gosec // i is below callers, so byte(i) does not overflow.
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Put: %v", err)
		}
	}
	closeWriter(t, w)

	if n := count(t, openFile(t, path)); n != callers {
		t.Errorf("tiles table holds %d rows, want %d", n, callers)
	}
}

func TestPutReplacesARowWithTheSameKey(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	for _, data := range []string{"old", "new"} {
		if err := w.Put(1, 0, 0, []byte(data)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	closeWriter(t, w)

	var got string
	var n int
	if err := openFile(t, path).QueryRowContext(t.Context(), "SELECT tile_data, COUNT(*) FROM tiles").Scan(&got, &n); err != nil {
		t.Fatalf("read tiles: %v", err)
	}
	if n != 1 || got != "new" {
		t.Errorf("tiles = %d rows, data %q; want 1 row, data %q", n, got, "new")
	}
}

func TestWriterCommitsOneTransactionPer500Tiles(t *testing.T) {
	tests := []struct {
		name  string
		tiles int
		want  int
	}{
		{"commits once for 1 tile", 1, 1},
		{"commits once for 500 tiles", 500, 1},
		{"commits 3 times for 1,001 tiles", 1001, 3},
		{"commits no tile transaction for 0 tiles", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, path := create(t, map[string]string{"name": "test"})
			for i := range tt.tiles {
				if err := w.Put(12, i, 0, []byte{1}); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			closeWriter(t, w)

			if w.commits != tt.want {
				t.Errorf("writer committed %d tile transactions, want %d", w.commits, tt.want)
			}
			if n := count(t, openFile(t, path)); n != tt.tiles {
				t.Errorf("tiles table holds %d rows, want %d", n, tt.tiles)
			}
		})
	}
}

func TestHasReportsTheTilesOfAnOpenedFileAndOfPut(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	if err := w.Put(3, 2, 1, []byte("stored")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !w.Has(3, 2, 1) {
		t.Error("Has(3, 2, 1) = false after Put, want true")
	}
	closeWriter(t, w)

	w, err := Open(path, map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { closeWriter(t, w) })

	tests := []struct {
		name    string
		z, x, y int
		want    bool
	}{
		{"reports the stored tile at its XYZ row", 3, 2, 1, true},
		{"does not report the TMS row of the stored tile", 3, 2, 6, false},
		{"does not report a tile at another zoom level", 4, 2, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := w.Has(tt.z, tt.x, tt.y); got != tt.want {
				t.Errorf("Has(%d, %d, %d) = %v, want %v", tt.z, tt.x, tt.y, got, tt.want)
			}
		})
	}
}

func TestPutReportsAWriteFailureAndCloseReturnsIt(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	// A missing tiles table makes the first transaction fail, as a full disk does.
	if _, err := openFile(t, path).ExecContext(t.Context(), "DROP TABLE tiles"); err != nil {
		t.Fatalf("drop the tiles table: %v", err)
	}

	var putErr error
	for i := 0; i < 2*batchSize && putErr == nil; i++ {
		putErr = w.Put(12, i, 0, []byte{1})
	}
	if putErr == nil {
		t.Error("Put returned no error after a failed transaction")
	}
	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("Close error = %v, want an error that names the missing table", err)
	}
}
