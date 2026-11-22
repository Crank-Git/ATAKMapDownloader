package commands

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
)

// arcticAreaFile returns the path of a GeoJSON file with one polygon from latitude minLat to 86.
func arcticAreaFile(t *testing.T, minLat string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "arctic.geojson")
	body := `{"type":"Feature","properties":{"min_zoom":3,"max_zoom":3},"geometry":{"type":"Polygon","coordinates":[[[10,` + minLat + `],[11,` + minLat + `],[11,86],[10,86],[10,` + minLat + `]]]}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runCommandArgs runs the estimate or download command of the map source of srv with args, and returns the error of Execute.
func runCommandArgs(t *testing.T, srv *testtiles.Server, command string, args []string) error {
	t.Helper()
	if command == "estimate" {
		_, _, err := runEstimateArgs(t, srv, args...)
		return err
	}
	_, err := runDownloadArgs(t, srv, append(args, "--output", filepath.Join(t.TempDir(), "out.sqlite"))...)
	return err
}

func TestCLIRejectsAnAreaWhollyOutsideWebMercator(t *testing.T) {
	above, atLimit := arcticAreaFile(t, "85.0512"), arcticAreaFile(t, "85.0511")
	const bboxErr = "invalid bbox: latitude 86 to 89 is outside the Web Mercator range -85.0511 to 85.0511"
	const areaErr = "latitude 85.0512 to 86 is outside the Web Mercator range"
	tests := []struct {
		name    string
		command string
		args    []string
		wantErr string
	}{
		{"estimate rejects --bbox 86,0,89,1", "estimate", []string{"--bbox", "86,0,89,1", "--zoom", "3"}, bboxErr},
		{"download rejects --bbox 86,0,89,1", "download", []string{"--bbox", "86,0,89,1", "--min-zoom", "3", "--max-zoom", "3"}, bboxErr},
		{"estimate accepts a --bbox that crosses the north limit", "estimate", []string{"--bbox", "80,0,89,1", "--zoom", "3"}, ""},
		{"estimate rejects an area file just above the north limit", "estimate", []string{"--area", above}, areaErr},
		{"download rejects an area file just above the north limit", "download", []string{"--area", above}, areaErr},
		{"estimate accepts an area file that starts at the north limit", "estimate", []string{"--area", atLimit}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			err := runCommandArgs(t, srv, tt.command, tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("%s error = %v, want nil", tt.command, err)
				}
				return
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

func TestAPIRejectsAnInputOutsideWebMercator(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	arctic := `{"kind":"polygon","geometry":{"type":"Polygon","coordinates":[[[10,85.0512],[11,85.0512],[11,86],[10,86],[10,85.0512]]]}}`
	tests := []struct {
		name    string
		path    string
		body    string
		status  int
		code    string
		message string
	}{
		{"tile-info accepts the north latitude limit", "/api/tile-info", `{"lat":85.0511,"lon":0,"zoom":3}`, http.StatusOK, "", ""},
		{"tile-info rejects a latitude above the north limit", "/api/tile-info", `{"lat":85.0512,"lon":0,"zoom":3}`, http.StatusBadRequest, "invalid_coordinate", "lat 85.0512 is outside the range -85.0511 to 85.0511"},
		{"tile-info accepts the south latitude limit", "/api/tile-info", `{"lat":-85.0511,"lon":0,"zoom":3}`, http.StatusOK, "", ""},
		{"tile-info rejects a latitude below the south limit", "/api/tile-info", `{"lat":-85.0512,"lon":0,"zoom":3}`, http.StatusBadRequest, "invalid_coordinate", "lat -85.0512 is outside the range -85.0511 to 85.0511"},
		{"tile-info accepts the east longitude limit", "/api/tile-info", `{"lat":0,"lon":180,"zoom":3}`, http.StatusOK, "", ""},
		{"tile-info rejects a longitude above the east limit", "/api/tile-info", `{"lat":0,"lon":180.0001,"zoom":3}`, http.StatusBadRequest, "invalid_coordinate", "lon 180.0001 is outside the range -180 to 180"},
		{"tile-info accepts the west longitude limit", "/api/tile-info", `{"lat":0,"lon":-180,"zoom":3}`, http.StatusOK, "", ""},
		{"tile-info rejects a longitude below the west limit", "/api/tile-info", `{"lat":0,"lon":-180.0001,"zoom":3}`, http.StatusBadRequest, "invalid_coordinate", "lon -180.0001 is outside the range -180 to 180"},
		{"tile-info accepts zoom level 25", "/api/tile-info", `{"lat":0,"lon":0,"zoom":25}`, http.StatusOK, "", ""},
		{"tile-info rejects zoom level 26", "/api/tile-info", `{"lat":0,"lon":0,"zoom":26}`, http.StatusBadRequest, "zoom_out_of_range", "zoom 26 is outside the range 0-25"},
		{"tile-info rejects zoom level -1", "/api/tile-info", `{"lat":0,"lon":0,"zoom":-1}`, http.StatusBadRequest, "zoom_out_of_range", "zoom -1 is outside the range 0-25"},
		{"estimate rejects bounds from 86 to 89", "/api/estimate", `{"source":"Test Source","bounds":[0,86,1,89]}`, http.StatusBadRequest, "invalid_bounds", "latitude 86 to 89 is outside the Web Mercator range -85.0511 to 85.0511"},
		{"estimate accepts bounds that cross the north limit", "/api/estimate", `{"source":"Test Source","bounds":[0,80,1,89],"min_zoom":3,"max_zoom":3}`, http.StatusOK, "", ""},
		{"estimate rejects a polygon above the north limit", "/api/estimate", `{"source":"Test Source","areas":[` + arctic + `]}`, http.StatusBadRequest, "invalid_area", "latitude 85.0512 to 86 is outside the Web Mercator range"},
		{"preview rejects bounds from 86 to 89", "/api/preview", `{"source":"Test Source","bounds":[0,86,1,89]}`, http.StatusBadRequest, "invalid_bounds", "latitude 86 to 89 is outside the Web Mercator range -85.0511 to 85.0511"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, http.MethodPost, tt.path, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tt.status, rec.Body.String())
			}
			if tt.code == "" {
				return
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("body does not parse as the error shape: %v", err)
			}
			if body.Error.Code != tt.code || !strings.Contains(body.Error.Message, tt.message) {
				t.Errorf("error = %+v, want code %q and a message that holds %q", body.Error, tt.code, tt.message)
			}
		})
	}
}
