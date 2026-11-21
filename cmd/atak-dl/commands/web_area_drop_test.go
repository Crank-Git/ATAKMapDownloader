package commands

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWebUIDropOfAKMLWithTwoPlacemarksAddsTwoAreaRows runs app.js in Node against the real server routes.
// The drop sends camps.kml to POST /api/areas/parse, and the counts come from POST /api/estimate.
func TestWebUIDropOfAKMLWithTwoPlacemarksAddsTwoAreaRows(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so app.js cannot run")
	}
	srv := httptest.NewServer(newServerHandler(newTestHandler(t, nil)))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	// #nosec G204 -- the arguments are test fixture paths and the test server URL.
	cmd := exec.CommandContext(t.Context(), node, filepath.Join("testdata", "area_drop.js"),
		filepath.Join("..", "..", "..", "web", "static", "js", "app.js"), srv.URL, areaFixture("camps.kml"))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node area_drop.js: %v\n%s", err, stderr.String())
	}
	var got struct {
		Rows    int      `json:"rows"`
		Names   []string `json:"names"`
		Kinds   []string `json:"kinds"`
		Tiles   []*int   `json:"tiles"`
		RowText []string `json:"rowText"`
		Total   string   `json:"total"`
		Empty   bool     `json:"empty"`
		Error   string   `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
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
