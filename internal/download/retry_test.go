package download

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
)

// failedRow is one row of the atakdl_failed_tiles table.
type failedRow struct {
	reason   string
	attempts int
}

// failedRows returns the rows of the atakdl_failed_tiles table of the output file at path, keyed by XYZ tile.
func failedRows(t *testing.T, path string) map[testtiles.Tile]failedRow {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close output file: %v", err)
		}
	}()

	rows, err := db.QueryContext(t.Context(), "SELECT zoom_level, tile_column, tile_row, reason, attempts FROM atakdl_failed_tiles")
	if err != nil {
		t.Fatalf("query atakdl_failed_tiles: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	got := make(map[testtiles.Tile]failedRow)
	for rows.Next() {
		var tile testtiles.Tile
		var row failedRow
		if err := rows.Scan(&tile.Z, &tile.X, &tile.Y, &row.reason, &row.attempts); err != nil {
			t.Fatalf("scan failed tile row: %v", err)
		}
		// The table stores TMS rows, and the tests name tiles by XYZ row.
		tile.Y = (1 << tile.Z) - 1 - tile.Y
		got[tile] = row
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read failed tile rows: %v", err)
	}
	return got
}

// hasTile reports whether the output file at path holds data for the XYZ tile.
func hasTile(t *testing.T, path string, tile testtiles.Tile) bool {
	t.Helper()
	tms := testtiles.Tile{Z: tile.Z, X: tile.X, Y: (1 << tile.Z) - 1 - tile.Y}
	return len(storedTiles(t, path)[tms]) > 0
}

// retryEngine returns an engine with its own host gates and a short backoff, so a test of 503 responses is fast.
func retryEngine() *Engine {
	e := politeEngine(testConfig(), 1e6)
	e.backoffBase = time.Millisecond
	return e
}

func TestEngineStoresATileThatFailsWith503AndThenSucceeds(t *testing.T) {
	tests := []struct {
		name         string
		failures     int
		wantRequests int
	}{
		{"stores the tile after one 503 in the main pass", 1, 2},
		{"stores the tile in the retry pass after 3 responses with 503 in the main pass", maxAttempts, maxAttempts + retryAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
			srv := testtiles.New(t, testtiles.Table{tile: {Kind: testtiles.Unavailable, Times: tt.failures}})
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")

			res, err := retryEngine().DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, output, nil)
			if !res.OK || err != nil {
				t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
			}
			if res.Downloaded != 1 || res.Failed != 0 {
				t.Errorf("DownloadTiles counts %d downloaded and %d failed, want 1 and 0", res.Downloaded, res.Failed)
			}
			if !hasTile(t, output, tile) {
				t.Error("tiles has no row for the tile")
			}
			if rows := failedRows(t, output); len(rows) != 0 {
				t.Errorf("atakdl_failed_tiles = %v, want no rows", rows)
			}
			if n := srv.Requests(tile); n != tt.wantRequests {
				t.Errorf("tile got %d requests, want %d", n, tt.wantRequests)
			}
		})
	}
}

func TestEngineRecordsAFailedTileWithItsReason(t *testing.T) {
	tests := []struct {
		name         string
		kind         testtiles.Kind
		want         failedRow
		wantRequests int
	}{
		{"records a 404 as not_found and does not retry it", testtiles.NotFound, failedRow{"not_found", 1}, 1},
		{"records an HTML body as not_image after the retry pass", testtiles.HTML, failedRow{"not_image", 2}, 2},
		{"records a 503 as http_503 after one request in the retry pass", testtiles.Unavailable, failedRow{"http_503", 2}, maxAttempts + retryAttempts},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
			srv := testtiles.New(t, testtiles.Table{tile: {Kind: tt.kind}})
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")

			res, _ := retryEngine().DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, output, nil) // The test checks the failed tile row, not the job error.
			if res.Downloaded != 0 || res.Failed != 1 {
				t.Errorf("DownloadTiles counts %d downloaded and %d failed, want 0 and 1", res.Downloaded, res.Failed)
			}
			want := map[testtiles.Tile]failedRow{tile: tt.want}
			if got := failedRows(t, output); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("atakdl_failed_tiles = %v, want %v", got, want)
			}
			if n := srv.Requests(tile); n != tt.wantRequests {
				t.Errorf("tile got %d requests, want %d", n, tt.wantRequests)
			}
		})
	}
}

func TestEngineRetriesTheFailedTilesOfAResumedFile(t *testing.T) {
	jobTile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
	// The two failed tiles are outside the bounds of the resumed job, so only the retry pass can request them.
	retried := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX + 5, Y: singleTile.MinY}
	notFound := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX + 6, Y: singleTile.MinY}
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")

	writeFailedTiles(t, output, map[testtiles.Tile]string{retried: "http_503", notFound: "not_found"})

	res, err := retryEngine().DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, output, nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	if res.Downloaded != 2 || res.Failed != 0 {
		t.Errorf("DownloadTiles counts %d downloaded and %d failed, want 2 and 0", res.Downloaded, res.Failed)
	}
	for _, tile := range []testtiles.Tile{jobTile, retried} {
		if !hasTile(t, output, tile) {
			t.Errorf("tiles has no row for tile %v", tile)
		}
	}
	want := map[testtiles.Tile]failedRow{notFound: {"not_found", 1}}
	if got := failedRows(t, output); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("atakdl_failed_tiles = %v, want %v", got, want)
	}
	if n := srv.Requests(notFound); n != 0 {
		t.Errorf("the not_found tile got %d requests, want 0", n)
	}
}

func TestEngineStopsTheRetryPassWithin500msOfACancel(t *testing.T) {
	// Each retried tile holds the one slot for 2 s, so the cancel arrives while the retry pass waits for the slot.
	table := testtiles.Table{}
	failed := map[testtiles.Tile]string{}
	var retried []testtiles.Tile
	for i := range 5 {
		tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX + 5 + i, Y: singleTile.MinY}
		table[tile] = testtiles.Response{Kind: testtiles.PNG, Delay: 2 * time.Second}
		failed[tile] = "http_503"
		retried = append(retried, tile)
	}
	srv := testtiles.New(t, table)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	writeFailedTiles(t, output, failed)

	cfg := testConfig()
	cfg.MaxConcurrent = 1
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	var res Result
	go func() {
		var err error
		res, err = politeEngine(cfg, 1e6).DownloadTiles(ctx, source, boundsOf(t, singleTile), []int{singleTile.Zoom}, output, nil)
		done <- err
	}()
	for srv.Requests(retried[0]) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	var stop cancelTime
	stop.mark()
	cancel()
	checkStopWithin500ms(t, srv, done, &stop)

	// The cancel ends the retried tile, so the tile is neither downloaded nor failed.
	if res.Downloaded != 1 || res.Failed != 0 {
		t.Errorf("DownloadTiles counts %d downloaded and %d failed, want 1 and 0", res.Downloaded, res.Failed)
	}
	for _, tile := range retried[1:] {
		if n := srv.Requests(tile); n != 0 {
			t.Errorf("tile %v got %d requests after the cancel, want 0", tile, n)
		}
	}
	if rows := failedRows(t, output); len(rows) != len(retried) {
		t.Errorf("atakdl_failed_tiles holds %d rows, want %d: a cancel records no failure", len(rows), len(retried))
	}
}

// writeFailedTiles writes an output file at path whose atakdl_failed_tiles table holds each tile in failed with its reason.
func writeFailedTiles(t *testing.T, path string, failed map[testtiles.Tile]string) {
	t.Helper()
	w, err := mbtiles.Open(path, map[string]string{"atakdl_source": "Test XYZ"})
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	for tile, reason := range failed {
		if err := w.Fail(tile.Z, tile.X, tile.Y, reason); err != nil {
			t.Fatalf("Fail: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}
}
