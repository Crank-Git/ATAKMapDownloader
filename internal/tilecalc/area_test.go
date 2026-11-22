package tilecalc

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/paulmach/orb"
)

// collectTiles returns every tile that a.Tiles sends at zoom level z, as a set.
func collectTiles(t *testing.T, a Area, z int) map[[2]int]bool {
	t.Helper()
	got := map[[2]int]bool{}
	for tile := range a.Tiles(t.Context(), z) {
		if got[tile] {
			t.Fatalf("Tiles sent tile %v two times", tile)
		}
		got[tile] = true
	}
	return got
}

// tileGeo returns the geographic bounds of one tile through TileBoundsToGeographic.
func tileGeo(x, y, z int) *GeographicBounds {
	return NewTileCalculator().TileBoundsToGeographic(&TileBounds{MinX: x, MaxX: x, MinY: y, MaxY: y, Zoom: z})
}

// mercY returns the Web Mercator northing of lat in degrees. It is separate from the code under test, so that the test oracle does not share the code under test.
func mercY(lat float64) float64 {
	return math.Log(math.Tan(math.Pi/4 + lat*math.Pi/360))
}

type rect struct{ minX, minY, maxX, maxY float64 }

func (r rect) contains(p orb.Point) bool {
	return p[0] >= r.minX && p[0] <= r.maxX && p[1] >= r.minY && p[1] <= r.maxY
}

func cross(o, a, b orb.Point) float64 {
	return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0])
}

func segmentsCross(a, b, c, d orb.Point) bool {
	d1, d2 := cross(c, d, a), cross(c, d, b)
	d3, d4 := cross(a, b, c), cross(a, b, d)
	return ((d1 > 0) != (d2 > 0)) && ((d3 > 0) != (d4 > 0))
}

func segmentTouchesRect(a, b orb.Point, r rect) bool {
	if r.contains(a) || r.contains(b) {
		return true
	}
	corners := []orb.Point{{r.minX, r.minY}, {r.maxX, r.minY}, {r.maxX, r.maxY}, {r.minX, r.maxY}}
	for i := range corners {
		if segmentsCross(a, b, corners[i], corners[(i+1)%4]) {
			return true
		}
	}
	return false
}

func mercPoint(p orb.Point) orb.Point { return orb.Point{p[0], mercY(p[1])} }

// oracleIntersects reports whether a tile square meets the polygon, with a brute-force test in Web Mercator.
func oracleIntersects(p orb.Polygon, g *GeographicBounds) bool {
	r := rect{g.MinLon, mercY(g.MinLat), g.MaxLon, mercY(g.MaxLat)}
	inside := false
	center := orb.Point{(r.minX + r.maxX) / 2, (r.minY + r.maxY) / 2}
	for _, ring := range p {
		for i := 0; i+1 < len(ring); i++ {
			a, b := mercPoint(ring[i]), mercPoint(ring[i+1])
			if segmentTouchesRect(a, b, r) {
				return true
			}
			if (a[1] > center[1]) != (b[1] > center[1]) &&
				center[0] < a[0]+(center[1]-a[1])*(b[0]-a[0])/(b[1]-a[1]) {
				inside = !inside
			}
		}
	}
	return inside
}

func polygonsOf(g orb.Geometry) []orb.Polygon {
	if mp, ok := g.(orb.MultiPolygon); ok {
		return mp
	}
	return []orb.Polygon{g.(orb.Polygon)}
}

// compareWithOracle fails the test when the selected tiles differ from the tiles that the oracle finds in box.
func compareWithOracle(t *testing.T, got map[[2]int]bool, polygons []orb.Polygon, box *TileBounds) {
	t.Helper()
	for x := box.MinX; x <= box.MaxX; x++ {
		for y := box.MinY; y <= box.MaxY; y++ {
			want := false
			for _, p := range polygons {
				want = want || oracleIntersects(p, tileGeo(x, y, box.Zoom))
			}
			if got[[2]int{x, y}] != want {
				t.Errorf("tile %d/%d/%d: selected = %v, intersects = %v", box.Zoom, x, y, got[[2]int{x, y}], want)
			}
		}
	}
	for tile := range got {
		if !box.ContainsTile(tile[0], tile[1]) {
			t.Errorf("tile %v is outside the bounds of the polygon", tile)
		}
	}
}

func TestAreaPolygonSelectsTheTilesThatIntersectThePolygon(t *testing.T) {
	triangle := orb.Polygon{{{-105.0, 39.0}, {-104.0, 39.0}, {-104.5, 40.0}, {-105.0, 39.0}}}
	square := orb.Ring{{10.0, 45.0}, {11.0, 45.0}, {11.0, 46.0}, {10.0, 46.0}, {10.0, 45.0}}
	hole := orb.Ring{{10.3, 45.3}, {10.3, 45.7}, {10.7, 45.7}, {10.7, 45.3}, {10.3, 45.3}}
	tests := []struct {
		name string
		geom orb.Geometry
		zoom int
	}{
		{"a triangle at zoom level 10", triangle, 10},
		{"a square with a hole at zoom level 12", orb.Polygon{square, hole}, 12},
		{"a multipolygon of two triangles at zoom level 9", orb.MultiPolygon{
			triangle,
			{{{-100.0, 30.0}, {-99.0, 30.5}, {-100.0, 31.0}, {-100.0, 30.0}}},
		}, 9},
	}
	calc := NewTileCalculator()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := Area{Kind: AreaPolygon, Geometry: tt.geom}
			if err := a.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			got := collectTiles(t, a, tt.zoom)
			b := tt.geom.Bound()
			box := calc.CalculateTileBounds(&GeographicBounds{MinLat: b.Min[1], MaxLat: b.Max[1], MinLon: b.Min[0], MaxLon: b.Max[0]}, tt.zoom)
			if len(got) >= box.TileCount() {
				t.Errorf("polygon selects %d tiles, want fewer than the %d tiles of its bounds", len(got), box.TileCount())
			}
			compareWithOracle(t, got, polygonsOf(tt.geom), box)
		})
	}
}

// groundDistance returns the distance in meters from the tile to the line, on a flat plane scaled at lat0.
func groundDistance(line orb.LineString, g *GeographicBounds, lat0 float64) float64 {
	kx := EarthRadius * math.Cos(lat0*math.Pi/180) * math.Pi / 180
	ky := EarthRadius * math.Pi / 180
	r := rect{g.MinLon * kx, g.MinLat * ky, g.MaxLon * kx, g.MaxLat * ky}
	best := math.Inf(1)
	for i := 0; i+1 < len(line); i++ {
		a := orb.Point{line[i][0] * kx, line[i][1] * ky}
		b := orb.Point{line[i+1][0] * kx, line[i+1][1] * ky}
		best = math.Min(best, segmentRectDistance(a, b, r))
	}
	return best
}

func pointSegmentDistance(p, a, b orb.Point) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l))
	}
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}

func segmentRectDistance(a, b orb.Point, r rect) float64 {
	if segmentTouchesRect(a, b, r) {
		return 0
	}
	corners := []orb.Point{{r.minX, r.minY}, {r.maxX, r.minY}, {r.maxX, r.maxY}, {r.minX, r.maxY}}
	best := math.Inf(1)
	for i, c := range corners {
		best = math.Min(best, pointSegmentDistance(c, a, b))
		d := corners[(i+1)%4]
		best = math.Min(best, math.Min(pointSegmentDistance(a, c, d), pointSegmentDistance(b, c, d)))
	}
	return best
}

func TestAreaRouteBufferSelectsTheTilesWithinTheBuffer(t *testing.T) {
	// One degree of latitude is about 111,320 m, so 1 km north of the line is 0.00898 degrees.
	line := orb.LineString{{10.0, 45.0}, {10.05, 45.0}, {10.08, 45.02}}
	a := Area{Kind: AreaRouteBuffer, Geometry: line, BufferM: 300}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	const z = 17
	got := collectTiles(t, a, z)
	calc := NewTileCalculator()

	tests := []struct {
		name     string
		lat, lon float64
		want     bool
	}{
		{"selects the tile at the line", 45.0, 10.02, true},
		{"selects the tile 200 m north of the line", 45.0018, 10.02, true},
		{"leaves out the tile 1 km north of the line", 45.00898, 10.02, false},
		{"leaves out the tile 1 km south of the line", 44.99102, 10.02, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x, y := calc.LatLonToTile(tt.lat, tt.lon, z)
			if got[[2]int{x, y}] != tt.want {
				t.Errorf("tile %d/%d/%d: selected = %v, want %v", z, x, y, !tt.want, tt.want)
			}
		})
	}

	t.Run("keeps the buffer within 5 percent", func(t *testing.T) {
		b := line.Bound()
		box := calc.CalculateTileBounds(&GeographicBounds{MinLat: b.Min[1] - 0.02, MaxLat: b.Max[1] + 0.02, MinLon: b.Min[0] - 0.03, MaxLon: b.Max[0] + 0.03}, z)
		for x := box.MinX; x <= box.MaxX; x++ {
			for y := box.MinY; y <= box.MaxY; y++ {
				d := groundDistance(line, tileGeo(x, y, z), 45.0)
				sel := got[[2]int{x, y}]
				if sel && d > 300*1.05 {
					t.Errorf("tile %d/%d/%d is %.0f m from the line, but Tiles selects it", z, x, y, d)
				}
				if !sel && d < 300*0.95 {
					t.Errorf("tile %d/%d/%d is %.0f m from the line, but Tiles leaves it out", z, x, y, d)
				}
			}
		}
	})
}

func TestAreaBoundsSelectsTheSameTilesAsTheRectangle(t *testing.T) {
	calc := NewTileCalculator()
	geom := orb.Polygon{{{-105.2, 39.5}, {-104.6, 39.5}, {-104.6, 40.1}, {-105.2, 40.1}, {-105.2, 39.5}}}
	a := Area{Kind: AreaBounds, Geometry: geom}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	for _, z := range []int{0, 5, 10, 14} {
		want, err := calc.CalculateTileBoundsForBbox(39.5, -105.2, 40.1, -104.6, z)
		if err != nil {
			t.Fatal(err)
		}
		got := collectTiles(t, a, z)
		if len(got) != want.TileCount() {
			t.Errorf("zoom level %d: Tiles sends %d tiles, want %d", z, len(got), want.TileCount())
		}
		for tile := range got {
			if !want.ContainsTile(tile[0], tile[1]) {
				t.Errorf("zoom level %d: tile %v is outside the rectangle", z, tile)
			}
		}
	}
}

// lineOf returns a line of n vertices that runs east from 10, 45.
func lineOf(n int) orb.LineString {
	line := make(orb.LineString, n)
	for i := range line {
		line[i] = orb.Point{10 + float64(i)*1e-5, 45}
	}
	return line
}

func TestAreaValidateRejectsAnInvalidArea(t *testing.T) {
	square := orb.Polygon{{{10, 45}, {11, 45}, {11, 46}, {10, 46}, {10, 45}}}
	line := orb.LineString{{10, 45}, {10.1, 45}}
	tests := []struct {
		name    string
		area    Area
		wantErr string
	}{
		{"accepts a polygon", Area{Kind: AreaPolygon, Geometry: square}, ""},
		{"accepts a multipolygon", Area{Kind: AreaPolygon, Geometry: orb.MultiPolygon{square}}, ""},
		{"accepts a buffer of 10 m", Area{Kind: AreaRouteBuffer, Geometry: line, BufferM: 10}, ""},
		{"accepts a buffer of 50,000 m", Area{Kind: AreaRouteBuffer, Geometry: orb.MultiLineString{line}, BufferM: 50000}, ""},
		{"rejects a polygon that crosses 180 degrees",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{179, 10}, {-179, 10}, {-179, 11}, {179, 11}, {179, 10}}}},
			"areas that cross 180° are not supported"},
		{"rejects a polygon with a longitude above 180",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{179, 10}, {181, 10}, {181, 11}, {179, 11}, {179, 10}}}},
			"areas that cross 180° are not supported"},
		{"rejects a line that crosses 180 degrees",
			Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{179.5, 10}, {-179.5, 10}}, BufferM: 100},
			"areas that cross 180° are not supported"},
		{"rejects a self-intersecting polygon",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 45}, {11, 46}, {11, 45}, {10, 46}, {10, 45}}}},
			"invalid polygon: self-intersection"},
		{"rejects a hole that crosses the outer ring",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{square[0], {{10.5, 45.5}, {11.5, 45.5}, {11.5, 45.6}, {10.5, 45.6}, {10.5, 45.5}}}},
			"invalid polygon: self-intersection"},
		{"rejects a buffer below 10 m", Area{Kind: AreaRouteBuffer, Geometry: line, BufferM: 9}, "buffer_m"},
		{"rejects a buffer above 50,000 m", Area{Kind: AreaRouteBuffer, Geometry: line, BufferM: 50001}, "buffer_m"},
		{"rejects a line for a polygon area", Area{Kind: AreaPolygon, Geometry: line}, "polygon"},
		{"rejects a polygon for a route buffer area", Area{Kind: AreaRouteBuffer, Geometry: square, BufferM: 100}, "route_buffer"},
		{"rejects an unknown kind", Area{Kind: "lasso", Geometry: square}, "lasso"},
		{"rejects a ring with two points", Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 45}, {11, 45}, {10, 45}}}}, "invalid polygon"},
		{"rejects a latitude above 90", Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{10, 45}, {10, 91}}, BufferM: 100}, "latitude"},
		{"accepts a polygon that crosses the north Web Mercator limit",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 84}, {11, 84}, {11, 86}, {10, 86}, {10, 84}}}}, ""},
		{"accepts a polygon that starts at the north Web Mercator limit",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 85.0511}, {11, 85.0511}, {11, 86}, {10, 86}, {10, 85.0511}}}}, ""},
		{"rejects a polygon just above the north Web Mercator limit",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 85.0512}, {11, 85.0512}, {11, 86}, {10, 86}, {10, 85.0512}}}}, "Web Mercator"},
		{"rejects a polygon just below the south Web Mercator limit",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{10, -86}, {11, -86}, {11, -85.0512}, {10, -85.0512}, {10, -86}}}}, "Web Mercator"},
		{"rejects a bounds area from 86 to 89", Area{Kind: AreaBounds, Geometry: orb.Bound{Min: orb.Point{0, 86}, Max: orb.Point{1, 89}}}, "Web Mercator"},
		{"accepts a line above the limit whose buffer reaches into Web Mercator",
			Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{10, 85.1}, {11, 85.1}}, BufferM: 50000}, ""},
		{"rejects a line above the limit whose buffer stays outside Web Mercator",
			Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{10, 85.1}, {11, 85.1}}, BufferM: 10}, "Web Mercator"},
		{"accepts a line with 20,000 vertices", Area{Kind: AreaRouteBuffer, Geometry: lineOf(MaxAreaVertices), BufferM: 100}, ""},
		{"rejects a line with 20,001 vertices", Area{Kind: AreaRouteBuffer, Geometry: lineOf(MaxAreaVertices + 1), BufferM: 100},
			"the area has 20001 vertices, and an area holds at most 20000"},
		{"rejects a polygon with 20,001 vertices before the self-intersection check",
			Area{Kind: AreaPolygon, Geometry: orb.Polygon{orb.Ring(lineOf(MaxAreaVertices + 1))}},
			"the area has 20001 vertices, and an area holds at most 20000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.area.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want an error that holds %q", err, tt.wantErr)
			}
		})
	}
}

func TestAreaTilesClosesTheChannelWhenTheContextEnds(t *testing.T) {
	areas := map[string]Area{
		"polygon":      {Kind: AreaPolygon, Geometry: orb.Polygon{{{10, 45}, {11, 45}, {11, 46}, {10, 45}}}},
		"route_buffer": {Kind: AreaRouteBuffer, Geometry: orb.LineString{{10, 45}, {11, 45}}, BufferM: 1000},
		"bounds":       {Kind: AreaBounds, Geometry: orb.Polygon{{{10, 45}, {11, 45}, {11, 46}, {10, 46}, {10, 45}}}},
	}
	for name, a := range areas {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			tiles := a.Tiles(ctx, 14)
			<-tiles
			cancel()
			timeout := time.After(time.Second)
			for received := 0; ; received++ {
				select {
				case _, open := <-tiles:
					if !open {
						return
					}
					// A select picks a ready case at random, so the producer can send one more tile after the cancel.
					if received > 0 {
						t.Fatalf("Tiles sent %d tiles after the cancel, want at most 1", received+1)
					}
				case <-timeout:
					t.Fatal("Tiles did not close its channel within 1 s of the cancel")
				}
			}
		})
	}
}
