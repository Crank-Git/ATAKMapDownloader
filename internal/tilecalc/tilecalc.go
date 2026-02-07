// Package tilecalc provides tile coordinate calculations and transformations.
package tilecalc

import (
	"fmt"
	"math"
)

// EarthRadius is the WGS84 earth radius in meters.
const EarthRadius = 6378137.0

// TileBounds represents tile boundaries for a zoom level.
type TileBounds struct {
	MinX, MaxX int
	MinY, MaxY int
	Zoom       int
}

// TileCount returns the total number of tiles.
func (t *TileBounds) TileCount() int {
	return (t.MaxX - t.MinX + 1) * (t.MaxY - t.MinY + 1)
}

// Width returns the width in tiles.
func (t *TileBounds) Width() int {
	return t.MaxX - t.MinX + 1
}

// Height returns the height in tiles.
func (t *TileBounds) Height() int {
	return t.MaxY - t.MinY + 1
}

// ContainsTile checks if a tile coordinate is within bounds.
func (t *TileBounds) ContainsTile(x, y int) bool {
	return x >= t.MinX && x <= t.MaxX && y >= t.MinY && y <= t.MaxY
}

// IterTiles yields all (x, y) tile coordinates.
func (t *TileBounds) IterTiles() <-chan [2]int {
	ch := make(chan [2]int)
	go func() {
		defer close(ch)
		for y := t.MinY; y <= t.MaxY; y++ {
			for x := t.MinX; x <= t.MaxX; x++ {
				ch <- [2]int{x, y}
			}
		}
	}()
	return ch
}

// GeographicBounds represents WGS84 geographic boundaries.
type GeographicBounds struct {
	MinLat, MaxLat float64
	MinLon, MaxLon float64
}

// NewGeographicBounds creates and validates geographic bounds.
func NewGeographicBounds(minLat, maxLat, minLon, maxLon float64) (*GeographicBounds, error) {
	if minLat >= maxLat {
		return nil, fmt.Errorf("min_lat (%f) must be less than max_lat (%f)", minLat, maxLat)
	}
	if minLon >= maxLon {
		return nil, fmt.Errorf("min_lon (%f) must be less than max_lon (%f)", minLon, maxLon)
	}
	if minLat < -90 || maxLat > 90 {
		return nil, fmt.Errorf("latitude must be between -90 and 90 degrees")
	}
	if minLon < -180 || maxLon > 180 {
		return nil, fmt.Errorf("longitude must be between -180 and 180 degrees")
	}
	return &GeographicBounds{MinLat: minLat, MaxLat: maxLat, MinLon: minLon, MaxLon: maxLon}, nil
}

// Center returns (lat, lon) of the center point.
func (g *GeographicBounds) Center() (float64, float64) {
	return (g.MinLat + g.MaxLat) / 2, (g.MinLon + g.MaxLon) / 2
}

// AreaDegrees returns area in square degrees.
func (g *GeographicBounds) AreaDegrees() float64 {
	return (g.MaxLat - g.MinLat) * (g.MaxLon - g.MinLon)
}

// TileCalculator handles tile coordinate calculations.
type TileCalculator struct {
	EarthRadius       float64
	EarthCircumference float64
}

// NewTileCalculator creates a new TileCalculator.
func NewTileCalculator() *TileCalculator {
	return &TileCalculator{
		EarthRadius:       EarthRadius,
		EarthCircumference: 2 * math.Pi * EarthRadius,
	}
}

// DegToRad converts degrees to radians.
func (t *TileCalculator) DegToRad(degrees float64) float64 {
	return degrees * math.Pi / 180
}

// RadToDeg converts radians to degrees.
func (t *TileCalculator) RadToDeg(radians float64) float64 {
	return radians * 180 / math.Pi
}

// LatLonToTile converts lat/lon to tile coordinates.
func (t *TileCalculator) LatLonToTile(lat, lon float64, zoom int) (int, int) {
	lat = max(-85.0511, min(85.0511, lat))
	latRad := t.DegToRad(lat)
	n := math.Pow(2, float64(zoom))
	tileX := int((lon + 180) / 360 * n)
	tileY := int((1 - math.Asinh(math.Tan(latRad))/math.Pi) / 2 * n)
	maxTile := int(n) - 1
	tileX = max(0, min(maxTile, tileX))
	tileY = max(0, min(maxTile, tileY))
	return tileX, tileY
}

// TileToLatLon converts tile coordinates to lat/lon.
func (t *TileCalculator) TileToLatLon(tileX, tileY int, zoom int) (float64, float64) {
	n := math.Pow(2, float64(zoom))
	lon := float64(tileX)/n*360 - 180
	latRad := math.Atan(math.Sinh(math.Pi * (1 - 2*float64(tileY)/n)))
	lat := t.RadToDeg(latRad)
	return lat, lon
}

// TileBoundsToGeographic converts tile bounds to geographic bounds.
func (t *TileCalculator) TileBoundsToGeographic(b *TileBounds) *GeographicBounds {
	minLat, minLon := t.TileToLatLon(b.MinX, b.MaxY+1, b.Zoom)
	maxLat, maxLon := t.TileToLatLon(b.MaxX+1, b.MinY, b.Zoom)
	gb, _ := NewGeographicBounds(minLat, maxLat, minLon, maxLon)
	return gb
}

// CalculateTileBounds returns tile bounds for a geographic area.
func (t *TileCalculator) CalculateTileBounds(bounds *GeographicBounds, zoom int) *TileBounds {
	minTileX, maxTileY := t.LatLonToTile(bounds.MinLat, bounds.MinLon, zoom)
	maxTileX, minTileY := t.LatLonToTile(bounds.MaxLat, bounds.MaxLon, zoom)
	return &TileBounds{MinX: minTileX, MaxX: maxTileX, MinY: minTileY, MaxY: maxTileY, Zoom: zoom}
}

// CalculateTileBoundsForBbox calculates tile bounds for a bbox (minLat, minLon, maxLat, maxLon).
func (t *TileCalculator) CalculateTileBoundsForBbox(minLat, minLon, maxLat, maxLon float64, zoom int) (*TileBounds, error) {
	bounds, err := NewGeographicBounds(minLat, maxLat, minLon, maxLon)
	if err != nil {
		return nil, err
	}
	return t.CalculateTileBounds(bounds, zoom), nil
}

// CalculateZoomForArea returns optimal zoom for max tile count.
func (t *TileCalculator) CalculateZoomForArea(bounds *GeographicBounds, maxTiles int) int {
	for zoom := 1; zoom < 19; zoom++ {
		tb := t.CalculateTileBounds(bounds, zoom)
		if tb.TileCount() > maxTiles {
			return max(1, zoom-1)
		}
	}
	return 18
}

// EstimateDownloadSize returns size estimates for tiles.
func (t *TileCalculator) EstimateDownloadSize(b *TileBounds, avgTileSizeKB float64) map[string]interface{} {
	count := b.TileCount()
	sizeKB := float64(count) * avgTileSizeKB
	return map[string]interface{}{
		"tile_count": count,
		"size_kb":    sizeKB,
		"size_mb":    sizeKB / 1024,
		"size_gb":    sizeKB / 1024 / 1024,
		"size_human": formatSize(sizeKB * 1024),
	}
}

func formatSize(sizeBytes float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	for _, u := range units {
		if sizeBytes < 1024 {
			return fmt.Sprintf("%.1f %s", sizeBytes, u)
		}
		sizeBytes /= 1024
	}
	return fmt.Sprintf("%.1f PB", sizeBytes)
}

// FormatSize formats bytes to human-readable string.
func FormatSize(sizeBytes float64) string {
	return formatSize(sizeBytes)
}

// GetTileURLBounds returns tile bounds for multiple zoom levels.
func (t *TileCalculator) GetTileURLBounds(bounds *GeographicBounds, zoomLevels []int) []*TileBounds {
	result := make([]*TileBounds, len(zoomLevels))
	for i, z := range zoomLevels {
		result[i] = t.CalculateTileBounds(bounds, z)
	}
	return result
}

// ValidateZoomRange validates min/max zoom.
func (t *TileCalculator) ValidateZoomRange(minZoom, maxZoom int) []string {
	var errors []string
	if minZoom < 0 {
		errors = append(errors, fmt.Sprintf("minimum zoom cannot be negative: %d", minZoom))
	}
	if maxZoom > 25 {
		errors = append(errors, fmt.Sprintf("maximum zoom too high: %d", maxZoom))
	}
	if minZoom > maxZoom {
		errors = append(errors, fmt.Sprintf("min zoom (%d) cannot be greater than max zoom (%d)", minZoom, maxZoom))
	}
	return errors
}

// CalculateResolutionAtLatitude returns meters per pixel.
func (t *TileCalculator) CalculateResolutionAtLatitude(lat float64, zoom int) float64 {
	latRad := t.DegToRad(lat)
	return (t.EarthCircumference * math.Cos(latRad)) / (256 * math.Pow(2, float64(zoom)))
}

// TilesIntersectGeometry checks if tile bounds intersect geographic bounds.
func (t *TileCalculator) TilesIntersectGeometry(tileBounds *TileBounds, geoBounds *GeographicBounds) bool {
	tileGeo := t.TileBoundsToGeographic(tileBounds)
	return !(tileGeo.MaxLat < geoBounds.MinLat ||
		tileGeo.MinLat > geoBounds.MaxLat ||
		tileGeo.MaxLon < geoBounds.MinLon ||
		tileGeo.MinLon > geoBounds.MaxLon)
}
