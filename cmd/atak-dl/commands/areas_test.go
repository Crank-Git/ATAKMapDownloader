package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/paulmach/orb"
	"github.com/spf13/cobra"
)

// westTiles and eastTiles are 3×3 tile areas at zoom level 10 that share 4 tiles.
var (
	westTiles = tilecalc.TileBounds{MinX: 300, MaxX: 302, MinY: 400, MaxY: 402, Zoom: 10}
	eastTiles = tilecalc.TileBounds{MinX: 301, MaxX: 303, MinY: 401, MaxY: 403, Zoom: 10}
)

// rectangleJSON returns a GeoJSON Polygon whose bounding rectangle holds exactly the tiles of tb.
func rectangleJSON(tb tilecalc.TileBounds) string {
	b := boundsOf(tb)
	return fmt.Sprintf(`{"type":"Polygon","coordinates":[[[%[1]f,%[2]f],[%[3]f,%[2]f],[%[3]f,%[4]f],[%[1]f,%[4]f],[%[1]f,%[2]f]]]}`,
		b[0], b[1], b[2], b[3])
}

// boundsAreaJSON returns one bounds area of a job request for the tiles of tb at its zoom level.
func boundsAreaJSON(name string, tb tilecalc.TileBounds) string {
	return fmt.Sprintf(`{"kind":"bounds","name":%q,"geometry":%s,"min_zoom":%d,"max_zoom":%d}`, name, rectangleJSON(tb), tb.Zoom, tb.Zoom)
}

// twoAreas is the areas field of a job request for westTiles and eastTiles.
var twoAreas = `"areas":[` + boundsAreaJSON("west", westTiles) + `,` + boundsAreaJSON("east", eastTiles) + `]`

func TestJobAPIRequestsEachTileThatTwoAreasShareOnce(t *testing.T) {
	s := newJobServer(t, nil)
	rec := s.startJob(t, `{"source":"Test Tiles",`+twoAreas+`}`)
	// The two areas hold 9 tiles each and share 4.
	if rec.Total != 14 {
		t.Errorf("job record total = %d, want 14", rec.Total)
	}
	if got := s.finalRecord(t, rec.ID); got.State != jobComplete {
		t.Fatalf("final record = %+v, want state complete", got)
	}
	if n := s.tiles.TotalRequests(); n != 14 {
		t.Errorf("tile server got %d requests, want 14", n)
	}
	for _, tb := range []tilecalc.TileBounds{westTiles, eastTiles} {
		for x := tb.MinX; x <= tb.MaxX; x++ {
			for y := tb.MinY; y <= tb.MaxY; y++ {
				if n := s.tiles.Requests(testtiles.Tile{Z: tb.Zoom, X: x, Y: y}); n != 1 {
					t.Errorf("tile %d/%d/%d got %d requests, want 1", tb.Zoom, x, y, n)
				}
			}
		}
	}
}

func TestJobAPIStoresTheBoundsOfALegacyRequestAsOneArea(t *testing.T) {
	s := newJobServer(t, nil)
	rec := s.startJob(t, jobBody(westTiles))
	if rec.Total != 9 {
		t.Errorf("job record total = %d, want 9", rec.Total)
	}
	if got := s.finalRecord(t, rec.ID); got.State != jobComplete {
		t.Fatalf("final record = %+v, want state complete", got)
	}
	stored, ok, err := mbtiles.ReadMetadata(filepath.Join(s.handler.outputDir, rec.OutputFile), "atakdl_areas")
	if err != nil || !ok {
		t.Fatalf("metadata atakdl_areas = %v, %v; want a row", ok, err)
	}
	areas, err := tilecalc.DecodeAreas([]byte(stored))
	if err != nil {
		t.Fatalf("DecodeAreas(atakdl_areas) = %v", err)
	}
	if len(areas) != 1 || areas[0].Kind != tilecalc.AreaBounds || areas[0].MinZoom != 10 || areas[0].MaxZoom != 10 {
		t.Errorf("atakdl_areas = %+v, want one bounds area at zoom level 10", areas)
	}
}

// selfIntersecting is a GeoJSON Polygon whose edges cross.
const selfIntersecting = `{"type":"Polygon","coordinates":[[[0,0],[1,1],[1,0],[0,1],[0,0]]]}`

// areaListCases are the invalid area lists that POST /api/jobs and POST /api/estimate reject.
var areaListCases = []struct {
	name string
	body string
	code string
}{
	{"rejects areas together with bounds", `"bounds":[0,0,1,1],` + twoAreas, "invalid_area"},
	{"rejects an empty area list", `"areas":[]`, "invalid_area"},
	{"rejects 21 areas", `"areas":[` + strings.Repeat(boundsAreaJSON("a", westTiles)+",", 20) + boundsAreaJSON("a", westTiles) + `]`, "invalid_area"},
	{"rejects an unknown kind", `"areas":[{"kind":"circle","geometry":` + rectangleJSON(westTiles) + `,"min_zoom":1,"max_zoom":2}]`, "invalid_area"},
	{"rejects a Point geometry", `"areas":[{"kind":"polygon","geometry":{"type":"Point","coordinates":[0,0]},"min_zoom":1,"max_zoom":2}]`, "invalid_area"},
	{"rejects a self-intersecting polygon", `"areas":[{"kind":"polygon","geometry":` + selfIntersecting + `,"min_zoom":1,"max_zoom":2}]`, "invalid_area"},
	{"rejects a route buffer without buffer_m", `"areas":[{"kind":"route_buffer","geometry":{"type":"LineString","coordinates":[[0,0],[1,1]]},"min_zoom":1,"max_zoom":2}]`, "invalid_area"},
	{"rejects an area with min_zoom above max_zoom", `"areas":[{"kind":"bounds","geometry":` + rectangleJSON(westTiles) + `,"min_zoom":5,"max_zoom":4}]`, "zoom_out_of_range"},
	{"rejects an area with 20,001 vertices", `"areas":[` + lineAreaJSON(tilecalc.MaxAreaVertices+1) + `]`, "invalid_area"},
	{"rejects three areas with 60,000 vertices", `"areas":[` + lineAreaJSON(20_000) + `,` + lineAreaJSON(20_000) + `,` + lineAreaJSON(20_000) + `]`, "invalid_area"},
}

// lineAreaJSON returns a route buffer area of a job request whose line has n vertices.
func lineAreaJSON(n int) string {
	coords := make([]string, n)
	for i := range coords {
		coords[i] = fmt.Sprintf("[%f,45]", 10+float64(i)*1e-5)
	}
	return `{"kind":"route_buffer","geometry":{"type":"LineString","coordinates":[` + strings.Join(coords, ",") + `]},"buffer_m":100,"min_zoom":1,"max_zoom":2}`
}

func TestJobAPIAcceptsAnAreaListOf2MiB(t *testing.T) {
	s := newJobServer(t, nil)
	// White space makes the body 2 MiB, so the body limit is the only new thing that the request meets.
	body := `{"source":"Test Tiles","areas":[` + strings.Repeat(" ", 2<<20) + boundsAreaJSON("west", westTiles) + `]}`
	rec := serve(newServerHandler(s.handler), http.MethodPost, "/api/jobs", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
	got := decodeRecord(t, rec.Body)
	if final := s.finalRecord(t, got.ID); final.State != jobComplete {
		t.Errorf("final record = %+v, want state complete", final)
	}
}

func TestJobAPIWithAnOldSourceNameAndAnAreaListUsesTheV2Name(t *testing.T) {
	s := newNamedJobServer(t, nil, "USGS Topo")
	rec := s.startJob(t, `{"source":"USGS - Usgsbasemap",`+twoAreas+`}`)
	if rec.Source != "USGS Topo" {
		t.Errorf("record source = %q, want %q", rec.Source, "USGS Topo")
	}
	if !strings.HasPrefix(rec.OutputFile, "USGS_Topo") {
		t.Errorf("output file = %q, want a name that starts with the v2 name", rec.OutputFile)
	}
	if got := s.finalRecord(t, rec.ID); got.State != jobComplete {
		t.Errorf("final record = %+v, want state complete", got)
	}
}

func TestJobAPIRejectsAnInvalidAreaList(t *testing.T) {
	s := newJobServer(t, nil)
	cases := append(areaListCases, struct {
		name string
		body string
		code string
	}{"rejects an area above the source max_zoom", `"areas":[{"kind":"bounds","geometry":` + rectangleJSON(westTiles) + `,"min_zoom":1,"max_zoom":19}]`, "zoom_out_of_range"})
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			checkError(t, s.request(t, http.MethodPost, "/api/jobs", `{"source":"Test Tiles",`+tt.body+`}`), http.StatusBadRequest, tt.code)
		})
	}
	if n := s.tiles.TotalRequests(); n != 0 {
		t.Errorf("tile server got %d requests, want 0", n)
	}
}

func TestEstimateRejectsAnInvalidAreaList(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	for _, tt := range areaListCases {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, http.MethodPost, "/api/estimate", `{`+tt.body+`}`)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			var body apiError
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Error.Code != tt.code {
				t.Errorf("error = %+v, %v; want code %q", body.Error, err, tt.code)
			}
		})
	}
}

func TestEstimateCountsEachAreaAndTheUniqueTiles(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	rec := serve(h, http.MethodPost, "/api/estimate", `{`+twoAreas+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var est struct {
		TotalTiles  int `json:"total_tiles"`
		UniqueTiles int `json:"unique_tiles"`
		Areas       []struct {
			Name  string `json:"name"`
			Tiles int    `json:"tiles"`
		} `json:"areas"`
		ZoomBreakdown map[string]int `json:"zoom_breakdown"`
		ZoomRange     []int          `json:"zoom_range"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&est); err != nil {
		t.Fatalf("decode estimate: %v", err)
	}
	if est.UniqueTiles != 14 || est.TotalTiles != 14 {
		t.Errorf("unique_tiles, total_tiles = %d, %d; want 14, 14", est.UniqueTiles, est.TotalTiles)
	}
	if len(est.Areas) != 2 || est.Areas[0].Name != "west" || est.Areas[0].Tiles != 9 || est.Areas[1].Name != "east" || est.Areas[1].Tiles != 9 {
		t.Errorf("areas = %+v, want west 9 and east 9", est.Areas)
	}
	if est.ZoomBreakdown["10"] != 14 || len(est.ZoomRange) != 2 || est.ZoomRange[0] != 10 || est.ZoomRange[1] != 10 {
		t.Errorf("zoom_breakdown, zoom_range = %v, %v; want map[10:14], [10 10]", est.ZoomBreakdown, est.ZoomRange)
	}
}

func TestEstimateOfALegacyRequestReturnsOneArea(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	b := boundsOf(westTiles)
	rec := serve(h, http.MethodPost, "/api/estimate", fmt.Sprintf(`{"bounds":[%f,%f,%f,%f],"min_zoom":10,"max_zoom":10}`, b[0], b[1], b[2], b[3]))
	var est struct {
		UniqueTiles int `json:"unique_tiles"`
		Areas       []struct {
			Tiles int `json:"tiles"`
		} `json:"areas"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&est); err != nil {
		t.Fatalf("decode estimate: %v", err)
	}
	if est.UniqueTiles != 9 || len(est.Areas) != 1 || est.Areas[0].Tiles != 9 {
		t.Errorf("estimate = %+v, want unique_tiles 9 and one area of 9 tiles", est)
	}
}

// runDownloadArgs runs the download command of the map source of srv with args after "download".
func runDownloadArgs(t *testing.T, srv *testtiles.Server, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", "", "")
	root.AddCommand(NewDownloadCmd(testUserAgent))
	var stderr bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--mapsources-dir", writeSource(t, srv), "download", "--source", "Test TLS", "--concurrent", "1"}, args...))
	err := root.Execute()
	return stderr.String(), err
}

func TestDownloadResumeReadsTheAreasOfTheOutputFile(t *testing.T) {
	square := tilecalc.TileBounds{MinX: 300, MaxX: 305, MinY: 400, MaxY: 405, Zoom: 10}
	b := boundsOf(square)
	triangle := []tilecalc.JobArea{{
		Area:    tilecalc.Area{Kind: tilecalc.AreaPolygon, Geometry: orb.Polygon{{{b[0], b[1]}, {b[2], b[1]}, {b[0], b[3]}, {b[0], b[1]}}}},
		MinZoom: 10, MaxZoom: 10,
	}}
	var all []testtiles.Tile
	for tile := range tilecalc.JobTiles(t.Context(), triangle) {
		all = append(all, testtiles.Tile{Z: tile.Z, X: tile.X, Y: tile.Y})
	}
	if len(all) >= 36 {
		t.Fatalf("the triangle selects %d tiles, want fewer than the 36 of its bounding rectangle", len(all))
	}
	encoded, err := tilecalc.EncodeAreas(triangle)
	if err != nil {
		t.Fatal(err)
	}
	srv := testtiles.New(t, nil)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	stored := all[:len(all)/2]
	writeOutputFile(t, output, map[string]string{"atakdl_source": "Test TLS", "atakdl_areas": string(encoded)}, stored...)

	if stderr, err := runDownloadArgs(t, srv, "--output", output, "--resume"); err != nil {
		t.Fatalf("download --resume = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != len(all)-len(stored) {
		t.Errorf("tile server got %d requests, want %d, the missing tiles of the triangle", n, len(all)-len(stored))
	}
	for _, tile := range stored {
		if n := srv.Requests(tile); n != 0 {
			t.Errorf("stored tile %v got %d requests, want 0", tile, n)
		}
	}
	if n := tileRows(t, output); n != len(all) {
		t.Errorf("output file holds %d tile rows, want %d", n, len(all))
	}
}

func TestDownloadNeedsTheBBoxUnlessItResumesAFileWithAreas(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]string
		args     []string
		wantErr  string
	}{
		{"rejects a job with no --bbox", nil, []string{"--output", "out.sqlite"}, "--bbox"},
		{"rejects a resume with no --output", nil, []string{"--resume"}, "--bbox"},
		{"rejects a resume into a file with no atakdl_areas row", map[string]string{"atakdl_source": "Test TLS"},
			[]string{"--output", "out.sqlite", "--resume"}, "atakdl_areas"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			dir := t.TempDir()
			args := append([]string(nil), tt.args...)
			for i, a := range args {
				if a == "out.sqlite" {
					args[i] = filepath.Join(dir, a)
				}
			}
			if tt.metadata != nil {
				writeOutputFile(t, filepath.Join(dir, "out.sqlite"), tt.metadata)
			}
			_, err := runDownloadArgs(t, srv, args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) || ExitCode(err) != 1 {
				t.Errorf("download error = %v (exit code %d), want exit code 1 and an error that holds %q", err, ExitCode(err), tt.wantErr)
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("tile server got %d requests, want 0", n)
			}
		})
	}
}
