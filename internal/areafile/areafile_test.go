package areafile

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/paulmach/orb"
)

// fixture returns the path of a file in testdata/areas at the repository root.
func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", "areas", name)
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// zipOf returns a zip archive that holds the files in order.
func zipOf(t *testing.T, files ...[2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var defaults = Defaults{MinZoom: 10, MaxZoom: 14, BufferM: 500}

func polygonArea(name string, g orb.Geometry, minZoom, maxZoom int) tilecalc.JobArea {
	return tilecalc.JobArea{Area: tilecalc.Area{Kind: tilecalc.AreaPolygon, Geometry: g}, Name: name, MinZoom: minZoom, MaxZoom: maxZoom}
}

func lineArea(name string, g orb.Geometry, bufferM float64, minZoom, maxZoom int) tilecalc.JobArea {
	return tilecalc.JobArea{Area: tilecalc.Area{Kind: tilecalc.AreaRouteBuffer, Geometry: g, BufferM: bufferM}, Name: name, MinZoom: minZoom, MaxZoom: maxZoom}
}

var (
	summitTrail = orb.LineString{{-121.70, 46.80}, {-121.70, 46.82}}
	lakeRing    = orb.Ring{{-122.0, 47.0}, {-121.9, 47.0}, {-121.9, 47.1}, {-122.0, 47.0}}
	campOuter   = orb.Ring{{-121.0, 46.0}, {-120.9, 46.0}, {-120.9, 46.1}, {-121.0, 46.1}, {-121.0, 46.0}}
	campInner   = orb.Ring{{-120.97, 46.03}, {-120.93, 46.03}, {-120.93, 46.07}, {-120.97, 46.03}}
)

func TestParseReadsTheAreasOfAFile(t *testing.T) {
	tests := []struct {
		name   string
		format Format
		data   []byte
		want   []tilecalc.JobArea
		points int
	}{
		{
			name:   "reads a KML LineString as a route_buffer area with the Placemark name and the default buffer",
			format: KML,
			data:   readFixture(t, "trail.kml"),
			want:   []tilecalc.JobArea{lineArea("Summit trail", summitTrail, 500, 10, 14)},
			points: 1,
		},
		{
			name:   "reads each part of a nested KML MultiGeometry",
			format: KML,
			data:   readFixture(t, "multi.kml"),
			want: []tilecalc.JobArea{
				polygonArea("Camp", orb.Polygon{campOuter, campInner}, 10, 14),
				lineArea("Camp", orb.LineString{{-121.0, 46.2}, {-120.9, 46.2}}, 500, 10, 14),
				polygonArea("Lake", orb.Polygon{lakeRing}, 10, 14),
			},
			points: 1,
		},
		{
			name:   "applies the properties of each GeoJSON Feature and skips the Point features",
			format: GeoJSON,
			data:   readFixture(t, "mixed.geojson"),
			want: []tilecalc.JobArea{
				polygonArea("County", orb.MultiPolygon{{{{-121.0, 46.0}, {-120.9, 46.0}, {-120.9, 46.1}, {-121.0, 46.0}}}}, 8, 12),
				lineArea("Ridge", orb.LineString{{-121.0, 46.2}, {-120.9, 46.2}}, 200, 10, 14),
				lineArea("", orb.MultiLineString{{{-121.0, 46.3}, {-120.9, 46.3}}}, 500, 10, 14),
			},
			points: 2,
		},
		{
			name:   "uses the max_zoom property of a single GeoJSON Feature",
			format: GeoJSON,
			data:   readFixture(t, "peak.geojson"),
			want: []tilecalc.JobArea{polygonArea("Peak",
				orb.Polygon{{{-121.760, 46.852}, {-121.759, 46.852}, {-121.759, 46.853}, {-121.760, 46.853}, {-121.760, 46.852}}}, 10, 16)},
		},
		{
			name:   "reads a bare GeoJSON geometry",
			format: GeoJSON,
			data:   []byte(`{"type":"LineString","coordinates":[[-121.70,46.80],[-121.70,46.82]]}`),
			want:   []tilecalc.JobArea{lineArea("", summitTrail, 500, 10, 14)},
		},
		{
			name:   "reads doc.kml of a KMZ that holds more than one KML",
			format: KMZ,
			data: zipOf(t,
				[2]string{"files/lake.kml", string(readFixture(t, "multi.kml"))},
				[2]string{"doc.kml", string(readFixture(t, "trail.kml"))}),
			want:   []tilecalc.JobArea{lineArea("Summit trail", summitTrail, 500, 10, 14)},
			points: 1,
		},
		{
			name:   "reads the first KML of a KMZ with no doc.kml",
			format: KMZ,
			data: zipOf(t,
				[2]string{"images/readme.txt", "not a KML"},
				[2]string{"a/trail.KML", string(readFixture(t, "trail.kml"))},
				[2]string{"b/multi.kml", string(readFixture(t, "multi.kml"))}),
			want:   []tilecalc.JobArea{lineArea("Summit trail", summitTrail, 500, 10, 14)},
			points: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.format, tt.data, defaults)
			if err != nil {
				t.Fatalf("Parse() = %v", err)
			}
			if !reflect.DeepEqual(got.Areas, tt.want) {
				t.Errorf("areas = %+v\nwant %+v", got.Areas, tt.want)
			}
			if got.Points != tt.points {
				t.Errorf("points = %d, want %d", got.Points, tt.points)
			}
		})
	}
}

func TestParseRejectsAFileThatHoldsNoArea(t *testing.T) {
	tests := []struct {
		name    string
		format  Format
		data    string
		wantErr string
	}{
		{"rejects GeoJSON that is not JSON", GeoJSON, `{"type":`, "read the GeoJSON"},
		{"rejects a GeoJSON type that is not an object of RFC 7946", GeoJSON, `{"type":"Topology"}`, `"Topology"`},
		{"rejects a GeometryCollection", GeoJSON, `{"type":"GeometryCollection","geometries":[]}`, `"GeometryCollection"`},
		{"rejects a zoom property that is not a number", GeoJSON,
			`{"type":"Feature","properties":{"max_zoom":"16"},"geometry":{"type":"LineString","coordinates":[[0,0],[1,1]]}}`, "feature 1"},
		{"rejects a file with Point features only", GeoJSON,
			`{"type":"FeatureCollection","features":[{"type":"Feature","properties":null,"geometry":{"type":"Point","coordinates":[0,0]}}]}`,
			"the file has no polygon and no line"},
		{"rejects KML that is not XML and names the line", KML, "<kml>\n<Placemark>\n</kml>", "line 3"},
		{"rejects a KML position with one number", KML,
			`<kml><Placemark><LineString><coordinates>1,2 3</coordinates></LineString></Placemark></kml>`, `position "3"`},
		{"rejects a KML position that is not a number", KML,
			`<kml><Placemark><LineString><coordinates>1,2 a,b</coordinates></LineString></Placemark></kml>`, `position "a,b"`},
		{"rejects a KML file with no Placemark geometry", KML, `<kml><Document/></kml>`, "the file has no polygon and no line"},
		{"rejects a KMZ that is not a zip archive", KMZ, "not a zip", "read the KMZ"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.format, []byte(tt.data), defaults)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() = %v, want an error that holds %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseRejectsAKMZWithNoKML(t *testing.T) {
	_, err := Parse(KMZ, zipOf(t, [2]string{"readme.txt", "no KML"}), defaults)
	if err == nil || !strings.Contains(err.Error(), "holds no .kml file") {
		t.Errorf("Parse() = %v, want an error that holds %q", err, "holds no .kml file")
	}
}

func TestParseRejectsAFileLargerThan5MiB(t *testing.T) {
	big := make([]byte, MaxBytes+1)
	if _, err := Parse(GeoJSON, big, defaults); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Parse() of %d bytes = %v, want ErrTooLarge", len(big), err)
	}
	// A KMZ of 5 MiB or less can hold a larger KML, so the limit applies to the KML too.
	kml := "<kml>" + strings.Repeat(" ", MaxBytes) + "</kml>"
	if _, err := Parse(KMZ, zipOf(t, [2]string{"doc.kml", kml}), defaults); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Parse() of a KMZ with a %d byte KML = %v, want ErrTooLarge", len(kml), err)
	}
}

func TestReadRejectsAFileLargerThan5MiB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.geojson")
	if err := os.WriteFile(path, make([]byte, MaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path, defaults); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Read() = %v, want ErrTooLarge", err)
	}
}

func TestReadDetectsTheFormatFromTheExtension(t *testing.T) {
	got, err := Read(fixture("trail.kml"), defaults)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if len(got.Areas) != 1 || got.Areas[0].Name != "Summit trail" {
		t.Errorf("areas = %+v, want the Summit trail area", got.Areas)
	}
}

func TestFormatOf(t *testing.T) {
	tests := []struct {
		name string
		want Format
	}{
		{"area.geojson", GeoJSON},
		{"area.json", GeoJSON},
		{"AREA.GeoJSON", GeoJSON},
		{"trail.kml", KML},
		{"trail.kmz", KMZ},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FormatOf(tt.name)
			if err != nil || got != tt.want {
				t.Errorf("FormatOf(%q) = %v, %v, want %v", tt.name, got, err, tt.want)
			}
		})
	}
	if _, err := FormatOf("trail.gpx"); err == nil || !strings.Contains(err.Error(), ".gpx") {
		t.Errorf("FormatOf(trail.gpx) = %v, want an error that names .gpx", err)
	}
}

func TestSniff(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want Format
	}{
		{"reads a JSON object as GeoJSON", []byte(" \n{\"type\":\"Feature\"}"), GeoJSON},
		{"reads XML as KML", []byte("\xef\xbb\xbf<?xml version=\"1.0\"?><kml/>"), KML},
		{"reads a zip archive as KMZ", []byte("PK\x03\x04rest"), KMZ},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Sniff(tt.data)
			if err != nil || got != tt.want {
				t.Errorf("Sniff() = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
	if _, err := Sniff([]byte("lon,lat")); err == nil {
		t.Error("Sniff(lon,lat) = nil error, want an error")
	}
}

func TestWarningsCountThePointFeatures(t *testing.T) {
	tests := []struct {
		points int
		want   []string
	}{
		{0, []string{}},
		{1, []string{"ignored 1 Point feature, because a point is not an area"}},
		{2, []string{"ignored 2 Point features, because a point is not an area"}},
	}
	for _, tt := range tests {
		if got := (Result{Points: tt.points}).Warnings(); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Warnings() with %d points = %q, want %q", tt.points, got, tt.want)
		}
	}
}
