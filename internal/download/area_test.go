package download

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/paulmach/orb"
)

// overlapArea is a 3×3 tile area at zoom level 10 that shares 4 tiles with area.
var overlapArea = tilecalc.TileBounds{MinX: 301, MaxX: 303, MinY: 401, MaxY: 403, Zoom: 10}

// jobTiles returns the tiles that tilecalc.JobTiles selects for areas.
func jobTiles(t *testing.T, areas []tilecalc.JobArea) []testtiles.Tile {
	t.Helper()
	var tiles []testtiles.Tile
	for tile := range tilecalc.JobTiles(t.Context(), areas) {
		tiles = append(tiles, testtiles.Tile{Z: tile.Z, X: tile.X, Y: tile.Y})
	}
	return tiles
}

func TestEngineRequestsEachTileThatTwoAreasShareOnce(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	areas := append(areasOf(t, area), areasOf(t, overlapArea)...)

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areas, output, nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	// The two areas hold 9 tiles each and share 4, so the job has 14 unique tiles.
	const unique = 14
	if res.TotalTiles != unique || res.Downloaded != unique {
		t.Errorf("TotalTiles, Downloaded = %d, %d; want %d, %d", res.TotalTiles, res.Downloaded, unique, unique)
	}
	if n := srv.TotalRequests(); n != unique {
		t.Errorf("tile server got %d requests, want %d", n, unique)
	}
	for _, tb := range []tilecalc.TileBounds{area, overlapArea} {
		for _, tile := range tilesOf(tb) {
			if n := srv.Requests(tile); n != 1 {
				t.Errorf("tile %v got %d requests, want 1", tile, n)
			}
		}
	}
	if n := len(storedTiles(t, output)); n != unique {
		t.Errorf("output file holds %d tile rows, want %d", n, unique)
	}
}

func TestEngineWritesTheAreasOfTheJobToTheMetadata(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	low := areasOf(t, area)[0]
	high := areasOf(t, overlapArea)[0]
	high.Name, high.MinZoom, high.MaxZoom = "detail", 11, 12
	areas := []tilecalc.JobArea{low, high}

	if _, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areas, output, nil); err != nil {
		t.Fatalf("DownloadTiles: %v", err)
	}
	b := boundOf(area).Union(boundOf(overlapArea))
	want := map[string]string{
		"bounds":  fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", b.Min[0], b.Min[1], b.Max[0], b.Max[1]),
		"minzoom": "10",
		"maxzoom": "12",
	}
	for key, value := range want {
		if got, ok, err := mbtiles.ReadMetadata(output, key); err != nil || !ok || got != value {
			t.Errorf("metadata %s = %q, %v, %v; want %q, true, nil", key, got, ok, err, value)
		}
	}
	stored, ok, err := mbtiles.ReadMetadata(output, "atakdl_areas")
	if err != nil || !ok {
		t.Fatalf("metadata atakdl_areas = %v, %v; want a row", ok, err)
	}
	got, err := tilecalc.DecodeAreas([]byte(stored))
	if err != nil {
		t.Fatalf("DecodeAreas(atakdl_areas) = %v", err)
	}
	if !reflect.DeepEqual(got, areas) {
		t.Errorf("atakdl_areas holds %#v, want %#v", got, areas)
	}
}

func TestEngineResumesAPolygonJobWithOnlyTheMissingTiles(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	square := tilecalc.TileBounds{MinX: 300, MaxX: 309, MinY: 400, MaxY: 409, Zoom: 10}
	b := boundOf(square)
	triangle := []tilecalc.JobArea{{
		Area:    tilecalc.Area{Kind: tilecalc.AreaPolygon, Geometry: orb.Polygon{{b.Min, {b.Max[0], b.Min[1]}, {b.Min[0], b.Max[1]}, b.Min}}},
		MinZoom: 10, MaxZoom: 10,
	}}
	all := jobTiles(t, triangle)
	if len(all) >= 100 || len(all) < 10 {
		t.Fatalf("the triangle selects %d tiles, want fewer than the 100 of its bounding rectangle", len(all))
	}
	stored := all[:len(all)/2]
	storeTiles(t, output, stored)

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, triangle, output, nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	checkRequestsOfMissingTiles(t, srv, all, stored)
	if n := srv.TotalRequests(); n != len(all)-len(stored) {
		t.Errorf("tile server got %d requests, want %d, the missing tiles of the triangle", n, len(all)-len(stored))
	}
	if res.Skipped != int64(len(stored)) {
		t.Errorf("Skipped = %d, want %d", res.Skipped, len(stored))
	}
}
