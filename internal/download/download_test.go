package download

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// fixtureHost is the URL prefix of every fixture source file in testdata/sources.
const fixtureHost = "http://tiles.test"

// area is a 3×3 tile area at zoom level 10.
var area = tilecalc.TileBounds{MinX: 300, MaxX: 302, MinY: 400, MaxY: 402, Zoom: 10}

// loadFixture returns the fixture map source in file, pointed at srv.
func loadFixture(t *testing.T, file string, srv *testtiles.Server) *xmlparser.MapSource {
	t.Helper()
	source, err := xmlparser.New("").ParseFile(filepath.Join("..", "..", "testdata", "sources", file))
	if err != nil || source == nil {
		t.Fatalf("parse fixture %s: source %v, error %v", file, source, err)
	}
	source.URL = strings.Replace(source.URL, fixtureHost, srv.URL, 1)
	return source
}

// areaBounds returns bounds that hold exactly the tiles of area.
func areaBounds(t *testing.T) *tilecalc.GeographicBounds {
	t.Helper()
	return boundsOf(t, area)
}

// boundsOf returns bounds that hold exactly the tiles of tb.
func boundsOf(t *testing.T, tb tilecalc.TileBounds) *tilecalc.GeographicBounds {
	t.Helper()
	edges := tilecalc.NewTileCalculator().TileBoundsToGeographic(&tb)
	// The tile edges belong to the next tile, so the bounds move inside them.
	const inset = 1e-6
	bounds, err := tilecalc.NewGeographicBounds(edges.MinLat+inset, edges.MaxLat-inset, edges.MinLon+inset, edges.MaxLon-inset)
	if err != nil {
		t.Fatalf("make bounds: %v", err)
	}
	return bounds
}

// testConfig returns the default engine configuration with a short request timeout.
func testConfig() Config {
	cfg := DefaultConfig()
	cfg.Timeout = 5 * time.Second
	return cfg
}

// storedTiles returns the tile rows of the output file at path, keyed by TMS tile.
func storedTiles(t *testing.T, path string) map[testtiles.Tile][]byte {
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

	rows, err := db.QueryContext(t.Context(), "SELECT zoom_level, tile_column, tile_row, tile_data FROM tiles")
	if err != nil {
		t.Fatalf("query tiles: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	tiles := make(map[testtiles.Tile][]byte)
	for rows.Next() {
		var tile testtiles.Tile
		var data []byte
		if err := rows.Scan(&tile.Z, &tile.X, &tile.Y, &data); err != nil {
			t.Fatalf("scan tile row: %v", err)
		}
		tiles[tile] = data
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read tile rows: %v", err)
	}
	return tiles
}

// checkTile reports an error when the XYZ tile x, y of area is missing from tiles at its TMS row,
// or when srv did not get exactly one request for it.
func checkTile(t *testing.T, srv *testtiles.Server, tiles map[testtiles.Tile][]byte, x, y int) {
	t.Helper()
	tmsY := (1 << area.Zoom) - 1 - y
	if len(tiles[testtiles.Tile{Z: area.Zoom, X: x, Y: tmsY}]) == 0 {
		t.Errorf("output file holds no data for tile %d/%d/%d at TMS row %d", area.Zoom, x, y, tmsY)
	}
	if n := srv.Requests(testtiles.Tile{Z: area.Zoom, X: x, Y: y}); n != 1 {
		t.Errorf("tile %d/%d/%d got %d requests, want 1", area.Zoom, x, y, n)
	}
}

// errorText returns the message of err, or an empty string for nil.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestEngineDownloadsA3x3TileArea(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
	}{
		{"downloads 9 tiles from an XYZ map source", "xyz.xml"},
		{"downloads 9 tiles from a quadkey map source", "quadkey.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			source := loadFixture(t, tt.fixture, srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")

			res, err := NewEngine(testConfig()).DownloadTiles(t.Context(), source, areaBounds(t), []int{area.Zoom}, output, nil)
			if !res.OK || err != nil {
				t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
			}

			tiles := storedTiles(t, output)
			if len(tiles) != 9 {
				t.Fatalf("output file holds %d tile rows, want 9", len(tiles))
			}
			for pair := range area.IterTiles(t.Context()) {
				checkTile(t, srv, tiles, pair[0], pair[1])
			}
		})
	}
}

func TestEngineFailureTolerance(t *testing.T) {
	missing := func(xs ...int) testtiles.Table {
		table := testtiles.Table{}
		for _, x := range xs {
			table[testtiles.Tile{Z: area.Zoom, X: x, Y: area.MinY}] = testtiles.Response{Kind: testtiles.NotFound}
		}
		return table
	}
	tests := []struct {
		name      string
		table     testtiles.Table
		wantOK    bool
		wantError string
		wantRows  int
	}{
		{
			name:     "succeeds when 1 of 9 tiles fails, below the failure tolerance",
			table:    missing(area.MinX),
			wantOK:   true,
			wantRows: 8,
		},
		{
			name:      "fails when 3 of 9 tiles fail, above the failure tolerance",
			table:     missing(area.MinX, area.MinX+1, area.MaxX),
			wantError: "failure rate 33.3% exceeds threshold 20.0%",
			wantRows:  6,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, tt.table)
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")

			res, err := NewEngine(testConfig()).DownloadTiles(t.Context(), source, areaBounds(t), []int{area.Zoom}, output, nil)
			if res.OK != tt.wantOK {
				t.Errorf("DownloadTiles ok = %v, want %v", res.OK, tt.wantOK)
			}
			if got := errorText(err); got != tt.wantError {
				t.Errorf("DownloadTiles error = %q, want %q", got, tt.wantError)
			}
			if rows := len(storedTiles(t, output)); rows != tt.wantRows {
				t.Errorf("output file holds %d tile rows, want %d", rows, tt.wantRows)
			}
			for tile := range tt.table {
				if n := srv.Requests(tile); n != 1 {
					t.Errorf("tile %v with status 404 got %d requests, want 1", tile, n)
				}
			}
		})
	}
}

// oneTile is the first tile of area. The TLS tests use one tile, because the engine retries
// a failed handshake with waits of 1 s and 2 s.
var oneTile = tilecalc.TileBounds{MinX: area.MinX, MaxX: area.MinX, MinY: area.MinY, MaxY: area.MinY, Zoom: area.Zoom}

func TestEngineRejectsASelfSignedCertificateByDefault(t *testing.T) {
	srv := testtiles.NewTLS(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	e := NewEngine(testConfig())
	// The retry pass repeats the handshake waits, so a short backoff keeps the test fast.
	e.backoffBase = time.Millisecond

	res, err := e.DownloadTiles(t.Context(), source, boundsOf(t, oneTile), []int{oneTile.Zoom}, output, nil)
	if res.OK {
		t.Errorf("DownloadTiles ok = true, want false")
	}
	if want := "failure rate 100.0% exceeds threshold 20.0%"; errorText(err) != want {
		t.Errorf("DownloadTiles error = %q, want %q", errorText(err), want)
	}
	if rows := len(storedTiles(t, output)); rows != 0 {
		t.Errorf("output file holds %d tile rows, want 0", rows)
	}
	// The handshake fails before the client sends a request, so the handler counts none.
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0", n)
	}
}

func TestEngineAcceptsASelfSignedCertificateWhenInsecure(t *testing.T) {
	srv := testtiles.NewTLS(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	cfg := testConfig()
	cfg.VerifyTLS = false

	res, err := NewEngine(cfg).DownloadTiles(t.Context(), source, areaBounds(t), []int{area.Zoom}, output, nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	tiles := storedTiles(t, output)
	if len(tiles) != 9 {
		t.Fatalf("output file holds %d tile rows, want 9", len(tiles))
	}
	for pair := range area.IterTiles(t.Context()) {
		checkTile(t, srv, tiles, pair[0], pair[1])
	}
}

// largeArea is a 100×100 tile area at zoom level 10, for FR-robust-downloads-5.
var largeArea = tilecalc.TileBounds{MinX: 300, MaxX: 399, MinY: 400, MaxY: 499, Zoom: 10}

func TestEngineDownloadsA10000TileJobWith16Slots(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	cfg := testConfig()
	cfg.MaxConcurrent = 16

	var calls atomic.Int64
	res, err := politeEngine(cfg, 1e6).DownloadTiles(t.Context(), source, boundsOf(t, largeArea), []int{largeArea.Zoom}, output,
		func(Progress) bool {
			calls.Add(1)
			return true
		})
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}

	const want = 10000
	if res.Downloaded != want || res.Failed != 0 || res.TotalTiles != want {
		t.Errorf("result = %d downloaded, %d failed of %d; want %d, 0 of %d", res.Downloaded, res.Failed, res.TotalTiles, want, want)
	}
	if n := len(storedTiles(t, output)); n != want {
		t.Errorf("output file holds %d tile rows, want %d", n, want)
	}
	if n := srv.TotalRequests(); n != want {
		t.Errorf("server got %d tile requests, want %d", n, want)
	}
	// The engine calls back once per tile and once when the job ends.
	if n := calls.Load(); n != want+1 {
		t.Errorf("callback ran %d times, want %d", n, want+1)
	}
}

func TestEngineStopsTheJobWhenTheWriterFails(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	// 600 tiles fill one transaction of 500 tiles, so the writer fails during the job.
	tiles := tilecalc.TileBounds{MinX: 300, MaxX: 329, MinY: 400, MaxY: 419, Zoom: 10}

	// The writer has no open transaction before tile 500, so a second connection can drop the table.
	// A missing tiles table makes the writer fail, as a full disk does.
	var drop sync.Once
	cb := func(Progress) bool {
		drop.Do(func() {
			db, err := sql.Open("sqlite", output+"?_busy_timeout=5000")
			if err != nil {
				t.Errorf("open output file: %v", err)
				return
			}
			defer func() { _ = db.Close() }() // The DROP below already reports a failure of the file.
			if _, err := db.ExecContext(t.Context(), "DROP TABLE tiles"); err != nil {
				t.Errorf("drop the tiles table: %v", err)
			}
		})
		return true
	}
	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, boundsOf(t, tiles), []int{tiles.Zoom}, output, cb)
	if res.OK || !errors.Is(err, ErrWriteFailed) || !strings.HasPrefix(errorText(err), "write_failed") {
		t.Errorf("DownloadTiles = %v, %v; want false and an error that starts with write_failed", res.OK, err)
	}
	if _, err := os.Stat(output); err != nil {
		t.Errorf("output file after a write failure: %v, want the file kept", err)
	}
}

func TestEngineReturnsWriteFailedWhenTheLastCommitFailsAfterACancel(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// The job holds fewer than 500 tiles, so only the commit of Close writes them, and the missing table makes it fail.
	var drop sync.Once
	cb := func(Progress) bool {
		drop.Do(func() {
			db, err := sql.Open("sqlite", output+"?_busy_timeout=5000")
			if err != nil {
				t.Errorf("open output file: %v", err)
				return
			}
			defer func() { _ = db.Close() }() // The DROP below already reports a failure of the file.
			if _, err := db.ExecContext(t.Context(), "DROP TABLE tiles"); err != nil {
				t.Errorf("drop the tiles table: %v", err)
			}
			cancel()
		})
		return true
	}
	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(ctx, source, areaBounds(t), []int{area.Zoom}, output, cb)
	if res.OK || !errors.Is(err, ErrWriteFailed) {
		t.Errorf("DownloadTiles = %v, %v; want false and write_failed, because a lost tile outranks the cancel", res.OK, err)
	}
}
