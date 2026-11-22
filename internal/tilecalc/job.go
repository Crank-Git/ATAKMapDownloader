package tilecalc

import (
	"context"
	"math"

	"github.com/paulmach/orb"
)

// MaxJobAreas is the highest number of areas in one job (FR-area-selection-5).
const MaxJobAreas = 20

// MaxJobVertices is the highest number of vertices over all areas of one job (FR-area-selection-31).
const MaxJobVertices = 50_000

// MaxZoomLevel is the highest zoom level of an area. It is the limit of ValidateZoomRange.
const MaxZoomLevel = 25

// MaxLatitude is the highest latitude in degrees that Web Mercator shows. LatLonToTile clamps to it.
const MaxLatitude = 85.0511

// JobArea is one area of a job, with its own zoom range (FR-area-selection-4).
// Name is the label of the area in the estimate, and it can be empty.
type JobArea struct {
	Area
	Name    string
	MinZoom int
	MaxZoom int
}

// Tile is one XYZ tile of a job.
type Tile struct{ Z, X, Y int }

// JobTiles returns a channel that carries each tile of the areas once (FR-area-selection-8).
// It sends the areas in list order, each area from its lowest zoom level up, and each zoom level row by row.
// A tile that an earlier area selects is not sent again for a later area.
// The producer goroutine closes the channel after the last tile, or when ctx ends.
// JobTiles does not validate the areas, so call Validate on each area first.
func JobTiles(ctx context.Context, areas []JobArea) <-chan Tile {
	ch := make(chan Tile)
	go func() {
		defer close(ch)
		walkJob(ctx, areas, func(_, z, y0, y1 int, _, unique []span) bool {
			for y := y0; y <= y1; y++ {
				for _, s := range unique {
					for x := s.lo; x <= s.hi; x++ {
						select {
						case ch <- Tile{Z: z, X: x, Y: y}:
						case <-ctx.Done():
							return false
						}
					}
				}
			}
			return true
		})
	}()
	return ch
}

// JobCount holds the tile counts of a job (FR-area-selection-9, -10).
type JobCount struct {
	// Areas holds the count of each area alone, in the order of the areas.
	Areas []int
	// Zooms maps each zoom level to its count of unique tiles.
	Zooms map[int]int
	// Unique is the count of unique tiles over all areas.
	Unique int
}

// CountJobTiles returns the tile counts of the areas.
// It counts runs of columns, not single tiles, so a large area costs time per tile row only.
// A bounds area that shares no zoom level with an earlier area costs one step per zoom level.
// CountJobTiles does not validate the areas, so call Validate on each area first.
func CountJobTiles(areas []JobArea) JobCount {
	c := JobCount{Areas: make([]int, len(areas)), Zooms: map[int]int{}}
	walkJob(context.Background(), areas, func(i, z, y0, y1 int, own, unique []span) bool {
		rows := y1 - y0 + 1
		c.Areas[i] += rows * spanWidth(own)
		n := rows * spanWidth(unique)
		c.Zooms[z] += n
		c.Unique += n
		return true
	})
	return c
}

// walkJob calls fn for the tile rows y0 to y1 of each zoom level of each area, in the order of JobTiles.
// own holds the columns of area i in each of the rows, and unique holds the columns that no earlier area selects.
// fn gets one row per call, except for a bounds area with no earlier area at the zoom level, which gets all its rows in one call.
// The walk stops when fn returns false or when ctx ends.
func walkJob(ctx context.Context, areas []JobArea, fn func(i, z, y0, y1 int, own, unique []span) bool) {
	for i, a := range areas {
		for z := a.MinZoom; z <= a.MaxZoom; z++ {
			if !walkZoom(ctx, areas, i, z, fn) {
				return
			}
		}
	}
}

// walkZoom calls fn for the tile rows of area i at zoom level z, as walkJob does.
// It returns false when the walk stops.
func walkZoom(ctx context.Context, areas []JobArea, i, z int, fn func(i, z, y0, y1 int, own, unique []span) bool) bool {
	n := 1 << z
	c := areas[i].cover(z)
	earlier := earlierCovers(areas[:i], z)
	y0, y1 := max(c.minY, 0), min(c.maxY, n-1)
	if c.sameRows && len(earlier) == 0 && y0 <= y1 {
		own := mergeSpans(c.spans(y0), n)
		return fn(i, z, y0, y1, own, own)
	}
	for y := y0; y <= y1; y++ {
		if ctx.Err() != nil {
			return false
		}
		own := mergeSpans(c.spans(y), n)
		if !fn(i, z, y, y, own, subtractSpans(own, rowSpans(earlier, y, n))) {
			return false
		}
	}
	return true
}

// earlierCovers returns the covers at zoom level z of the areas whose zoom range holds z.
func earlierCovers(areas []JobArea, z int) []cover {
	var out []cover
	for _, a := range areas {
		if z >= a.MinZoom && z <= a.MaxZoom {
			out = append(out, a.cover(z))
		}
	}
	return out
}

// rowSpans returns the columns of row y that any of the covers holds, merged.
func rowSpans(covers []cover, y, n int) []span {
	var out []span
	for _, c := range covers {
		if y >= c.minY && y <= c.maxY {
			out = append(out, c.spans(y)...)
		}
	}
	return mergeSpans(out, n)
}

// subtractSpans returns the columns of a that are not in b. Both lists are sorted and hold no overlaps.
func subtractSpans(a, b []span) []span {
	if len(b) == 0 {
		return a
	}
	var out []span
	j := 0
	for _, s := range a {
		lo := s.lo
		for j < len(b) && b[j].hi < lo {
			j++
		}
		for k := j; k < len(b) && b[k].lo <= s.hi; k++ {
			if b[k].lo > lo {
				out = append(out, span{lo, b[k].lo - 1})
			}
			lo = max(lo, b[k].hi+1)
		}
		if lo <= s.hi {
			out = append(out, span{lo, s.hi})
		}
	}
	return out
}

func spanWidth(spans []span) int {
	n := 0
	for _, s := range spans {
		n += s.hi - s.lo + 1
	}
	return n
}

// metersPerDegree is the length of one degree of latitude on the WGS 84 sphere of EarthRadius.
const metersPerDegree = 2 * math.Pi * EarthRadius / 360

// JobBound returns the bounding rectangle of all areas (FR-area-selection-27).
// The rectangle of a route_buffer area includes its buffer, with the longitude scale of its latitude nearest a pole.
func JobBound(areas []JobArea) orb.Bound {
	var out orb.Bound
	for i, a := range areas {
		b := a.Geometry.Bound()
		if a.Kind == AreaRouteBuffer {
			dLat := a.BufferM / metersPerDegree
			lat := min(85.0511, max(math.Abs(b.Min[1]), math.Abs(b.Max[1]))+dLat)
			dLon := dLat / math.Cos(lat*math.Pi/180)
			b = orb.Bound{
				Min: orb.Point{max(-180, b.Min[0]-dLon), max(-90, b.Min[1]-dLat)},
				Max: orb.Point{min(180, b.Max[0]+dLon), min(90, b.Max[1]+dLat)},
			}
		}
		if i == 0 {
			out = b
		} else {
			out = out.Union(b)
		}
	}
	return out
}

// JobZoomRange returns the lowest and the highest zoom level over all areas (FR-area-selection-28).
func JobZoomRange(areas []JobArea) (minZoom, maxZoom int) {
	minZoom, maxZoom = areas[0].MinZoom, areas[0].MaxZoom
	for _, a := range areas[1:] {
		minZoom, maxZoom = min(minZoom, a.MinZoom), max(maxZoom, a.MaxZoom)
	}
	return minZoom, maxZoom
}
