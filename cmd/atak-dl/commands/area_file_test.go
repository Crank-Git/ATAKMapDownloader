package commands

import (
	"bytes"
	"encoding/json"
	"math"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/areafile"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

// areaFixture returns the path of a file in testdata/areas at the repository root.
func areaFixture(name string) string {
	return filepath.Join("..", "..", "..", "testdata", "areas", name)
}

// tileEastOfTrail returns the tile at zoom level z that holds the point metres east of the middle of the Summit trail in trail.kml.
func tileEastOfTrail(z int, metres float64) testtiles.Tile {
	const lat, lon = 46.81, -121.70
	dLon := metres / (111320 * math.Cos(lat*math.Pi/180))
	x, y := tilecalc.NewTileCalculator().LatLonToTile(lat, lon+dLon, z)
	return testtiles.Tile{Z: z, X: x, Y: y}
}

func TestDownloadAreaFileWithABufferRequestsOnlyTilesNearTheLine(t *testing.T) {
	srv := testtiles.New(t, nil)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	stderr, err := runDownloadArgs(t, srv, "--area", areaFixture("trail.kml"), "--buffer", "300",
		"--min-zoom", "16", "--max-zoom", "16", "--output", output)
	if err != nil {
		t.Fatalf("download --area = %v, stderr %q", err, stderr)
	}
	if n := srv.Requests(tileEastOfTrail(16, 0)); n != 1 {
		t.Errorf("the tile on the line got %d requests, want 1", n)
	}
	// At zoom level 16 a tile is about 420 m wide, so the tile 1 km east starts more than 300 m from the line.
	if far := tileEastOfTrail(16, 1000); srv.Requests(far) != 0 {
		t.Errorf("tile %v, 1 km from the line, got %d requests, want 0", far, srv.Requests(far))
	}
	if !strings.Contains(stderr, "warning: ignored 1 Point feature") {
		t.Errorf("stderr = %q, want the Point warning", stderr)
	}
}

func TestDownloadAreaFileUsesTheMaxZoomPropertyOfAFeature(t *testing.T) {
	srv := testtiles.New(t, nil)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	if stderr, err := runDownloadArgs(t, srv, "--area", areaFixture("peak.geojson"),
		"--min-zoom", "14", "--max-zoom", "14", "--output", output); err != nil {
		t.Fatalf("download --area = %v, stderr %q", err, stderr)
	}
	zooms := map[int]int{}
	for _, r := range srv.Log() {
		zooms[r.Tile.Z]++
	}
	for z := 14; z <= 16; z++ {
		if zooms[z] == 0 {
			t.Errorf("the job requested no tile at zoom level %d, want tiles at 14 to 16 (requests per zoom level: %v)", z, zooms)
		}
	}
	if len(zooms) != 3 {
		t.Errorf("requests per zoom level = %v, want zoom levels 14 to 16 only", zooms)
	}
}

// runEstimateArgs runs the estimate command of the map source of srv with args after "estimate".
func runEstimateArgs(t *testing.T, srv *testtiles.Server, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", "", "")
	root.AddCommand(NewEstimateCmd())
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--mapsources-dir", writeSource(t, srv), "estimate", "--source", "Test TLS"}, args...))
	err = root.Execute()
	return out.String(), errOut.String(), err
}

func TestAreaFileErrorsExitWithCode1AndSendNoRequest(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big.geojson")
	if err := os.WriteFile(big, make([]byte, areafile.MaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	trail := areaFixture("trail.kml")
	tests := []struct {
		name    string
		command string
		args    []string
		wantErr string
	}{
		{"download rejects --area with --bbox", "download", []string{"--area", trail, "--bbox", oneTileBBox()}, "--area and --bbox"},
		{"estimate rejects --area with --bbox", "estimate", []string{"--area", trail, "--bbox", oneTileBBox()}, "--area and --bbox"},
		{"download rejects a file larger than 5 MiB", "download", []string{"--area", big}, "5 MiB"},
		{"download rejects an unknown extension", "download", []string{"--area", "trail.gpx"}, ".gpx"},
		{"download rejects a line with no buffer", "download", []string{"--area", trail}, "buffer_m 0"},
		{"estimate needs a zoom range for a feature with no zoom properties", "estimate", []string{"--area", trail, "--buffer", "300"}, "--zoom-range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			args := append(append([]string(nil), tt.args...), "--output", filepath.Join(t.TempDir(), "out.sqlite"))
			var err error
			if tt.command == "estimate" {
				_, _, err = runEstimateArgs(t, srv, tt.args...)
			} else {
				_, err = runDownloadArgs(t, srv, args...)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || ExitCode(err) != 1 {
				t.Errorf("%s error = %v (exit code %d), want exit code 1 and an error that holds %q", tt.command, err, ExitCode(err), tt.wantErr)
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("tile server got %d requests, want 0", n)
			}
		})
	}
}

func TestEstimateAreaFileCountsEachAreaAndTheUniqueTiles(t *testing.T) {
	srv := testtiles.New(t, nil)
	stdout, stderr, err := runEstimateArgs(t, srv, "--area", areaFixture("mixed.geojson"), "--zoom-range", "10-12", "--buffer", "300")
	if err != nil {
		t.Fatalf("estimate --area = %v, stderr %q", err, stderr)
	}
	areas, err := areafile.Read(areaFixture("mixed.geojson"), areafile.Defaults{MinZoom: 10, MaxZoom: 12, BufferM: 300})
	if err != nil {
		t.Fatal(err)
	}
	count := tilecalc.CountJobTiles(areas.Areas)
	for _, want := range []string{
		"County (polygon, zoom 8-12): " + formatInt(count.Areas[0]) + " tiles",
		"Ridge (route_buffer, zoom 10-12): " + formatInt(count.Areas[1]) + " tiles",
		"area 3 (route_buffer, zoom 10-12): " + formatInt(count.Areas[2]) + " tiles",
		"Unique tiles: " + formatInt(count.Unique),
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to hold %q", stdout, want)
		}
	}
	if !strings.Contains(stderr, "warning: ignored 2 Point features") {
		t.Errorf("stderr = %q, want the Point warning", stderr)
	}
}

// parseResponse is the POST /api/areas/parse response body.
type parseResponse struct {
	Areas []struct {
		Kind     string          `json:"kind"`
		Name     string          `json:"name"`
		MinZoom  int             `json:"min_zoom"`
		MaxZoom  int             `json:"max_zoom"`
		BufferM  float64         `json:"buffer_m"`
		Geometry json.RawMessage `json:"geometry"`
	} `json:"areas"`
	Warnings []string `json:"warnings"`
}

// multipartBody returns a multipart form with one file field named file, and its content type.
func multipartBody(t *testing.T, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func TestParseRouteReturnsTheAreasOfAFile(t *testing.T) {
	h := newServerHandler(newTestHandler(t, map[string]string{"test.xml": testSourceXML}))
	trail, err := os.ReadFile(areaFixture("trail.kml"))
	if err != nil {
		t.Fatal(err)
	}
	mixed, err := os.ReadFile(areaFixture("mixed.geojson"))
	if err != nil {
		t.Fatal(err)
	}
	// The padding makes the body larger than the 1 MiB limit of the other routes.
	padded := append(bytes.Repeat([]byte(" "), 2<<20), mixed...)

	t.Run("reads a multipart KML file with the query defaults", func(t *testing.T) {
		body, contentType := multipartBody(t, "trail.kml", trail)
		req := httptest.NewRequest(http.MethodPost, "/api/areas/parse?min_zoom=12&max_zoom=17&buffer_m=300", body)
		req.Header.Set("Content-Type", contentType)
		got := serveParse(t, h, req)
		if len(got.Areas) != 1 {
			t.Fatalf("areas = %+v, want 1", got.Areas)
		}
		a := got.Areas[0]
		if a.Kind != "route_buffer" || a.Name != "Summit trail" || a.MinZoom != 12 || a.MaxZoom != 17 || a.BufferM != 300 {
			t.Errorf("area = %+v, want the route_buffer Summit trail, zoom 12-17, buffer_m 300", a)
		}
		if want := `{"type":"LineString","coordinates":[[-121.7,46.8],[-121.7,46.82]]}`; string(a.Geometry) != want {
			t.Errorf("geometry = %s, want %s", a.Geometry, want)
		}
		if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], "1 Point feature") {
			t.Errorf("warnings = %q, want the Point warning", got.Warnings)
		}
	})
	t.Run("reads a raw GeoJSON body larger than 1 MiB", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/areas/parse", bytes.NewReader(padded))
		got := serveParse(t, h, req)
		if len(got.Areas) != 3 || got.Areas[0].Name != "County" || got.Areas[0].MaxZoom != 12 {
			t.Errorf("areas = %+v, want County, Ridge and the unnamed line", got.Areas)
		}
	})

	tests := []struct {
		name   string
		path   string
		body   []byte
		status int
		code   string
	}{
		{"rejects a KML that is not XML", "/api/areas/parse", []byte("<kml><Placemark></kml>"), http.StatusBadRequest, "invalid_area_file"},
		{"rejects a body that is no area file", "/api/areas/parse", []byte("lon,lat"), http.StatusBadRequest, "invalid_area_file"},
		{"rejects a zoom default that is not a number", "/api/areas/parse?max_zoom=x", trail, http.StatusBadRequest, "invalid_query"},
		{"rejects a body larger than 5 MiB", "/api/areas/parse", append(bytes.Repeat([]byte(" "), 6<<20), mixed...),
			http.StatusRequestEntityTooLarge, "body_too_large"},
		{"keeps the 1 MiB limit of the estimate route", "/api/estimate", padded, http.StatusRequestEntityTooLarge, "body_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(tt.body)))
			checkError(t, rec.Result(), tt.status, tt.code)
		})
	}
	t.Run("rejects GET", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/areas/parse", nil))
		checkError(t, rec.Result(), http.StatusMethodNotAllowed, "method_not_allowed")
	})
}

func serveParse(t *testing.T, h http.Handler, req *http.Request) parseResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s, want 200", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got parseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}
