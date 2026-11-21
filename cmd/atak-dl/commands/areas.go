package commands

import (
	"encoding/json"
	"fmt"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/paulmach/orb"
)

// areaRequest is one area of POST /api/jobs and POST /api/estimate (FR-area-selection-25).
// Geometry is a GeoJSON geometry object. A bounds area takes a Polygon, and its bounding rectangle is the bounds.
type areaRequest struct {
	Kind     tilecalc.AreaKind `json:"kind"`
	Geometry json.RawMessage   `json:"geometry"`
	MinZoom  int               `json:"min_zoom"`
	MaxZoom  int               `json:"max_zoom"`
	BufferM  float64           `json:"buffer_m"`
	Name     string            `json:"name"`
}

// requestAreas returns the areas of a request that holds either an area list or the legacy bounds and zoom range.
// The legacy bounds are [west, south, east, north], and they give one bounds area (FR-area-selection-26).
// It returns an error code and a message for these cases:
//   - "invalid_area" when the request holds both areas and bounds, or when a geometry does not decode.
//   - "invalid_bounds" when the request holds no areas and the bounds are not four valid numbers.
//
// requestAreas does not check the areas. checkAreas does.
func requestAreas(bounds []float64, minZoom, maxZoom int, areas []areaRequest) (out []tilecalc.JobArea, code, message string) {
	if areas == nil {
		if len(bounds) != 4 {
			return nil, "invalid_bounds", "Bounds must hold four numbers: west, south, east, north"
		}
		if _, err := tilecalc.NewGeographicBounds(bounds[1], bounds[3], bounds[0], bounds[2]); err != nil {
			return nil, "invalid_bounds", err.Error()
		}
		b := orb.Bound{Min: orb.Point{bounds[0], bounds[1]}, Max: orb.Point{bounds[2], bounds[3]}}
		return []tilecalc.JobArea{{Area: tilecalc.Area{Kind: tilecalc.AreaBounds, Geometry: b}, MinZoom: minZoom, MaxZoom: maxZoom}}, "", ""
	}
	if bounds != nil {
		return nil, "invalid_area", "Send areas or bounds, not both"
	}
	out = make([]tilecalc.JobArea, 0, len(areas))
	for i, a := range areas {
		g, err := tilecalc.DecodeGeometry(a.Geometry)
		if err != nil {
			return nil, "invalid_area", fmt.Sprintf("area %d: %v", i+1, err)
		}
		out = append(out, tilecalc.JobArea{
			Area: tilecalc.Area{Kind: a.Kind, Geometry: g, BufferM: a.BufferM}, Name: a.Name, MinZoom: a.MinZoom, MaxZoom: a.MaxZoom,
		})
	}
	return out, "", ""
}

// checkAreas returns an error code and a message when the area list of a job is not valid.
// It returns these codes:
//   - "invalid_area" when the list holds fewer than 1 or more than 20 areas (FR-area-selection-5),
//     when the areas hold more than tilecalc.MaxJobVertices vertices (FR-area-selection-31), or when an area does not validate.
//   - "zoom_out_of_range" when the zoom range of an area is empty or outside 0 to tilecalc.MaxZoomLevel.
//
// It returns an empty code when the list is valid.
func checkAreas(areas []tilecalc.JobArea) (code, message string) {
	if len(areas) < 1 || len(areas) > tilecalc.MaxJobAreas {
		return "invalid_area", fmt.Sprintf("a job holds 1 to %d areas, and the request has %d", tilecalc.MaxJobAreas, len(areas))
	}
	// The count comes before Validate, so a large request costs no self-intersection check.
	vertices := 0
	for _, a := range areas {
		vertices += a.Vertices()
	}
	if vertices > tilecalc.MaxJobVertices {
		return "invalid_area", fmt.Sprintf("the areas have %d vertices, and a job holds at most %d", vertices, tilecalc.MaxJobVertices)
	}
	for i, a := range areas {
		// A legacy request has one area, so its messages keep the form that they had before the area list.
		prefix := ""
		if len(areas) > 1 {
			prefix = fmt.Sprintf("area %d: ", i+1)
		}
		if a.MinZoom > a.MaxZoom {
			return "zoom_out_of_range", fmt.Sprintf("%smin_zoom %d is above max_zoom %d", prefix, a.MinZoom, a.MaxZoom)
		}
		if a.MinZoom < 0 || a.MaxZoom > tilecalc.MaxZoomLevel {
			return "zoom_out_of_range", fmt.Sprintf("%szoom range %d-%d is outside the range 0-%d", prefix, a.MinZoom, a.MaxZoom, tilecalc.MaxZoomLevel)
		}
		if err := a.Validate(); err != nil {
			return "invalid_area", prefix + err.Error()
		}
	}
	return "", ""
}

// jobOutputFile returns the name of the output file of a job of the areas from sourceName.
// The name holds the rectangle and the zoom range over all areas, so a legacy job keeps the name of v0.1.
func jobOutputFile(sourceName string, areas []tilecalc.JobArea, customName, format string) string {
	b := tilecalc.JobBound(areas)
	minZoom, maxZoom := tilecalc.JobZoomRange(areas)
	bounds := &tilecalc.GeographicBounds{MinLat: b.Min[1], MaxLat: b.Max[1], MinLon: b.Min[0], MaxLon: b.Max[0]}
	return download.GenerateOutputFilename(sourceName, bounds, minZoom, maxZoom, customName, format)
}
