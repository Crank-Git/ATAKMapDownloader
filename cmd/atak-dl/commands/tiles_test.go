package commands

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
)

// pngTile is a tile body that starts with the PNG signature, so the route detects image/png.
var pngTile = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR one known tile")

// writeTileFile writes an output file at name in the output directory of h with data at the XYZ tile z, x, y.
func writeTileFile(t *testing.T, h *WebHandler, name string, z, x, y int, data []byte) {
	t.Helper()
	w, err := mbtiles.Open(filepath.Join(h.outputDir, name), fileMetadata)
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	if err := w.Put(z, x, y, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}
}

func TestTileRouteReturnsTheStoredTileAtItsXYZPosition(t *testing.T) {
	h := filesHandler(t)
	writeTileFile(t, h, "area.mbtiles", 10, 300, 400, pngTile)

	rec := serve(h, http.MethodGet, "/api/files/area.mbtiles/tiles/10/300/400", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if rec.Body.String() != string(pngTile) {
		t.Errorf("body = %q, want the stored tile", rec.Body)
	}

	// The output file stores TMS row 623 for XYZ row 400, so a request for XYZ row 623 is a different tile.
	if rec := serve(h, http.MethodGet, "/api/files/area.mbtiles/tiles/10/300/623", ""); rec.Code != http.StatusNoContent {
		t.Errorf("GET the XYZ row 623 status = %d, want 204", rec.Code)
	}
}

func TestTileRouteReturns204ForATileThatTheFileDoesNotHold(t *testing.T) {
	h := filesHandler(t)
	writeTileFile(t, h, "area.mbtiles", 10, 300, 400, pngTile)

	rec := serve(h, http.MethodGet, "/api/files/area.mbtiles/tiles/10/301/400", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want no body", rec.Body)
	}
}

func TestTileRouteServesTheCommittedTilesOfAnOpenOutputFile(t *testing.T) {
	h := filesHandler(t)
	w, err := mbtiles.Open(filepath.Join(h.outputDir, "running.mbtiles"), fileMetadata)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := w.Put(10, 300, 400, pngTile); err != nil {
		t.Fatal(err)
	}
	// Failed commits every queued Put first, as the batch commit of a running job does.
	if _, err := w.Failed(); err != nil {
		t.Fatal(err)
	}

	rec := serve(h, http.MethodGet, "/api/files/running.mbtiles/tiles/10/300/400", "")
	if rec.Code != http.StatusOK || rec.Body.String() != string(pngTile) {
		t.Errorf("status = %d, body %q; want 200 and the stored tile", rec.Code, rec.Body)
	}
}

func TestTileRouteReturns404ForAFileThatIsNotAnMBTilesOutputFile(t *testing.T) {
	h := filesHandler(t)
	writeTileFile(t, h, "area.mbtiles", 10, 300, 400, pngTile)
	for name, body := range map[string]string{
		"notes.mbtiles": "not a database",
		"package.zip":   "PK\x03\x04 a data package",
	} {
		if err := os.WriteFile(filepath.Join(h.outputDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(filepath.Dir(h.outputDir), "outside.mbtiles")
	writeOutputFile(t, outside, fileMetadata, oneTile)
	if err := os.Symlink(outside, filepath.Join(h.outputDir, "escape.mbtiles")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		code string
	}{
		{"rejects an unknown file", "/api/files/missing.mbtiles/tiles/10/300/400", "file_not_found"},
		{"rejects a file that is not an SQLite database", "/api/files/notes.mbtiles/tiles/10/300/400", "file_not_found"},
		{"rejects a data package", "/api/files/package.zip/tiles/10/300/400", "file_not_found"},
		{"rejects a symbolic link that leaves the output directory", "/api/files/escape.mbtiles/tiles/10/300/400", "file_not_found"},
		{"rejects a parent path", "/api/files/../tiles/10/300/400", "file_not_found"},
		{"rejects a zoom level that is not a number", "/api/files/area.mbtiles/tiles/z/300/400", "not_found"},
		{"rejects a negative column", "/api/files/area.mbtiles/tiles/10/-1/400", "not_found"},
		{"rejects a row outside the zoom level", "/api/files/area.mbtiles/tiles/10/300/1024", "not_found"},
		{"rejects a zoom level above 30", "/api/files/area.mbtiles/tiles/31/0/0", "not_found"},
		{"rejects a missing row", "/api/files/area.mbtiles/tiles/10/300", "file_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkError(t, serve(h, http.MethodGet, tt.path, "").Result(), http.StatusNotFound, tt.code)
		})
	}
	if _, err := os.Stat(filepath.Join(h.outputDir, "missing.mbtiles")); !os.IsNotExist(err) {
		t.Errorf("stat missing.mbtiles after the request: %v, want it still missing", err)
	}
}
