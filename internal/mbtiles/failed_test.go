package mbtiles

import (
	"fmt"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/output"
)

// failedTiles returns the failed tiles that w lists, and fails the test on an error.
func failedTiles(t *testing.T, w *Writer) []output.FailedTile {
	t.Helper()
	tiles, err := w.Failed()
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	return tiles
}

func TestFailRecordsTheTileWithItsLastReasonAndItsAttempts(t *testing.T) {
	tests := []struct {
		name    string
		reasons []string
		want    output.FailedTile
	}{
		{"records one failure with attempts 1", []string{"http_503"}, output.FailedTile{Z: 2, X: 3, Y: 2, Reason: "http_503", Attempts: 1}},
		{"keeps the last reason and counts each failure", []string{"http_503", "not_image"}, output.FailedTile{Z: 2, X: 3, Y: 2, Reason: "not_image", Attempts: 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, path := create(t, map[string]string{"name": "test"})
			for _, r := range tt.reasons {
				if err := w.Fail(2, 3, 2, r); err != nil {
					t.Fatalf("Fail: %v", err)
				}
			}
			if got := failedTiles(t, w); fmt.Sprint(got) != fmt.Sprint([]output.FailedTile{tt.want}) {
				t.Errorf("Failed = %v, want [%v]", got, tt.want)
			}
			closeWriter(t, w)

			// The XYZ row 2 at zoom level 2 is the TMS row 1, and the table stores TMS rows like tiles does.
			var reason string
			var attempts int
			err := openFile(t, path).QueryRowContext(t.Context(),
				"SELECT reason, attempts FROM atakdl_failed_tiles WHERE zoom_level = 2 AND tile_column = 3 AND tile_row = 1",
			).Scan(&reason, &attempts)
			if err != nil || reason != tt.want.Reason || attempts != tt.want.Attempts {
				t.Errorf("row at TMS row 1 = %q, %d, %v; want %q, %d, nil", reason, attempts, err, tt.want.Reason, tt.want.Attempts)
			}
		})
	}
}

func TestPutRemovesTheFailedTileRow(t *testing.T) {
	w, _ := create(t, map[string]string{"name": "test"})
	if err := w.Fail(2, 3, 2, "http_503"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := w.Fail(2, 0, 0, "not_found"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if err := w.Put(2, 3, 2, []byte("tile")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := []output.FailedTile{{Z: 2, X: 0, Y: 0, Reason: "not_found", Attempts: 1}}
	if got := failedTiles(t, w); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Failed = %v, want %v", got, want)
	}
	closeWriter(t, w)
}

func TestOpenKeepsTheFailedTilesOfAnExistingOutputFile(t *testing.T) {
	w, path := create(t, map[string]string{"name": "test"})
	if err := w.Fail(4, 5, 6, "network"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	closeWriter(t, w)

	w, err := Open(path, map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	want := []output.FailedTile{{Z: 4, X: 5, Y: 6, Reason: "network", Attempts: 1}}
	if got := failedTiles(t, w); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Failed = %v, want %v", got, want)
	}
	if w.Has(4, 5, 6) {
		t.Error("Has reports a failed tile, want false because the tiles table has no row for it")
	}
	closeWriter(t, w)
}
