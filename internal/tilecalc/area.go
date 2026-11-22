package tilecalc

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/paulmach/orb"
)

// AreaKind names the shape of an area (FR-area-selection-1).
type AreaKind string

// The area kinds of FR-area-selection-1. The values are the `kind` strings of the job API.
const (
	AreaBounds      AreaKind = "bounds"
	AreaPolygon     AreaKind = "polygon"
	AreaRouteBuffer AreaKind = "route_buffer"
)

// The range of buffer_m in meters (FR-area-selection-3).
const (
	MinBufferM = 10
	MaxBufferM = 50000
)

// MaxAreaVertices is the highest number of vertices in one area (FR-area-selection-30).
// The self-intersection check compares each pair of edges, so the cap bounds its time.
const MaxAreaVertices = 20_000

// The edge-case errors of feature 11. Callers match them with errors.Is.
var (
	ErrAntimeridian     = errors.New("areas that cross 180° are not supported")
	ErrSelfIntersection = errors.New("invalid polygon: self-intersection")
)

// Area is the region of a job: bounds, a polygon or a route buffer.
// Geometry holds WGS 84 longitude and latitude pairs:
//   - bounds: an orb.Bound, or an orb.Polygon whose bounding rectangle is the bounds.
//   - polygon: an orb.Polygon or an orb.MultiPolygon.
//   - route_buffer: an orb.LineString or an orb.MultiLineString.
//
// BufferM is the distance in meters on each side of the line. Only a route_buffer area uses it.
type Area struct {
	Kind     AreaKind
	Geometry orb.Geometry
	BufferM  float64
}

// Validate returns nil when the area is one that Tiles can select tiles for.
// It returns ErrAntimeridian when a position or an edge crosses 180°, and
// ErrSelfIntersection when the edges of one polygon cross or touch.
// It also returns an error for an unknown kind, a geometry of the wrong type,
// a latitude outside -90 to 90, an area wholly outside the Web Mercator latitudes,
// too few positions, a BufferM outside 10 to 50,000, or more than MaxAreaVertices vertices.
func (a Area) Validate() error {
	if n := a.Vertices(); n > MaxAreaVertices {
		return fmt.Errorf("the area has %d vertices, and an area holds at most %d", n, MaxAreaVertices)
	}
	switch a.Kind {
	case AreaBounds:
		return validateBounds(a.Geometry)
	case AreaPolygon:
		polygons, ok := areaPolygons(a.Geometry)
		if !ok {
			return fmt.Errorf("a polygon area needs a Polygon or MultiPolygon geometry, not %T", a.Geometry)
		}
		if err := validatePolygons(polygons); err != nil {
			return err
		}
		b := a.Geometry.Bound()
		return checkWebMercator(b.Min[1], b.Max[1])
	case AreaRouteBuffer:
		lines, ok := areaLines(a.Geometry)
		if !ok {
			return fmt.Errorf("a route_buffer area needs a LineString or MultiLineString geometry, not %T", a.Geometry)
		}
		if !(a.BufferM >= MinBufferM && a.BufferM <= MaxBufferM) {
			return fmt.Errorf("buffer_m %g is outside the range %d to %d", a.BufferM, MinBufferM, MaxBufferM)
		}
		for _, line := range lines {
			if len(line) < 2 {
				return errors.New("a line needs at least 2 positions")
			}
			if err := validatePath(line); err != nil {
				return err
			}
		}
		// The buffer can reach into the Web Mercator latitudes from a line outside them, so the check uses the buffered rectangle.
		b := JobBound([]JobArea{{Area: a}})
		return checkWebMercator(b.Min[1], b.Max[1])
	default:
		return fmt.Errorf("unknown area kind %q: use bounds, polygon or route_buffer", a.Kind)
	}
}

// Vertices returns the number of positions in the geometry of the area.
// A ring counts its closing position. A geometry of a type that no area kind takes counts 0.
func (a Area) Vertices() int {
	switch g := a.Geometry.(type) {
	case orb.Bound:
		return 4
	case orb.LineString:
		return len(g)
	case orb.MultiLineString:
		n := 0
		for _, line := range g {
			n += len(line)
		}
		return n
	case orb.Polygon:
		return polygonVertices(g)
	case orb.MultiPolygon:
		n := 0
		for _, p := range g {
			n += polygonVertices(p)
		}
		return n
	default:
		return 0
	}
}

// polygonVertices returns the number of positions in the rings of p.
func polygonVertices(p orb.Polygon) int {
	n := 0
	for _, ring := range p {
		n += len(ring)
	}
	return n
}

// Tiles returns a channel that carries every (x, y) tile of the area at zoom level z, row by row.
//   - bounds: every tile of the bounding rectangle, as IterTiles sends them.
//   - polygon: every tile whose square intersects the polygon in Web Mercator (FR-area-selection-6).
//   - route_buffer: every tile whose square is within BufferM of the line (FR-area-selection-7).
//
// The producer goroutine closes the channel after the last tile, or when ctx ends.
// Tiles does not run Validate, so call Validate first. An area of an unknown kind sends no tiles.
func (a Area) Tiles(ctx context.Context, z int) <-chan [2]int {
	return emitCover(ctx, a.cover(z), 1<<z)
}

// cover returns the tile columns of the area for each tile row at zoom level z.
// An area of an unknown kind has no rows.
func (a Area) cover(z int) cover {
	n := 1 << z
	switch a.Kind {
	case AreaBounds:
		b := a.Geometry.Bound()
		geo := &GeographicBounds{MinLat: b.Min[1], MaxLat: b.Max[1], MinLon: b.Min[0], MaxLon: b.Max[0]}
		tb := NewTileCalculator().CalculateTileBounds(geo, z)
		return cover{minY: tb.MinY, maxY: tb.MaxY, spans: func(int) []span { return []span{{tb.MinX, tb.MaxX}} }, sameRows: true}
	case AreaPolygon:
		polygons, _ := areaPolygons(a.Geometry) // Validate checked the type, and a wrong type gives no polygons.
		return newPolygonCover(polygons, n)
	case AreaRouteBuffer:
		lines, _ := areaLines(a.Geometry) // Validate checked the type, and a wrong type gives no lines.
		return newBufferCover(lines, a.BufferM, n)
	default:
		return cover{minY: 0, maxY: -1, spans: func(int) []span { return nil }}
	}
}

func areaPolygons(g orb.Geometry) ([]orb.Polygon, bool) {
	switch g := g.(type) {
	case orb.Polygon:
		return []orb.Polygon{g}, true
	case orb.MultiPolygon:
		return g, true
	}
	return nil, false
}

func areaLines(g orb.Geometry) ([]orb.LineString, bool) {
	switch g := g.(type) {
	case orb.LineString:
		return []orb.LineString{g}, true
	case orb.MultiLineString:
		return g, true
	}
	return nil, false
}

func validateBounds(g orb.Geometry) error {
	switch g := g.(type) {
	case orb.Bound:
		if err := validatePath([]orb.Point{g.Min, g.Max}); err != nil {
			return err
		}
	case orb.Polygon:
		for _, ring := range g {
			if err := validatePath(ring); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("a bounds area needs a Polygon or Bound geometry, not %T", g)
	}
	b := g.Bound()
	_, err := NewGeographicBounds(b.Min[1], b.Max[1], b.Min[0], b.Max[0])
	return err
}

// validatePath checks every position and every edge of a ring or a line.
// GeoJSON writers show a crossing of 180° as a longitude beyond ±180 or as an edge longer than 180° (RFC 7946, section 3.1.9).
func validatePath(path []orb.Point) error {
	var prevLon float64
	for i, p := range path {
		if math.IsNaN(p[0]) || math.IsNaN(p[1]) {
			return errors.New("a position is not a number")
		}
		if p[0] < -180 || p[0] > 180 || (i > 0 && math.Abs(p[0]-prevLon) > 180) {
			return ErrAntimeridian
		}
		prevLon = p[0]
		if p[1] < -90 || p[1] > 90 {
			return fmt.Errorf("latitude %g is outside -90 to 90", p[1])
		}
	}
	return nil
}

func validatePolygons(polygons []orb.Polygon) error {
	for _, p := range polygons {
		var edges []edge
		for r, ring := range p {
			if err := validatePath(ring); err != nil {
				return err
			}
			pts := openRing(ring)
			if len(pts) < 3 {
				return errors.New("invalid polygon: a ring needs at least 3 different positions")
			}
			for i := range pts {
				edges = append(edges, edge{pts[i], pts[(i+1)%len(pts)], r, i, len(pts)})
			}
		}
		if crossingEdges(edges) {
			return ErrSelfIntersection
		}
	}
	return nil
}

// edge is one edge of a polygon ring: position pos of a ring with size edges.
type edge struct {
	a, b            orb.Point
	ring, pos, size int
}

// openRing returns the ring without repeated positions and without the closing position.
// A repeated position makes a zero-length edge, and that edge touches two edges that are not its neighbors.
func openRing(ring orb.Ring) []orb.Point {
	var pts []orb.Point
	for _, p := range ring {
		if len(pts) == 0 || p != pts[len(pts)-1] {
			pts = append(pts, p)
		}
	}
	if len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	return pts
}

// crossingEdges reports whether two edges that are not neighbors in one ring meet.
// The check compares every pair, so a polygon of n positions costs n² tests.
func crossingEdges(edges []edge) bool {
	for i, e := range edges {
		for _, f := range edges[i+1:] {
			neighbors := e.ring == f.ring && (f.pos == e.pos+1 || (e.pos == 0 && f.pos == f.size-1))
			if !neighbors && segmentsMeet(e.a, e.b, f.a, f.b) {
				return true
			}
		}
	}
	return false
}

func orient(a, b, c orb.Point) float64 {
	return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0])
}

// onSegment reports whether p, which is collinear with a and b, lies between them.
func onSegment(a, b, p orb.Point) bool {
	return min(a[0], b[0]) <= p[0] && p[0] <= max(a[0], b[0]) && min(a[1], b[1]) <= p[1] && p[1] <= max(a[1], b[1])
}

func segmentsMeet(a, b, c, d orb.Point) bool {
	d1, d2 := orient(c, d, a), orient(c, d, b)
	d3, d4 := orient(a, b, c), orient(a, b, d)
	if d1*d2 < 0 && d3*d4 < 0 {
		return true
	}
	return (d1 == 0 && onSegment(c, d, a)) || (d2 == 0 && onSegment(c, d, b)) ||
		(d3 == 0 && onSegment(a, b, c)) || (d4 == 0 && onSegment(a, b, d))
}

// worldPoint returns p in tile units at a grid of n by n tiles, with y down as in XYZ.
// In these units the square of tile (x, y) is [x, x+1] by [y, y+1].
func worldPoint(p orb.Point, n int) orb.Point {
	lat := max(-85.0511, min(85.0511, p[1])) * math.Pi / 180
	return orb.Point{
		(p[0] + 180) / 360 * float64(n),
		(1 - math.Asinh(math.Tan(lat))/math.Pi) / 2 * float64(n),
	}
}

// span is a run of tile columns in one row, with both ends included.
type span struct{ lo, hi int }

// cover gives the tile columns of an area for each tile row from minY to maxY.
type cover struct {
	minY, maxY int
	spans      func(y int) []span
	// sameRows is true when every row has the same columns, as in a bounds area.
	sameRows bool
}

func emitCover(ctx context.Context, c cover, n int) <-chan [2]int {
	ch := make(chan [2]int)
	go func() {
		defer close(ch)
		for y := max(c.minY, 0); y <= min(c.maxY, n-1); y++ {
			// A row with no tiles sends nothing, so the row loop checks ctx itself.
			if ctx.Err() != nil {
				return
			}
			for _, s := range mergeSpans(c.spans(y), n) {
				for x := s.lo; x <= s.hi; x++ {
					select {
					case ch <- [2]int{x, y}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch
}

// mergeSpans clips the spans to the grid of n columns and joins the spans that overlap or touch.
func mergeSpans(spans []span, n int) []span {
	var out []span
	for _, s := range spans {
		s.lo, s.hi = max(s.lo, 0), min(s.hi, n-1)
		if s.lo <= s.hi {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b span) int { return a.lo - b.lo })
	merged := out[:0]
	for _, s := range out {
		if last := len(merged) - 1; last >= 0 && s.lo <= merged[last].hi+1 {
			merged[last].hi = max(merged[last].hi, s.hi)
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// clipX returns the x range of the part of segment ab with y from y0 to y1.
// It returns false when no part of the segment is in that band.
func clipX(a, b orb.Point, y0, y1 float64) (lo, hi float64, ok bool) {
	dy := b[1] - a[1]
	if dy == 0 {
		if a[1] < y0 || a[1] > y1 {
			return 0, 0, false
		}
		return min(a[0], b[0]), max(a[0], b[0]), true
	}
	t0, t1 := (y0-a[1])/dy, (y1-a[1])/dy
	tMin, tMax := max(0, min(t0, t1)), min(1, max(t0, t1))
	if tMin > tMax {
		return 0, 0, false
	}
	x0, x1 := a[0]+tMin*(b[0]-a[0]), a[0]+tMax*(b[0]-a[0])
	return min(x0, x1), max(x0, x1), true
}

func floorInt(v float64) int { return int(math.Floor(v)) }

func newPolygonCover(polygons []orb.Polygon, n int) cover {
	var projected [][][]orb.Point
	c := cover{minY: math.MaxInt, maxY: math.MinInt}
	for _, p := range polygons {
		var rings [][]orb.Point
		for _, ring := range p {
			pts := openRing(ring)
			for i, q := range pts {
				pts[i] = worldPoint(q, n)
				c.minY, c.maxY = min(c.minY, floorInt(pts[i][1])), max(c.maxY, floorInt(pts[i][1]))
			}
			rings = append(rings, pts)
		}
		projected = append(projected, rings)
	}
	c.spans = func(y int) []span {
		var out []span
		for _, rings := range projected {
			out = append(out, polygonRowSpans(rings, y)...)
		}
		return out
	}
	return c
}

// polygonRowSpans returns the columns of row y whose square meets the polygon.
// A square that meets no edge is either all inside or all outside, so the test at the row center decides it.
func polygonRowSpans(rings [][]orb.Point, y int) []span {
	y0, mid := float64(y), float64(y)+0.5
	var out []span
	var crossings []float64
	for _, pts := range rings {
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			if lo, hi, ok := clipX(a, b, y0, y0+1); ok {
				out = append(out, span{floorInt(lo), floorInt(hi)})
			}
			if (a[1] > mid) != (b[1] > mid) {
				crossings = append(crossings, a[0]+(mid-a[1])*(b[0]-a[0])/(b[1]-a[1]))
			}
		}
	}
	slices.Sort(crossings)
	for i := 0; i+1 < len(crossings); i += 2 {
		out = append(out, span{int(math.Ceil(crossings[i] - 0.5)), floorInt(crossings[i+1] - 0.5)})
	}
	return out
}

// piece is one part of a line in tile units, with the buffer in tile units at its latitude.
type piece struct {
	a, b orb.Point
	r    float64
}

func newBufferCover(lines []orb.LineString, bufferM float64, n int) cover {
	var pieces []piece
	c := cover{minY: math.MaxInt, maxY: math.MinInt}
	for _, line := range lines {
		for i := 0; i+1 < len(line); i++ {
			for _, p := range splitSegment(line[i], line[i+1], bufferM, n) {
				pieces = append(pieces, p)
				c.minY = min(c.minY, floorInt(min(p.a[1], p.b[1])-p.r))
				c.maxY = max(c.maxY, floorInt(max(p.a[1], p.b[1])+p.r))
			}
		}
	}
	c.spans = func(y int) []span {
		var out []span
		for _, p := range pieces {
			if s, ok := bufferRowSpan(p, y); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return c
}

// splitSegment cuts segment ab into pieces of at most 0.25° of latitude.
// The Web Mercator scale changes with latitude. A piece of 0.25° keeps the buffer within the 5% of feature 11 up to 85°.
// Each piece takes the scale of its end nearer a pole, so the buffer is never smaller than bufferM.
func splitSegment(a, b orb.Point, bufferM float64, n int) []piece {
	k := max(1, int(math.Ceil(4*math.Abs(b[1]-a[1]))))
	metersPerUnit := 2 * math.Pi * EarthRadius / float64(n)
	pieces := make([]piece, 0, k)
	for i := range k {
		p := orb.Point{a[0] + (b[0]-a[0])*float64(i)/float64(k), a[1] + (b[1]-a[1])*float64(i)/float64(k)}
		q := orb.Point{a[0] + (b[0]-a[0])*float64(i+1)/float64(k), a[1] + (b[1]-a[1])*float64(i+1)/float64(k)}
		lat := min(85.0511, max(math.Abs(p[1]), math.Abs(q[1]))) * math.Pi / 180
		pieces = append(pieces, piece{worldPoint(p, n), worldPoint(q, n), bufferM / (metersPerUnit * math.Cos(lat))})
	}
	return pieces
}

// bufferRowSpan returns the columns of row y whose square is within p.r of the piece.
// The buffer around a segment is convex, so the columns that meet it in one row are one run.
func bufferRowSpan(p piece, y int) (span, bool) {
	y0 := float64(y)
	if min(p.a[1], p.b[1])-p.r > y0+1 || max(p.a[1], p.b[1])+p.r < y0 {
		return span{}, false
	}
	near := func(x int) bool { return squareDist2(p.a, p.b, float64(x), y0) <= p.r*p.r }
	lo, hi := floorInt(min(p.a[0], p.b[0])-p.r), floorInt(max(p.a[0], p.b[0])+p.r)
	for lo <= hi && !near(lo) {
		lo++
	}
	for hi >= lo && !near(hi) {
		hi--
	}
	return span{lo, hi}, lo <= hi
}

// squareDist2 returns the squared distance from segment ab to the unit square at (x, y).
// When the two do not meet, the nearest points include an end of the segment or a corner of the square.
func squareDist2(a, b orb.Point, x, y float64) float64 {
	if lo, hi, ok := clipX(a, b, y, y+1); ok && lo <= x+1 && hi >= x {
		return 0
	}
	d := min(pointSquareDist2(a, x, y), pointSquareDist2(b, x, y))
	for _, c := range [4]orb.Point{{x, y}, {x + 1, y}, {x, y + 1}, {x + 1, y + 1}} {
		d = min(d, pointSegmentDist2(c, a, b))
	}
	return d
}

func pointSquareDist2(p orb.Point, x, y float64) float64 {
	dx := max(x-p[0], 0, p[0]-x-1)
	dy := max(y-p[1], 0, p[1]-y-1)
	return dx*dx + dy*dy
}

func pointSegmentDist2(p, a, b orb.Point) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = max(0, min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l))
	}
	ex, ey := p[0]-a[0]-t*dx, p[1]-a[1]-t*dy
	return ex*ex + ey*ey
}
