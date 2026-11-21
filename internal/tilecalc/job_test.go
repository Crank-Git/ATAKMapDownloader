package tilecalc

import (
	"math"
	"testing"

	"github.com/paulmach/orb"
)

// boundsArea returns a bounds area that holds exactly the tiles of tb, for the zoom levels minZoom to maxZoom.
func boundsArea(tb TileBounds, minZoom, maxZoom int) JobArea {
	edges := NewTileCalculator().TileBoundsToGeographic(&tb)
	// The tile edges belong to the next tile, so the bounds move inside them.
	const inset = 1e-6
	b := orb.Bound{Min: orb.Point{edges.MinLon + inset, edges.MinLat + inset}, Max: orb.Point{edges.MaxLon - inset, edges.MaxLat - inset}}
	return JobArea{Area: Area{Kind: AreaBounds, Geometry: b.ToPolygon()}, MinZoom: minZoom, MaxZoom: maxZoom}
}

// collectJobTiles returns the tiles that JobTiles sends for areas, in order.
// It fails the test when JobTiles sends one tile two times.
func collectJobTiles(t *testing.T, areas []JobArea) []Tile {
	t.Helper()
	seen := map[Tile]bool{}
	var got []Tile
	for tile := range JobTiles(t.Context(), areas) {
		if seen[tile] {
			t.Fatalf("JobTiles sent tile %v two times", tile)
		}
		seen[tile] = true
		got = append(got, tile)
	}
	return got
}

// westArea and eastArea are 3×3 tile areas at zoom level 10 that share 2×3 tiles.
var (
	westArea = TileBounds{MinX: 300, MaxX: 302, MinY: 400, MaxY: 402, Zoom: 10}
	eastArea = TileBounds{MinX: 301, MaxX: 303, MinY: 400, MaxY: 402, Zoom: 10}
)

func TestJobTilesSendsATileThatTwoAreasShareOnce(t *testing.T) {
	areas := []JobArea{boundsArea(westArea, 10, 10), boundsArea(eastArea, 10, 10)}
	got := collectJobTiles(t, areas)
	if len(got) != 12 {
		t.Fatalf("JobTiles sent %d tiles, want 12 (9 + 9 - 6 shared)", len(got))
	}
	// The areas go in list order, so the first 9 tiles are the tiles of the first area.
	for i, tile := range got[:9] {
		if !westArea.ContainsTile(tile.X, tile.Y) || tile.Z != 10 {
			t.Errorf("tile %d = %v, want a tile of the first area", i, tile)
		}
	}
	for i, tile := range got[9:] {
		if tile.X != 303 {
			t.Errorf("tile %d = %v, want a tile of column 303, which only the second area holds", 9+i, tile)
		}
	}
}

func TestJobTilesUsesTheZoomRangeOfEachArea(t *testing.T) {
	low := boundsArea(westArea, 10, 10)
	high := boundsArea(westArea, 10, 11)
	got := collectJobTiles(t, []JobArea{low, high})
	perZoom := map[int]int{}
	for _, tile := range got {
		perZoom[tile.Z]++
	}
	// The second area adds zoom level 11 only, because the first area holds its zoom level 10 tiles.
	if perZoom[10] != 9 || perZoom[11] != 36 || len(perZoom) != 2 {
		t.Errorf("tiles per zoom level = %v, want map[10:9 11:36]", perZoom)
	}
}

func TestCountJobTilesCountsEachAreaAndTheUniqueTiles(t *testing.T) {
	triangle := JobArea{Area: Area{Kind: AreaPolygon, Geometry: orb.Polygon{{
		{-105.2, 39.5}, {-104.6, 39.5}, {-105.2, 40.1}, {-105.2, 39.5},
	}}}, Name: "triangle", MinZoom: 9, MaxZoom: 12}
	route := JobArea{Area: Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{-105.3, 39.8}, {-104.5, 39.9}}, BufferM: 500},
		Name: "route", MinZoom: 11, MaxZoom: 13}
	tests := []struct {
		name  string
		areas []JobArea
	}{
		{"counts two bounds areas that share tiles", []JobArea{boundsArea(westArea, 10, 10), boundsArea(eastArea, 10, 10)}},
		{"counts a polygon and a route buffer with other zoom ranges", []JobArea{triangle, route}},
		{"counts one area", []JobArea{triangle}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountJobTiles(tt.areas)
			tiles := collectJobTiles(t, tt.areas)
			if got.Unique != len(tiles) {
				t.Errorf("Unique = %d, want %d, the count of JobTiles", got.Unique, len(tiles))
			}
			perZoom := map[int]int{}
			for _, tile := range tiles {
				perZoom[tile.Z]++
			}
			for z, n := range perZoom {
				if got.Zooms[z] != n {
					t.Errorf("Zooms[%d] = %d, want %d", z, got.Zooms[z], n)
				}
			}
			if len(got.Areas) != len(tt.areas) {
				t.Fatalf("Areas has %d counts, want %d", len(got.Areas), len(tt.areas))
			}
			sum := 0
			for i, a := range tt.areas {
				own := len(collectJobTiles(t, []JobArea{a}))
				if got.Areas[i] != own {
					t.Errorf("Areas[%d] = %d, want %d, the tiles of the area alone", i, got.Areas[i], own)
				}
				sum += own
			}
			if len(tt.areas) > 1 && got.Unique >= sum {
				t.Errorf("Unique = %d, want fewer than the sum %d, because the areas share tiles", got.Unique, sum)
			}
		})
	}
}

func TestJobBoundHoldsEveryAreaAndTheBuffer(t *testing.T) {
	polygon := JobArea{Area: Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 40}, {11, 40}, {10, 41}, {10, 40}}}}}
	route := JobArea{Area: Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{12, 45}, {13, 45}}, BufferM: 11132}}
	got := JobBound([]JobArea{polygon, route})
	// 11,132 m is 0.1° of latitude, and 0.1° / cos(45.1°) of longitude.
	wantLon := 13 + 0.1/math.Cos(45.1*math.Pi/180)
	if math.Abs(got.Min[0]-10) > 1e-9 || math.Abs(got.Min[1]-40) > 1e-9 ||
		math.Abs(got.Max[0]-wantLon) > 1e-3 || math.Abs(got.Max[1]-45.1) > 1e-3 {
		t.Errorf("JobBound = %v, want [[10 40] [%.4f 45.1]]", got, wantLon)
	}
}

func TestJobZoomRangeReturnsTheLowestAndHighestZoomLevel(t *testing.T) {
	minZoom, maxZoom := JobZoomRange([]JobArea{boundsArea(westArea, 8, 12), boundsArea(eastArea, 5, 9)})
	if minZoom != 5 || maxZoom != 12 {
		t.Errorf("JobZoomRange = %d, %d; want 5, 12", minZoom, maxZoom)
	}
}
