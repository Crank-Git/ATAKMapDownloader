package mbtiles

import (
	"bytes"
	"path/filepath"
	"sync"
	"testing"
)

// The output file schema must not change with the driver (FR-foundation-4).
// SQLite stores each statement text in sqlite_master as Create wrote it.
var wantSchema = []string{
	"CREATE TABLE metadata (name text, value text)",
	"CREATE TABLE tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob)",
	"CREATE UNIQUE INDEX name ON metadata (name)",
	"CREATE UNIQUE INDEX tile_index ON tiles (zoom_level, tile_column, tile_row)",
}

func TestCreateWritesTheFourSchemaStatements(t *testing.T) {
	db, err := Create(filepath.Join(t.TempDir(), "out.mbtiles"), map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	rows, err := db.Query("SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY rowid")
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()

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
	db, err := Create(filepath.Join(t.TempDir(), "out.mbtiles"), meta)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	for k, want := range meta {
		var got string
		if err := db.QueryRow("SELECT value FROM metadata WHERE name = ?", k).Scan(&got); err != nil {
			t.Fatalf("read metadata %s: %v", k, err)
		}
		if got != want {
			t.Errorf("metadata %s = %q, want %q", k, got, want)
		}
	}
}

func TestInsertTileStoresTheTMSRow(t *testing.T) {
	db, err := Create(filepath.Join(t.TempDir(), "out.mbtiles"), map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	tiles := []struct {
		z, x, tmsY int
		data       []byte
	}{
		{0, 0, 0, []byte("tile-0-0-0")},
		{1, 1, 0, []byte("tile-1-1-0")},
		{2, 3, 1, []byte{0x89, 'P', 'N', 'G', 0x00, 0xff}},
	}
	for _, tl := range tiles {
		if err := InsertTile(db, tl.z, tl.x, tl.tmsY, tl.data); err != nil {
			t.Fatalf("InsertTile(%d,%d,%d): %v", tl.z, tl.x, tl.tmsY, err)
		}
	}

	for _, tl := range tiles {
		var got []byte
		err := db.QueryRow(
			"SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?",
			tl.z, tl.x, tl.tmsY,
		).Scan(&got)
		if err != nil {
			t.Fatalf("read tile %d/%d/%d: %v", tl.z, tl.x, tl.tmsY, err)
		}
		if !bytes.Equal(got, tl.data) {
			t.Errorf("tile %d/%d/%d = %v, want %v", tl.z, tl.x, tl.tmsY, got, tl.data)
		}
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM tiles").Scan(&n); err != nil {
		t.Fatalf("count tiles: %v", err)
	}
	if n != len(tiles) {
		t.Errorf("tiles table holds %d rows, want %d", n, len(tiles))
	}
}

// The engine calls InsertTile from one goroutine per tile.
func TestInsertTileAcceptsConcurrentWriters(t *testing.T) {
	db, err := Create(filepath.Join(t.TempDir(), "out.mbtiles"), map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	const writers = 32
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- InsertTile(db, 5, i, 0, bytes.Repeat([]byte{byte(i)}, 4096)) //nolint:gosec // i is below writers, so byte(i) does not overflow.
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("InsertTile: %v", err)
		}
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM tiles").Scan(&n); err != nil {
		t.Fatalf("count tiles: %v", err)
	}
	if n != writers {
		t.Errorf("tiles table holds %d rows, want %d", n, writers)
	}
}

func TestInsertTileReplacesARowWithTheSameKey(t *testing.T) {
	db, err := Create(filepath.Join(t.TempDir(), "out.mbtiles"), map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer db.Close()

	if err := InsertTile(db, 1, 0, 1, []byte("old")); err != nil {
		t.Fatalf("InsertTile: %v", err)
	}
	if err := InsertTile(db, 1, 0, 1, []byte("new")); err != nil {
		t.Fatalf("InsertTile: %v", err)
	}

	var got string
	var n int
	if err := db.QueryRow("SELECT tile_data, COUNT(*) FROM tiles").Scan(&got, &n); err != nil {
		t.Fatalf("read tiles: %v", err)
	}
	if n != 1 || got != "new" {
		t.Errorf("tiles = %d rows, data %q; want 1 row, data %q", n, got, "new")
	}
}
