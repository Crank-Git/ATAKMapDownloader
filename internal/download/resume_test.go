package download

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// storeTiles writes a tile for each XYZ tile in tiles to a new output file at path.
func storeTiles(t *testing.T, path string, tiles []testtiles.Tile) {
	t.Helper()
	w, err := mbtiles.Open(path, map[string]string{"atakdl_source": "Test XYZ"})
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	for _, tile := range tiles {
		if err := w.Put(tile.Z, tile.X, tile.Y, []byte("stored")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}
}

// tilesOf returns the XYZ tiles of tb.
func tilesOf(tb tilecalc.TileBounds) []testtiles.Tile {
	var tiles []testtiles.Tile
	for pair := range tb.IterTiles(context.Background()) {
		tiles = append(tiles, testtiles.Tile{Z: tb.Zoom, X: pair[0], Y: pair[1]})
	}
	return tiles
}

func TestEngineSkipsTilesThatTheOutputFileHolds(t *testing.T) {
	all := tilesOf(area)
	tests := []struct {
		name   string
		stored []testtiles.Tile
	}{
		{"requests only the 5 tiles that the output file does not hold", all[:4]},
		{"sends no request and succeeds when the output file holds every tile", all},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")
			storeTiles(t, output, tt.stored)

			res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), output, nil)
			if !res.OK || err != nil {
				t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
			}
			checkRequestsOfMissingTiles(t, srv, all, tt.stored)
			wantSkipped := int64(len(tt.stored))
			if res.Skipped != wantSkipped || res.Downloaded != int64(len(all))-wantSkipped {
				t.Errorf("Skipped, Downloaded = %d, %d; want %d, %d", res.Skipped, res.Downloaded, wantSkipped, int64(len(all))-wantSkipped)
			}
			if n := len(storedTiles(t, output)); n != len(all) {
				t.Errorf("output file holds %d tile rows, want %d", n, len(all))
			}
		})
	}
}

// checkRequestsOfMissingTiles reports an error when a tile of all that stored omits did not get exactly one request,
// or when a tile in stored got a request.
func checkRequestsOfMissingTiles(t *testing.T, srv *testtiles.Server, all, stored []testtiles.Tile) {
	t.Helper()
	want := make(map[testtiles.Tile]int)
	for _, tile := range all {
		want[tile] = 1
	}
	for _, tile := range stored {
		want[tile] = 0
	}
	for tile, n := range want {
		if got := srv.Requests(tile); got != n {
			t.Errorf("tile %v got %d requests, want %d", tile, got, n)
		}
	}
}

func TestEngineWritesTheMapSourceNameToTheMetadata(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	path := filepath.Join(t.TempDir(), "out.mbtiles")

	if _, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, area), path, nil); err != nil {
		t.Fatalf("DownloadTiles: %v", err)
	}
	info, err := output.ReadInfo(path)
	if err != nil || info.Source != "Test XYZ" {
		t.Errorf("ReadInfo = %+v, %v; want source %q", info, err, "Test XYZ")
	}
}

func TestEngineKeepsTheTilesOfACancelledJobAndResumesIt(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	tiles := tilecalc.TileBounds{MinX: 300, MaxX: 309, MinY: 400, MaxY: 409, Zoom: 10}
	const total = 100

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cb := func(p Progress) bool {
		if p.Downloaded >= total/2 {
			cancel()
		}
		return true
	}
	_, err := politeEngine(testConfig(), 1e6).DownloadTiles(ctx, source, areasOf(t, tiles), output, cb)
	if err == nil {
		t.Fatal("DownloadTiles after a cancel returned no error")
	}
	stored := len(storedTiles(t, output))
	if stored == 0 || stored == total {
		t.Fatalf("output file of the cancelled job holds %d tile rows, want more than 0 and fewer than %d", stored, total)
	}

	before := srv.TotalRequests()
	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, tiles), output, nil)
	if !res.OK || err != nil {
		t.Fatalf("resumed DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	if n := srv.TotalRequests() - before; n != total-stored {
		t.Errorf("the resumed job sent %d requests, want %d", n, total-stored)
	}
	if res.Skipped != int64(stored) {
		t.Errorf("Skipped = %d, want %d", res.Skipped, stored)
	}
	if n := len(storedTiles(t, output)); n != total {
		t.Errorf("output file holds %d tile rows, want %d", n, total)
	}
}
