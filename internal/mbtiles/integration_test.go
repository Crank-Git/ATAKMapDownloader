package mbtiles

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
)

// fetch returns the body that srv sends for path.
func fetch(t *testing.T, srv *testtiles.Server, path string) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // The URL comes from the test server.
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }() // The body is fully read, so a close error loses nothing.
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestInsertTileStoresTheTestServerTileBytes(t *testing.T) {
	srv := testtiles.New(t, testtiles.Table{{Z: 1, X: 1, Y: 0}: {Kind: testtiles.JPEG}})
	db, err := Create(filepath.Join(t.TempDir(), "out.mbtiles"), map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})

	tests := []struct {
		name string
		path string
		tms  testtiles.Tile
	}{
		{"stores a PNG tile unchanged", "/1/0/0.png", testtiles.Tile{Z: 1, X: 0, Y: 1}},
		{"stores a JPEG tile unchanged", "/1/1/0.jpg", testtiles.Tile{Z: 1, X: 1, Y: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			want := fetch(t, srv, tt.path)
			if err := InsertTile(db, tt.tms.Z, tt.tms.X, tt.tms.Y, want); err != nil {
				t.Fatalf("InsertTile: %v", err)
			}
			var got []byte
			err := db.QueryRowContext(t.Context(),
				"SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?",
				tt.tms.Z, tt.tms.X, tt.tms.Y).Scan(&got)
			if err != nil {
				t.Fatalf("read tile: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("stored %d bytes, want the %d bytes that the server sent", len(got), len(want))
			}
		})
	}
}
