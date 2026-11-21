package mbtiles

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The output file schema must not change with the driver (FR-foundation-4).
// SQLite stores each statement text in sqlite_master without its IF NOT EXISTS clause.
var wantSchema = []string{
	"CREATE TABLE metadata (name text, value text)",
	"CREATE TABLE tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob)",
	"CREATE UNIQUE INDEX name ON metadata (name)",
	"CREATE UNIQUE INDEX tile_index ON tiles (zoom_level, tile_column, tile_row)",
}

// create returns a writer for a new output file in a temporary directory, and the path of the file.
func create(t *testing.T, metadata map[string]string) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.mbtiles")
	w, err := Create(path, metadata)
	if err != nil {
		t.Fatalf("Create: %v", err)
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

func TestCreateWritesTheFourSchemaStatements(t *testing.T) {
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

func TestCreateStoresTheMetadataKeys(t *testing.T) {
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

func TestCreateDeletesAnExistingOutputFile(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	if err := w.Put(1, 0, 0, []byte("old")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter(t, w)

	w, err := Create(path, map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w.Has(1, 0, 0) {
		t.Error("Has(1, 0, 0) = true after Create, want false")
	}
	closeWriter(t, w)
	if n := count(t, openFile(t, path)); n != 0 {
		t.Errorf("tiles table holds %d rows, want 0", n)
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
