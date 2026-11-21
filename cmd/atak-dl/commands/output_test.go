package commands

import (
	"bytes"
	"database/sql"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

// oneTile is the tile that oneTileBBox holds.
var oneTile = testtiles.Tile{Z: 10, X: 300, Y: 400}

// writeOutputFile writes an output file at path with the metadata rows in metadata and the tiles in tiles.
func writeOutputFile(t *testing.T, path string, metadata map[string]string, tiles ...testtiles.Tile) {
	t.Helper()
	w, err := mbtiles.Open(path, metadata)
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	for _, tile := range tiles {
		if err := w.Put(tile.Z, tile.X, tile.Y, []byte("stored")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}
}

// tileRows returns the number of rows in the tiles table of the output file at path.
func tileRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	defer func() { _ = db.Close() }() // The query below reports a failure of the file.
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM tiles").Scan(&n); err != nil {
		t.Fatalf("count tiles: %v", err)
	}
	return n
}

func TestDownloadIntoAnExistingOutputFile(t *testing.T) {
	tests := []struct {
		name string
		// metadata is the metadata of the existing output file. A nil map writes a text file in place of an output file.
		metadata   map[string]string
		args       []string
		exitCode   int
		wantStderr string
		requests   int
		// unchanged requires the same file bytes after the command.
		unchanged bool
	}{
		{
			name:       "refuses the job with exit code 3 and keeps the file without a flag",
			metadata:   nil,
			exitCode:   3,
			wantStderr: "output file exists: use --resume or --overwrite",
			unchanged:  true,
		},
		{
			name:     "deletes the file and downloads every tile with --overwrite",
			metadata: nil,
			args:     []string{"--overwrite"},
			requests: 1,
		},
		{
			name:     "skips the stored tile with --resume",
			metadata: map[string]string{"atakdl_source": "Test TLS"},
			args:     []string{"--resume"},
			requests: 0,
		},
		{
			name:       "refuses a file of another map source with exit code 3 and --resume",
			metadata:   map[string]string{"atakdl_source": "Other"},
			args:       []string{"--resume"},
			exitCode:   3,
			wantStderr: "output file belongs to map source Other",
			unchanged:  true,
		},
		{
			name:       "refuses a file with no map source name with --resume and no --force",
			metadata:   map[string]string{"name": "v0.1"},
			args:       []string{"--resume"},
			exitCode:   3,
			wantStderr: "output file names no map source: use --resume --force",
			unchanged:  true,
		},
		{
			name:     "accepts a file with no map source name with --resume --force",
			metadata: map[string]string{"name": "v0.1"},
			args:     []string{"--resume", "--force"},
			requests: 0,
		},
		{
			name:       "rejects --resume together with --overwrite",
			metadata:   map[string]string{"atakdl_source": "Test TLS"},
			args:       []string{"--resume", "--overwrite"},
			exitCode:   1,
			wantStderr: "none of the others can be",
			unchanged:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			output := filepath.Join(t.TempDir(), "out.sqlite")
			if tt.metadata == nil {
				if err := os.WriteFile(output, []byte("my notes"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				writeOutputFile(t, output, tt.metadata, oneTile)
			}
			before, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}

			root := &cobra.Command{Use: "atak-dl"}
			root.PersistentFlags().String("mapsources-dir", "", "")
			root.AddCommand(NewDownloadCmd(testUserAgent))
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			root.SetArgs(append([]string{
				"--mapsources-dir", writeSource(t, srv),
				"download",
				"--source", "Test TLS",
				"--bbox", oneTileBBox(),
				"--min-zoom", "10", "--max-zoom", "10",
				"--concurrent", "1",
				"--output", output,
			}, tt.args...))

			err = root.Execute()
			if got := ExitCode(err); got != tt.exitCode {
				t.Errorf("ExitCode(%v) = %d, want %d", err, got, tt.exitCode)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to hold %q", stderr.String(), tt.wantStderr)
			}
			if n := srv.TotalRequests(); n != tt.requests {
				t.Errorf("server got %d tile requests, want %d", n, tt.requests)
			}
			if tt.unchanged {
				after, err := os.ReadFile(output)
				if err != nil || !bytes.Equal(after, before) {
					t.Errorf("output file changed: read error %v, %d bytes before, %d bytes after", err, len(before), len(after))
				}
				return
			}
			if n := tileRows(t, output); n != 1 {
				t.Errorf("output file holds %d tile rows, want 1", n)
			}
		})
	}
}

// fourTiles is a 2×2 tile area at zoom level 10.
var fourTiles = tilecalc.TileBounds{MinX: 300, MaxX: 301, MinY: 400, MaxY: 401, Zoom: 10}

func TestJobAPIIfExists(t *testing.T) {
	tests := []struct {
		name     string
		ifExists string
		status   int
		code     string
		requests int
	}{
		{"returns 409 output_exists without if_exists", "", http.StatusConflict, "output_exists", 0},
		{"returns 409 output_exists for fail", `,"if_exists":"fail"`, http.StatusConflict, "output_exists", 0},
		{"skips the stored tiles for resume", `,"if_exists":"resume"`, http.StatusAccepted, "", 0},
		{"downloads every tile again for overwrite", `,"if_exists":"overwrite"`, http.StatusAccepted, "", 4},
		{"rejects an unknown value", `,"if_exists":"append"`, http.StatusBadRequest, "invalid_if_exists", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newJobServer(t, nil)
			first := s.startJob(t, jobBody(fourTiles))
			if final := s.finalRecord(t, first.ID); final.State != jobComplete {
				t.Fatalf("first job state = %q, want complete", final.State)
			}
			before := s.tiles.TotalRequests()

			body := strings.TrimSuffix(jobBody(fourTiles), "}") + tt.ifExists + "}"
			resp := s.request(t, http.MethodPost, "/api/jobs", body)
			if tt.status != http.StatusAccepted {
				checkError(t, resp, tt.status, tt.code)
				return
			}
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("POST /api/jobs status = %d, want 202", resp.StatusCode)
			}
			rec := decodeRecord(t, resp.Body)
			closeBody(resp)
			final := s.finalRecord(t, rec.ID)
			if final.State != jobComplete {
				t.Errorf("second job state = %q, want complete", final.State)
			}
			if n := s.tiles.TotalRequests() - before; n != tt.requests {
				t.Errorf("the second job sent %d requests, want %d", n, tt.requests)
			}
			if want := int64(4 - tt.requests); final.Skipped != want {
				t.Errorf("second job skipped = %d, want %d", final.Skipped, want)
			}
		})
	}
}
