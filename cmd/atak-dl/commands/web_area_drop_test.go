package commands

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// areaDropResult is the area list that area_drop.js prints after the drop.
type areaDropResult struct {
	Rows    int      `json:"rows"`
	Names   []string `json:"names"`
	Kinds   []string `json:"kinds"`
	Tiles   []*int   `json:"tiles"`
	RowText []string `json:"rowText"`
	Total   string   `json:"total"`
	Empty   bool     `json:"empty"`
	Error   string   `json:"error"`
	// FitBounds holds one entry per map.fitBounds call. Bounds is [west, south, east, north].
	FitBounds []struct {
		Bounds  [4]float64 `json:"bounds"`
		Options struct {
			Padding []float64 `json:"padding"`
		} `json:"options"`
	} `json:"fitBounds"`
}

// dropAreaFile runs app.js in Node against a test server, drops the area fixture name, and returns the area list.
func dropAreaFile(t *testing.T, name string) areaDropResult {
	t.Helper()
	return runAreaDrop(t, areaFixture(name))
}

// runAreaDrop runs area_drop.js with areaArg, which is an area file path or "draw".
func runAreaDrop(t *testing.T, areaArg string) areaDropResult {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so app.js cannot run")
	}
	srv := httptest.NewServer(newServerHandler(newTestHandler(t, nil)))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	// #nosec G204 -- the arguments are test fixture paths and the test server URL.
	cmd := exec.CommandContext(t.Context(), node, filepath.Join("testdata", "area_drop.js"),
		filepath.Join("..", "..", "..", "web", "static", "js", "app.js"), srv.URL, areaArg)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node area_drop.js: %v\n%s", err, stderr.String())
	}
	var got areaDropResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	return got
}

// TestWebUIDropOfAKMLWithTwoPlacemarksAddsTwoAreaRows runs app.js in Node against the real server routes.
// The drop sends camps.kml to POST /api/areas/parse, and the counts come from POST /api/estimate.
func TestWebUIDropOfAKMLWithTwoPlacemarksAddsTwoAreaRows(t *testing.T) {
	got := dropAreaFile(t, "camps.kml")
	if got.Rows != 2 {
		t.Fatalf("the area list has %d rows, want 2: %+v", got.Rows, got)
	}
	if want := []string{"North camp", "South camp"}; !slices.Equal(got.Names, want) {
		t.Errorf("names = %q, want %q", got.Names, want)
	}
	if want := []string{"polygon", "polygon"}; !slices.Equal(got.Kinds, want) {
		t.Errorf("kinds = %q, want %q", got.Kinds, want)
	}
	for i, row := range got.RowText {
		if !strings.Contains(row, got.Names[i]) || !strings.Contains(row, "Polygon") || !strings.Contains(row, "tiles") {
			t.Errorf("row %d = %q, want the name, the kind and the tile count", i, row)
		}
		if got.Tiles[i] == nil || *got.Tiles[i] <= 0 {
			t.Errorf("row %d has no tile count from POST /api/estimate", i)
		}
	}
	if !got.Empty {
		t.Error("the empty state shows next to two areas")
	}
	if !strings.Contains(got.Total, "tiles") {
		t.Errorf("total = %q, want the unique tile count", got.Total)
	}
	if got.Error != "" {
		t.Errorf("the error banner shows %q", got.Error)
	}
}

// fitZoom returns the zoom that Leaflet picks to fit bounds into a width x height map, with zoomSnap 1.
// bounds is [west, south, east, north] in degrees, and padding is the space in pixels on each side.
func fitZoom(bounds [4]float64, width, height, padding float64) int {
	mercY := func(lat float64) float64 {
		r := lat * math.Pi / 180
		return (1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2
	}
	// At zoom 0 the world is one 256 pixel tile.
	dx := (bounds[2] - bounds[0]) / 360 * 256
	dy := (mercY(bounds[1]) - mercY(bounds[3])) * 256
	return int(math.Floor(math.Log2(math.Min((width-2*padding)/dx, (height-2*padding)/dy))))
}

// TestWebUIImportFitsTheMapToTheImportedAreas checks that the map moves to the two camps of camps.kml (#124).
func TestWebUIImportFitsTheMapToTheImportedAreas(t *testing.T) {
	got := dropAreaFile(t, "camps.kml")
	if len(got.FitBounds) != 1 {
		t.Fatalf("the import called map.fitBounds %d times, want 1: %+v", len(got.FitBounds), got.FitBounds)
	}
	b := got.FitBounds[0].Bounds
	// camps.kml holds North camp at -121.8..-121.7, 46.9..46.95 and South camp at -121.8..-121.7, 46.7..46.75.
	if b[0] > -121.8 || b[1] > 46.7 || b[2] < -121.7 || b[3] < 46.95 {
		t.Errorf("bounds = %v, want bounds that hold both camps", b)
	}
	padding := got.FitBounds[0].Options.Padding
	if len(padding) != 2 || padding[0] <= 0 {
		t.Fatalf("padding = %v, want a padding so the areas do not touch the map edge", padding)
	}
	// The default view of app.js is zoom 4. A 1024 x 768 map is a small laptop window.
	if z := fitZoom(b, 1024, 768, padding[0]); z <= 4 {
		t.Errorf("the fitted zoom is %d, want a zoom higher than the default view zoom 4", z)
	}
}

// TestWebUIMapStaysAtItsViewWithNoImportedArea checks that a failed import and a draw do not move the map.
func TestWebUIMapStaysAtItsViewWithNoImportedArea(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.kml")
	if err := os.WriteFile(bad, []byte("this is not KML"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		areaArg string
		rows    int
		wantErr bool
	}{
		{"a failed import", bad, 0, true},
		{"a draw with the Polygon tool", "draw", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runAreaDrop(t, tt.areaArg)
			if got.Rows != tt.rows {
				t.Fatalf("the area list has %d rows, want %d: %+v", got.Rows, tt.rows, got)
			}
			if (got.Error != "") != tt.wantErr {
				t.Errorf("the error banner shows %q, want an error: %v", got.Error, tt.wantErr)
			}
			if len(got.FitBounds) != 0 {
				t.Errorf("map.fitBounds was called %d times, want 0", len(got.FitBounds))
			}
		})
	}
}
