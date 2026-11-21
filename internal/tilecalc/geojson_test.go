package tilecalc

import (
	"reflect"
	"strings"
	"testing"

	"github.com/paulmach/orb"
)

func TestDecodeGeometry(t *testing.T) {
	tests := []struct {
		name string
		json string
		want orb.Geometry
	}{
		{"reads a Polygon", `{"type":"Polygon","coordinates":[[[0,0],[1,0],[0,1],[0,0]]]}`,
			orb.Polygon{{{0, 0}, {1, 0}, {0, 1}, {0, 0}}}},
		{"reads a MultiPolygon", `{"type":"MultiPolygon","coordinates":[[[[0,0],[1,0],[0,1],[0,0]]]]}`,
			orb.MultiPolygon{{{{0, 0}, {1, 0}, {0, 1}, {0, 0}}}}},
		{"reads a LineString", `{"type":"LineString","coordinates":[[0,0],[1,1]]}`,
			orb.LineString{{0, 0}, {1, 1}}},
		{"reads a MultiLineString", `{"type":"MultiLineString","coordinates":[[[0,0],[1,1]],[[2,2],[3,3]]]}`,
			orb.MultiLineString{{{0, 0}, {1, 1}}, {{2, 2}, {3, 3}}}},
		{"drops the altitude of a position", `{"type":"LineString","coordinates":[[0,0,100],[1,1,200]]}`,
			orb.LineString{{0, 0}, {1, 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeGeometry([]byte(tt.json))
			if err != nil {
				t.Fatalf("DecodeGeometry = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DecodeGeometry = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDecodeGeometryRejectsAnInvalidGeometry(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr string
	}{
		{"rejects a Point", `{"type":"Point","coordinates":[0,0]}`, `geometry type "Point"`},
		{"rejects a position with one number", `{"type":"LineString","coordinates":[[0],[1,1]]}`, "position"},
		{"rejects coordinates of the wrong depth", `{"type":"Polygon","coordinates":[[0,0],[1,1]]}`, "coordinates"},
		{"rejects text that is not JSON", `{`, "geometry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeGeometry([]byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("DecodeGeometry error = %v, want an error that holds %q", err, tt.wantErr)
			}
		})
	}
}

func TestEncodeGeometryWritesABoundAsAPolygon(t *testing.T) {
	got, err := EncodeGeometry(orb.Bound{Min: orb.Point{0, 0}, Max: orb.Point{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,2],[0,2],[0,0]]]}`; string(got) != want {
		t.Errorf("EncodeGeometry = %s, want %s", got, want)
	}
}

func TestEncodeAreasAndDecodeAreasKeepTheAreas(t *testing.T) {
	areas := []JobArea{
		{Area: Area{Kind: AreaPolygon, Geometry: orb.Polygon{{{0, 0}, {1, 0}, {0, 1}, {0, 0}}}}, Name: "county", MinZoom: 8, MaxZoom: 12},
		{Area: Area{Kind: AreaRouteBuffer, Geometry: orb.LineString{{0, 0}, {1, 1}}, BufferM: 300}, Name: "trail", MinZoom: 13, MaxZoom: 17},
		{Area: Area{Kind: AreaBounds, Geometry: orb.Polygon{{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}}}, MinZoom: 0, MaxZoom: 3},
	}
	data, err := EncodeAreas(areas)
	if err != nil {
		t.Fatalf("EncodeAreas = %v", err)
	}
	if !strings.HasPrefix(string(data), `{"type":"FeatureCollection","features":[{"type":"Feature",`) {
		t.Errorf("EncodeAreas = %s, want a GeoJSON FeatureCollection", data)
	}
	got, err := DecodeAreas(data)
	if err != nil {
		t.Fatalf("DecodeAreas = %v", err)
	}
	if !reflect.DeepEqual(got, areas) {
		t.Errorf("DecodeAreas = %#v, want %#v", got, areas)
	}
}

func TestDecodeAreasRejectsAFeatureWithAnInvalidGeometry(t *testing.T) {
	data := `{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},` +
		`"properties":{"kind":"polygon","min_zoom":1,"max_zoom":2}}]}`
	if _, err := DecodeAreas([]byte(data)); err == nil {
		t.Error("DecodeAreas of a Point feature = nil, want an error")
	}
}
