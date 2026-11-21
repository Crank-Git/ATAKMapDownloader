package areafile

import (
	"encoding/json"
	"fmt"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/paulmach/orb"
)

// geoJSONObject holds the members of a FeatureCollection, a Feature and a geometry that Parse reads (RFC 7946, section 3).
type geoJSONObject struct {
	Type       string            `json:"type"`
	Features   []json.RawMessage `json:"features"`
	Geometry   json.RawMessage   `json:"geometry"`
	Properties json.RawMessage   `json:"properties"`
}

// featureProps holds the Feature properties that set the values of an area (FR-area-selection-13).
// A nil field is a property that the Feature does not have.
type featureProps struct {
	Name    string   `json:"name"`
	MinZoom *int     `json:"min_zoom"`
	MaxZoom *int     `json:"max_zoom"`
	BufferM *float64 `json:"buffer_m"`
}

func parseGeoJSON(data []byte, d Defaults) (Result, error) {
	var top geoJSONObject
	if err := json.Unmarshal(data, &top); err != nil {
		return Result{}, fmt.Errorf("read the GeoJSON: %w", err)
	}
	var res Result
	switch top.Type {
	case "FeatureCollection":
		for i, raw := range top.Features {
			var f geoJSONObject
			if err := json.Unmarshal(raw, &f); err != nil {
				return Result{}, fmt.Errorf("read the GeoJSON: feature %d: %w", i+1, err)
			}
			if err := res.addFeature(f, d); err != nil {
				return Result{}, fmt.Errorf("feature %d: %w", i+1, err)
			}
		}
	case "Feature":
		if err := res.addFeature(top, d); err != nil {
			return Result{}, fmt.Errorf("feature 1: %w", err)
		}
	default:
		if err := res.addGeometry(data, "", d); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// addFeature adds the area of the Feature f, with its properties in place of the defaults.
func (r *Result) addFeature(f geoJSONObject, d Defaults) error {
	// RFC 7946 allows a null geometry for a Feature with no location, and such a Feature has no area.
	if len(f.Geometry) == 0 || string(f.Geometry) == "null" {
		return nil
	}
	var p featureProps
	if len(f.Properties) > 0 {
		if err := json.Unmarshal(f.Properties, &p); err != nil {
			return fmt.Errorf("read the properties: %w", err)
		}
	}
	if p.MinZoom != nil {
		d.MinZoom = *p.MinZoom
	}
	if p.MaxZoom != nil {
		d.MaxZoom = *p.MaxZoom
	}
	if p.BufferM != nil {
		d.BufferM = *p.BufferM
	}
	return r.addGeometry(f.Geometry, p.Name, d)
}

// addGeometry adds the area of one GeoJSON geometry object, or counts a Point or MultiPoint.
func (r *Result) addGeometry(data json.RawMessage, name string, d Defaults) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("read the geometry: %w", err)
	}
	if head.Type == "Point" || head.Type == "MultiPoint" {
		r.Points++
		return nil
	}
	g, err := tilecalc.DecodeGeometry(data)
	if err != nil {
		return err
	}
	r.add(g, name, d)
	return nil
}

// add appends the area of the polygon or line g.
func (r *Result) add(g orb.Geometry, name string, d Defaults) {
	a := tilecalc.JobArea{Name: name, MinZoom: d.MinZoom, MaxZoom: d.MaxZoom}
	switch g.(type) {
	case orb.Polygon, orb.MultiPolygon:
		a.Area = tilecalc.Area{Kind: tilecalc.AreaPolygon, Geometry: g}
	default:
		a.Area = tilecalc.Area{Kind: tilecalc.AreaRouteBuffer, Geometry: g, BufferM: d.BufferM}
	}
	r.Areas = append(r.Areas, a)
}
