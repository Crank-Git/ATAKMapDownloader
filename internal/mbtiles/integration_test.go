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

func TestPutStoresTheTestServerTileBytes(t *testing.T) {
	srv := testtiles.New(t, testtiles.Table{{Z: 1, X: 1, Y: 0}: {Kind: testtiles.JPEG}})
	path := filepath.Join(t.TempDir(), "out.mbtiles")
	w, err := Create(path, map[string]string{"name": "test"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	tests := []struct {
		name string
		path string
		tile testtiles.Tile
		tmsY int
	}{
		{"stores a PNG tile unchanged", "/1/0/0.png", testtiles.Tile{Z: 1, X: 0, Y: 0}, 1},
		{"stores a JPEG tile unchanged", "/1/1/0.jpg", testtiles.Tile{Z: 1, X: 1, Y: 0}, 1},
	}
	want := make([][]byte, len(tests))
	for i, tt := range tests {
		want[i] = fetch(t, srv, tt.path)
		if err := w.Put(tt.tile.Z, tt.tile.X, tt.tile.Y, want[i]); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	closeWriter(t, w)

	db := openFile(t, path)
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []byte
			err := db.QueryRowContext(t.Context(),
				"SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?",
				tt.tile.Z, tt.tile.X, tt.tmsY).Scan(&got)
			if err != nil {
				t.Fatalf("read tile: %v", err)
			}
			if !bytes.Equal(got, want[i]) {
				t.Errorf("stored %d bytes, want the %d bytes that the server sent", len(got), len(want[i]))
			}
		})
	}
}
