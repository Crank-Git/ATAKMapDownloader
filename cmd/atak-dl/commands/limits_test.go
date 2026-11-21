package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

// bboxOf returns a --bbox value that holds exactly the tiles of tb.
func bboxOf(tb tilecalc.TileBounds) string {
	b := boundsOf(tb)
	return fmt.Sprintf("%f,%f,%f,%f", b[1], b[0], b[3], b[2])
}

// downloadRun is the result of one run of the download command.
type downloadRun struct {
	stdout, stderr string
	err            error
}

// runDownloadOf runs the download command with ctx for the tiles of tb from the map source of srv into output, with args last.
func runDownloadOf(ctx context.Context, t *testing.T, srv *testtiles.Server, tb tilecalc.TileBounds, output string, args ...string) downloadRun {
	t.Helper()
	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", "", "")
	root.AddCommand(NewDownloadCmd(testUserAgent))
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	zoom := fmt.Sprint(tb.Zoom)
	root.SetArgs(append([]string{
		"--mapsources-dir", writeSource(t, srv),
		"download",
		"--source", "Test TLS",
		"--bbox", bboxOf(tb),
		"--min-zoom", zoom, "--max-zoom", zoom,
		"--concurrent", "1",
		"--output", output,
	}, args...))
	err := root.ExecuteContext(ctx)
	return downloadRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// oneTileArea is the area of oneTile.
var oneTileArea = tilecalc.TileBounds{MinX: oneTile.X, MaxX: oneTile.X, MinY: oneTile.Y, MaxY: oneTile.Y, Zoom: oneTile.Z}

// manyTiles is a 1000×600 tile area at zoom level 12, so its estimate is 600,000 tiles.
var manyTiles = tilecalc.TileBounds{MinX: 0, MaxX: 999, MinY: 1000, MaxY: 1599, Zoom: 12}

func TestDownloadTileCap(t *testing.T) {
	tests := []struct {
		name     string
		area     tilecalc.TileBounds
		args     []string
		exitCode int
		requests int
	}{
		{name: "refuses a 600,000-tile job at the default cap with exit code 4", area: manyTiles, exitCode: 4},
		{name: "refuses a 4-tile job above --max-tiles 3 with exit code 4", area: fourTiles, args: []string{"--max-tiles", "3"}, exitCode: 4},
		{name: "starts a 4-tile job at --max-tiles 4", area: fourTiles, args: []string{"--max-tiles", "4"}, exitCode: 0, requests: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			output := filepath.Join(t.TempDir(), "out.sqlite")

			run := runDownloadOf(t.Context(), t, srv, tt.area, output, tt.args...)
			if got := ExitCode(run.err); got != tt.exitCode {
				t.Errorf("ExitCode(%v) = %d, want %d", run.err, got, tt.exitCode)
			}
			if n := srv.TotalRequests(); n != tt.requests {
				t.Errorf("server got %d tile requests, want %d", n, tt.requests)
			}
			if tt.exitCode == 0 {
				return
			}
			if !strings.Contains(run.stderr, "too_many_tiles: ") {
				t.Errorf("stderr = %q, want it to hold too_many_tiles", run.stderr)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("output file stat error = %v, want os.ErrNotExist", err)
			}
		})
	}
}

func TestDownloadExitCodeAndFinalLine(t *testing.T) {
	missing := testtiles.Table{oneTile: {Kind: testtiles.NotFound}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name       string
		ctx        context.Context
		table      testtiles.Table
		exitCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "exits 0 and prints the summary line when the job succeeds",
			ctx:        context.Background(),
			exitCode:   0,
			wantStdout: "Done: 1 downloaded, 0 skipped, 0 failed in 0s -> ",
		},
		{
			name:       "exits 5 when the job fails above the failure tolerance",
			ctx:        context.Background(),
			table:      missing,
			exitCode:   5,
			wantStdout: "Done: 0 downloaded, 0 skipped, 1 failed in 0s -> ",
			wantStderr: "failure rate 100.0% exceeds threshold 20.0%",
		},
		{
			name:       "exits 130 and prints the stored count when the job is cancelled",
			ctx:        cancelled,
			exitCode:   130,
			wantStdout: "Cancelled. 0 of 1 tiles stored. Run again with --resume.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, tt.table)
			output := filepath.Join(t.TempDir(), "out.sqlite")

			run := runDownloadOf(tt.ctx, t, srv, oneTileArea, output)
			if got := ExitCode(run.err); got != tt.exitCode {
				t.Errorf("ExitCode(%v) = %d, want %d", run.err, got, tt.exitCode)
			}
			if !strings.Contains(run.stdout, tt.wantStdout) {
				t.Errorf("stdout = %q, want it to hold %q", run.stdout, tt.wantStdout)
			}
			if !strings.Contains(run.stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to hold %q", run.stderr, tt.wantStderr)
			}
		})
	}
}

func TestProgressLineHasTheFormOfFeature07(t *testing.T) {
	// 43,666 tiles at 12.1 tiles per second take 3608.76 s, so the line shows the rate of the example.
	p := download.Progress{
		TotalTiles: 120000,
		Downloaded: 43666,
		Skipped:    1200,
		Failed:     14,
		StartTime:  time.Now().Add(-3608760 * time.Millisecond),
	}
	want := "37.4% (44,880/120,000) - 12.1 tiles/s - 1,200 skipped - 14 failed - ETA 1h43m"
	if got := progressLine(p); got != want {
		t.Errorf("progressLine() = %q, want %q", got, want)
	}
}

func TestSummaryLineHasTheFormOfFeature07(t *testing.T) {
	p := download.Progress{TotalTiles: 120000, Downloaded: 118640, Skipped: 1200, Failed: 160}
	elapsed := 2*time.Hour + 41*time.Minute
	want := "Done: 118,640 downloaded, 1,200 skipped, 160 failed in 2h41m -> downloads/OpenTopoMap_Mt_Hood_z8-15.sqlite"
	if got := summaryLine(p, elapsed, "downloads/OpenTopoMap_Mt_Hood_z8-15.sqlite"); got != want {
		t.Errorf("summaryLine() = %q, want %q", got, want)
	}
}

func TestFormatCount(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{120000, "120,000"},
		{1234567, "1,234,567"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatCount(tt.n); got != tt.want {
				t.Errorf("formatCount(%d) = %q, want %q", tt.n, got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{400 * time.Millisecond, "0s"},
		{5 * time.Second, "5s"},
		{65 * time.Second, "1m5s"},
		{time.Hour + 43*time.Minute + 28*time.Second, "1h43m"},
		{2*time.Hour + 41*time.Minute, "2h41m"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := formatDuration(tt.d); got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestJobAPIRefusesAJobAboveTheTileCap(t *testing.T) {
	s := newJobServer(t, nil)
	s.handler.maxTiles = 3

	checkError(t, s.request(t, http.MethodPost, "/api/jobs", jobBody(fourTiles)), http.StatusBadRequest, "too_many_tiles")
	if n := s.tiles.TotalRequests(); n != 0 {
		t.Errorf("tile server got %d requests, want 0", n)
	}
}

func TestTheTileCapDefaultIs500000(t *testing.T) {
	for _, cmd := range []*cobra.Command{NewDownloadCmd(testUserAgent), NewServeCmd(testUserAgent)} {
		flag := cmd.Flags().Lookup("max-tiles")
		if flag == nil || flag.DefValue != "500000" {
			t.Errorf("%s --max-tiles = %v, want a flag with the default 500000", cmd.Name(), flag)
		}
	}
	if got := NewWebHandler(nil, nil, "", testUserAgent).maxTiles; got != defaultMaxTiles || defaultMaxTiles != 500000 {
		t.Errorf("NewWebHandler maxTiles = %d, want 500000", got)
	}
}
