package tilecalc

import (
	"encoding/json"
	"fmt"

	"github.com/paulmach/orb"
)

// This file decodes GeoJSON (RFC 7946) with encoding/json, because orb/geojson adds the mongo-driver bson package (ADR 0012).

// geometryObject is a GeoJSON geometry object (RFC 7946, section 3.1).
type geometryObject struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// DecodeGeometry returns the orb geometry of the GeoJSON geometry object in data.
// It reads the types Polygon, MultiPolygon, LineString and MultiLineString, the types that an area can hold.
// It keeps the longitude and the latitude of each position, and drops an altitude.
// It returns an error for another type, for coordinates of the wrong depth, and for a position with fewer than 2 numbers.
func DecodeGeometry(data []byte) (orb.Geometry, error) {
	var g geometryObject
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, fmt.Errorf("read the geometry: %w", err)
	}
	switch g.Type {
	case "LineString":
		var c [][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, coordinatesError(g.Type, err)
		}
		return linePoints(c)
	case "MultiLineString", "Polygon":
		var c [][][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, coordinatesError(g.Type, err)
		}
		lines, err := multiLine(c)
		if err != nil {
			return nil, err
		}
		if g.Type == "MultiLineString" {
			return lines, nil
		}
		return polygonOf(lines), nil
	case "MultiPolygon":
		var c [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &c); err != nil {
			return nil, coordinatesError(g.Type, err)
		}
		mp := make(orb.MultiPolygon, 0, len(c))
		for _, p := range c {
			lines, err := multiLine(p)
			if err != nil {
				return nil, err
			}
			mp = append(mp, polygonOf(lines))
		}
		return mp, nil
	default:
		return nil, fmt.Errorf("geometry type %q is not an area: use Polygon, MultiPolygon, LineString or MultiLineString", g.Type)
	}
}

func coordinatesError(geometryType string, err error) error {
	return fmt.Errorf("read the coordinates of the %s: %w", geometryType, err)
}

func linePoints(c [][]float64) (orb.LineString, error) {
	line := make(orb.LineString, 0, len(c))
	for _, p := range c {
		if len(p) < 2 {
			return nil, fmt.Errorf("a position has %d numbers, and it needs at least 2", len(p))
		}
		line = append(line, orb.Point{p[0], p[1]})
	}
	return line, nil
}

func multiLine(c [][][]float64) (orb.MultiLineString, error) {
	lines := make(orb.MultiLineString, 0, len(c))
	for _, l := range c {
		line, err := linePoints(l)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func polygonOf(lines orb.MultiLineString) orb.Polygon {
	p := make(orb.Polygon, 0, len(lines))
	for _, l := range lines {
		p = append(p, orb.Ring(l))
	}
	return p
}

// EncodeGeometry returns g as a GeoJSON geometry object.
// It writes an orb.Bound as a Polygon, because GeoJSON has no rectangle type.
func EncodeGeometry(g orb.Geometry) (json.RawMessage, error) {
	if b, ok := g.(orb.Bound); ok {
		g = b.ToPolygon()
	}
	// The orb types are nested arrays of [2]float64, so encoding/json writes them as GeoJSON coordinates.
	coordinates, err := json.Marshal(g)
	if err != nil {
		return nil, fmt.Errorf("write the geometry: %w", err)
	}
	return json.Marshal(geometryObject{Type: g.GeoJSONType(), Coordinates: coordinates})
}

// featureCollection is the GeoJSON form of the areas of a job, as the atakdl_areas metadata row holds it.
type featureCollection struct {
	Type     string        `json:"type"`
	Features []areaFeature `json:"features"`
}

// areaFeature is one area as a GeoJSON Feature.
// The properties use the field names of the job API (FR-area-selection-25).
type areaFeature struct {
	Type       string          `json:"type"`
	Geometry   json.RawMessage `json:"geometry"`
	Properties areaProperties  `json:"properties"`
}

type areaProperties struct {
	Kind    AreaKind `json:"kind"`
	Name    string   `json:"name,omitempty"`
	MinZoom int      `json:"min_zoom"`
	MaxZoom int      `json:"max_zoom"`
	BufferM float64  `json:"buffer_m,omitempty"`
}

// EncodeAreas returns the areas as a GeoJSON FeatureCollection, with one Feature per area in list order (FR-area-selection-29).
func EncodeAreas(areas []JobArea) ([]byte, error) {
	fc := featureCollection{Type: "FeatureCollection", Features: make([]areaFeature, 0, len(areas))}
	for _, a := range areas {
		geometry, err := EncodeGeometry(a.Geometry)
		if err != nil {
			return nil, err
		}
		fc.Features = append(fc.Features, areaFeature{
			Type:     "Feature",
			Geometry: geometry,
			Properties: areaProperties{
				Kind: a.Kind, Name: a.Name, MinZoom: a.MinZoom, MaxZoom: a.MaxZoom, BufferM: a.BufferM,
			},
		})
	}
	data, err := json.Marshal(fc)
	if err != nil {
		return nil, fmt.Errorf("write the areas: %w", err)
	}
	return data, nil
}

// DecodeAreas returns the areas of a GeoJSON FeatureCollection that EncodeAreas wrote.
// It returns an error when the text is not a FeatureCollection or when a geometry does not decode.
// It does not validate the areas.
func DecodeAreas(data []byte) ([]JobArea, error) {
	var fc featureCollection
	if err := json.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("read the areas: %w", err)
	}
	if fc.Type != "FeatureCollection" {
		return nil, fmt.Errorf("the areas are a GeoJSON %q, not a FeatureCollection", fc.Type)
	}
	areas := make([]JobArea, 0, len(fc.Features))
	for i, f := range fc.Features {
		g, err := DecodeGeometry(f.Geometry)
		if err != nil {
			return nil, fmt.Errorf("area %d: %w", i+1, err)
		}
		p := f.Properties
		areas = append(areas, JobArea{Area: Area{Kind: p.Kind, Geometry: g, BufferM: p.BufferM}, Name: p.Name, MinZoom: p.MinZoom, MaxZoom: p.MaxZoom})
	}
	return areas, nil
}
