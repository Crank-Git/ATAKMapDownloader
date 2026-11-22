package tilecalc

import "math"

// MercatorBounds returns the edges of the XYZ tile x, y at zoom level z in EPSG:3857 meters.
// A WMS GetMap request for the tile sends these edges as its BBOX (FR-source-sync-19).
func MercatorBounds(x, y, z int) (minX, minY, maxX, maxY float64) {
	half := math.Pi * EarthRadius
	size := 2 * half / float64(int(1)<<z)
	minX = -half + float64(x)*size
	maxY = half - float64(y)*size
	return minX, maxY - size, minX + size, maxY
}
