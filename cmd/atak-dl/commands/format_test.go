package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/spf13/cobra"
)

func TestDownloadRejectsAnOutputFormatThatIsNotValid(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		wantStderr string
	}{
		{"rejects an unknown format and lists the valid values", "kmz", `invalid_output_format: --format "kmz" is not one of mbtiles, osmdroid, gpkg`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			path := filepath.Join(t.TempDir(), "out")
			stderr, err := runDownloadInto(t, srv, path, []string{"--format", tt.format})
			if got := ExitCode(err); got != 1 {
				t.Errorf("ExitCode(%v) = %d, want 1", err, got)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to hold %q", stderr, tt.wantStderr)
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("Stat(%s) error = %v, want no output file", path, err)
			}
		})
	}
}

func TestDownloadWithoutOutputWritesAnMBTilesFileWithTheMBTilesExtension(t *testing.T) {
	srv := testtiles.New(t, nil)
	// The default output path is relative to the working directory.
	t.Chdir(t.TempDir())
	if stderr, err := runDownloadInto(t, srv, "", []string{"--name", "Mt Hood"}); err != nil {
		t.Fatalf("download error = %v, stderr %q", err, stderr)
	}
	info, err := output.ReadInfo(filepath.Join("downloads", "Mt_Hood_z10-10.mbtiles"))
	if err != nil || info.Format != output.MBTiles {
		t.Errorf("ReadInfo(downloads/Mt_Hood_z10-10.mbtiles) = %+v, %v; want format mbtiles", info, err)
	}
}

func TestDownloadResumesAnMBTilesFile(t *testing.T) {
	srv := testtiles.New(t, nil)
	path := filepath.Join(t.TempDir(), "out.mbtiles")
	writeOutputFile(t, path, map[string]string{"atakdl_source": "Test TLS"}, oneTile)

	if stderr, err := runDownloadInto(t, srv, path, []string{"--resume", "--format", "mbtiles"}); err != nil {
		t.Fatalf("download --resume error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0, because the file holds the tile", n)
	}
}

func TestDownloadResumesAGeoPackageFileAndSkipsTheStoredTiles(t *testing.T) {
	srv := testtiles.New(t, nil)
	path := filepath.Join(t.TempDir(), "out.gpkg")
	if stderr, err := runDownloadInto(t, srv, path, []string{"--format", "gpkg"}); err != nil {
		t.Fatalf("download --format gpkg error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != 1 {
		t.Fatalf("server got %d tile requests for the first job, want 1", n)
	}
	if info, err := output.ReadInfo(path); err != nil || info.Format != output.GeoPackage {
		t.Fatalf("ReadInfo(%s) = %+v, %v; want format gpkg", path, info, err)
	}

	if stderr, err := runDownloadInto(t, srv, path, []string{"--resume", "--format", "gpkg"}); err != nil {
		t.Fatalf("download --resume --format gpkg error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != 1 {
		t.Errorf("server got %d tile requests in total, want 1, because the resume skips the stored tile", n)
	}
}

func TestDownloadResumesAnOSMDroidFileAndSkipsTheStoredTiles(t *testing.T) {
	srv := testtiles.New(t, nil)
	path := filepath.Join(t.TempDir(), "out.sqlite")
	if stderr, err := runDownloadInto(t, srv, path, []string{"--format", "osmdroid"}); err != nil {
		t.Fatalf("download --format osmdroid error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != 1 {
		t.Fatalf("server got %d tile requests for the first job, want 1", n)
	}
	if info, err := output.ReadInfo(path); err != nil || info.Format != output.OSMDroid {
		t.Fatalf("ReadInfo(%s) = %+v, %v; want format osmdroid", path, info, err)
	}

	if stderr, err := runDownloadInto(t, srv, path, []string{"--resume", "--format", "osmdroid"}); err != nil {
		t.Fatalf("download --resume --format osmdroid error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != 1 {
		t.Errorf("server got %d tile requests in total, want 1, because the resume skips the stored tile", n)
	}
}

func TestPrepareOutputRefusesAResumeIntoAFileOfAnotherFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.sqlite")
	writeOutputFile(t, path, map[string]string{"atakdl_source": "Test TLS"}, oneTile)

	err := prepareOutput(path, ifExistsResume, "Test TLS", output.OSMDroid, false)
	if err == nil || err.Error() != "output file is mbtiles" {
		t.Errorf("prepareOutput error = %v, want output file is mbtiles", err)
	}
	if _, ok := err.(*outputConflict); !ok { //nolint:errorlint // prepareOutput returns the conflict without a wrap.
		t.Errorf("prepareOutput error type = %T, want *outputConflict, so that the CLI exits with code 3", err)
	}
}

func TestJobAPIOutputFormat(t *testing.T) {
	tests := []struct {
		name        string
		field       string
		status      int
		code        string
		wantMessage string
	}{
		{"rejects an unknown output_format and lists the valid values", `,"output_format":"kmz"`, http.StatusBadRequest, "invalid_output_format", "mbtiles, osmdroid, gpkg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newJobServer(t, nil)
			body := strings.TrimSuffix(jobBody(fourTiles), "}") + tt.field + "}"
			resp := s.request(t, http.MethodPost, "/api/jobs", body)
			defer closeBody(resp)
			if resp.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.status)
			}
			var e struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
				t.Fatal(err)
			}
			if e.Error.Code != tt.code || !strings.Contains(e.Error.Message, tt.wantMessage) {
				t.Errorf("error = %+v, want code %q and a message that holds %q", e.Error, tt.code, tt.wantMessage)
			}
			if n := s.tiles.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
		})
	}
}

func TestJobAPIWritesAnMBTilesFileByDefault(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"without output_format", ""},
		{"with output_format mbtiles", `,"output_format":"mbtiles"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newJobServer(t, nil)
			rec := s.startJob(t, strings.TrimSuffix(jobBody(fourTiles), "}")+tt.field+"}")
			if !strings.HasSuffix(rec.OutputFile, ".mbtiles") {
				t.Errorf("output_file = %q, want the extension .mbtiles", rec.OutputFile)
			}
			if final := s.finalRecord(t, rec.ID); final.State != jobComplete {
				t.Fatalf("job state = %q, want complete", final.State)
			}
			info, err := output.ReadInfo(filepath.Join(s.handler.outputDir, rec.OutputFile))
			if err != nil || info.Format != output.MBTiles {
				t.Errorf("ReadInfo = %+v, %v; want format mbtiles", info, err)
			}
		})
	}
}

// runTypedDownload runs the download command for one tile of a map source with the tile type tileType, with args at the end.
func runTypedDownload(t *testing.T, srv *testtiles.Server, tileType string, args ...string) (string, error) {
	t.Helper()
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", "", "")
	root.AddCommand(NewDownloadCmd(testUserAgent))
	var stderr bytes.Buffer
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&stderr)
	root.SetArgs(append([]string{
		"--mapsources-dir", writeTypedSource(t, srv, "Test Typed", tileType),
		"download", "--source", "Test Typed", "--bbox", oneTileBBox(),
		"--min-zoom", "10", "--max-zoom", "10", "--concurrent", "1",
	}, args...))
	err := root.Execute()
	return stderr.String(), err
}

func TestDownloadRefusesAGeoPackageOfTilesThatAreNotPNGOrJPEG(t *testing.T) {
	tests := []struct {
		name     string
		tileType string
		format   string
		refuse   bool
	}{
		{"refuses gpkg for webp tiles", "webp", "gpkg", true},
		{"refuses gpkg for gif tiles", "gif", "gpkg", true},
		{"refuses gpkg for upper case GIF tiles", "GIF", "gpkg", true},
		{"accepts gpkg for jpg tiles", "jpg", "gpkg", false},
		{"accepts gpkg for jpeg tiles", "jpeg", "gpkg", false},
		{"accepts osmdroid for webp tiles", "webp", "osmdroid", false},
		{"accepts mbtiles for gif tiles", "gif", "mbtiles", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			path := filepath.Join(t.TempDir(), "out")
			stderr, err := runTypedDownload(t, srv, tt.tileType, "--format", tt.format, "--output", path)
			if got := strings.Contains(stderr, "unsupported_tile_type"); got != tt.refuse {
				t.Fatalf("stderr = %q; holds unsupported_tile_type = %v, want %v", stderr, got, tt.refuse)
			}
			if !tt.refuse {
				return
			}
			if got := ExitCode(err); got != 1 {
				t.Errorf("ExitCode(%v) = %d, want 1", err, got)
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("Stat(%s) error = %v, want no output file", path, err)
			}
		})
	}
}

func TestJobAPIRefusesAGeoPackageOfTilesThatAreNotPNGOrJPEG(t *testing.T) {
	for _, tileType := range []string{"webp", "gif"} {
		t.Run(tileType, func(t *testing.T) {
			s := newTypedJobServer(t, nil, "Test Tiles", tileType)
			body := strings.TrimSuffix(jobBody(fourTiles), "}") + `,"output_format":"gpkg"}`
			checkError(t, s.request(t, http.MethodPost, "/api/jobs", body), http.StatusBadRequest, "unsupported_tile_type")
			if n := s.tiles.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
		})
	}
}
