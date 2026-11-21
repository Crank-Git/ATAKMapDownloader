package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/paulmach/orb"
)

// triangle is a polygon area near Mount Rainier. At zoom level 12 it selects a few tiles, and at 20 it selects more than 5,000.
const triangle = `{"kind":"polygon","geometry":{"type":"Polygon","coordinates":[[[-121.8,46.8],[-121.7,46.8],[-121.75,46.85],[-121.8,46.8]]]}}`

func TestAreaTilesRouteReturnsTheTilesOfTheArea(t *testing.T) {
	h := newServerHandler(newTestHandler(t, nil))
	rec := serve(h, http.MethodPost, "/api/areas/tiles", `{"area":`+triangle+`,"zoom":12}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		Tiles [][2]int `json:"tiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	area := tilecalc.Area{Kind: tilecalc.AreaPolygon, Geometry: orb.Polygon{{{-121.8, 46.8}, {-121.7, 46.8}, {-121.75, 46.85}, {-121.8, 46.8}}}}
	var want [][2]int
	for tile := range area.Tiles(context.Background(), 12) {
		want = append(want, tile)
	}
	if len(want) == 0 || !slices.Equal(got.Tiles, want) {
		t.Errorf("tiles = %v, want %v", got.Tiles, want)
	}
}

func TestAreaTilesRouteErrors(t *testing.T) {
	h := newServerHandler(newTestHandler(t, nil))
	bowtie := `{"kind":"polygon","geometry":{"type":"Polygon","coordinates":[[[0,0],[1,1],[1,0],[0,1],[0,0]]]}}`
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{"rejects 5,000 tiles or more, because the map shows the outline then", `{"area":` + triangle + `,"zoom":20}`,
			http.StatusRequestEntityTooLarge, "too_many_tiles"},
		{"rejects a self-intersecting polygon", `{"area":` + bowtie + `,"zoom":5}`, http.StatusBadRequest, "invalid_area"},
		{"rejects a geometry that does not decode", `{"area":{"kind":"polygon","geometry":{"type":"Point","coordinates":[0,0]}},"zoom":5}`,
			http.StatusBadRequest, "invalid_area"},
		{"rejects a zoom level above the highest", `{"area":` + triangle + `,"zoom":26}`, http.StatusBadRequest, "zoom_out_of_range"},
		{"rejects a negative zoom level", `{"area":` + triangle + `,"zoom":-1}`, http.StatusBadRequest, "zoom_out_of_range"},
		{"rejects a body that is not JSON", `{`, http.StatusBadRequest, "invalid_json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/areas/tiles", strings.NewReader(tt.body)))
			checkError(t, rec.Result(), tt.status, tt.code)
		})
	}
	t.Run("rejects GET", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/areas/tiles", nil))
		checkError(t, rec.Result(), http.StatusMethodNotAllowed, "method_not_allowed")
	})
}

func TestConfigReturnsTheTileCap(t *testing.T) {
	h := newTestHandler(t, nil)
	h.maxTiles = 1234
	rec := serve(h, http.MethodGet, "/api/config", "")
	var got struct {
		MaxTiles int `json:"max_tiles"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.MaxTiles != 1234 {
		t.Errorf("max_tiles = %d, want 1234, so the web UI can mark an area list above the tile cap", got.MaxTiles)
	}
}
