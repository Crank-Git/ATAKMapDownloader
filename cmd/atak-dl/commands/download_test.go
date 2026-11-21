package commands

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

// writeSource writes a map source file named "Test TLS" for srv into a new directory, and returns the directory.
func writeSource(t *testing.T, srv *testtiles.Server) string {
	t.Helper()
	dir := t.TempDir()
	xml := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<customMapSource>
    <name>Test TLS</name>
    <minZoom>0</minZoom>
    <maxZoom>18</maxZoom>
    <tileType>png</tileType>
    <url>%s</url>
</customMapSource>
`, srv.URLTemplate())
	if err := os.WriteFile(filepath.Join(dir, "tls.xml"), []byte(xml), 0o600); err != nil {
		t.Fatalf("write map source: %v", err)
	}
	return dir
}

// oneTileBBox returns a --bbox value that holds exactly one tile at zoom level 10.
func oneTileBBox() string {
	tile := tilecalc.TileBounds{MinX: 300, MaxX: 300, MinY: 400, MaxY: 400, Zoom: 10}
	edges := tilecalc.NewTileCalculator().TileBoundsToGeographic(&tile)
	// The tile edges belong to the next tile, so the bounds move inside them.
	const inset = 1e-6
	return fmt.Sprintf("%f,%f,%f,%f", edges.MinLat+inset, edges.MinLon+inset, edges.MaxLat-inset, edges.MaxLon-inset)
}

func TestDownloadWithInsecurePrintsTheWarningAndAcceptsASelfSignedCertificate(t *testing.T) {
	srv := testtiles.NewTLS(t, nil)
	output := filepath.Join(t.TempDir(), "out.sqlite")

	root := &cobra.Command{Use: "atak-dl"}
	root.PersistentFlags().String("mapsources-dir", "", "")
	root.AddCommand(NewDownloadCmd(testUserAgent))
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetArgs([]string{
		"--mapsources-dir", writeSource(t, srv),
		"download", "--insecure",
		"--source", "Test TLS",
		"--bbox", oneTileBBox(),
		"--min-zoom", "10", "--max-zoom", "10",
		"--concurrent", "1",
		"--output", output,
	})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute() returned %v", err)
	}
	const warning = "warning: TLS certificate verification is off for this job\n"
	if !strings.Contains(stderr.String(), warning) {
		t.Errorf("stderr = %q, want it to hold %q", stderr.String(), warning)
	}
	if n := srv.TotalRequests(); n != 1 {
		t.Errorf("server got %d tile requests, want 1", n)
	}
}

func TestDownloadRejectsAZoomRangeOrConcurrencyOutsideTheLimits(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code string
	}{
		{"rejects a max-zoom above the source maximum", []string{"--min-zoom", "10", "--max-zoom", "19"}, "zoom_out_of_range"},
		{"rejects a min-zoom above max-zoom", []string{"--min-zoom", "11", "--max-zoom", "10"}, "zoom_out_of_range"},
		{"rejects a concurrency of 0", []string{"--concurrent", "0"}, "invalid_concurrency"},
		{"rejects a concurrency of 33", []string{"--concurrent", "33"}, "invalid_concurrency"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			root := &cobra.Command{Use: "atak-dl"}
			root.PersistentFlags().String("mapsources-dir", "", "")
			root.AddCommand(NewDownloadCmd(testUserAgent))
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			args := []string{
				"--mapsources-dir", writeSource(t, srv),
				"download",
				"--source", "Test TLS",
				"--bbox", oneTileBBox(),
				"--min-zoom", "10", "--max-zoom", "10",
				"--output", filepath.Join(t.TempDir(), "out.sqlite"),
			}
			// A later flag value replaces an earlier one, so the case arguments come last.
			root.SetArgs(append(args, tt.args...))

			err := root.Execute()
			if err == nil || !strings.HasPrefix(err.Error(), tt.code+": ") {
				t.Fatalf("Execute() returned %v, want an error that starts with %q", err, tt.code+": ")
			}
			if !strings.Contains(stderr.String(), tt.code) {
				t.Errorf("stderr = %q, want it to hold %q", stderr.String(), tt.code)
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
		})
	}
}

func TestDownloadRateLimit(t *testing.T) {
	tests := []struct {
		name       string
		rateLimit  string
		wantErr    string
		wantStderr string
	}{
		{"accepts a rate limit below the policy rate", "1", "", ""},
		{"lowers a rate limit above the policy rate and prints a note", "100", "",
			"note: --rate-limit 100 is above the policy rate 4 of 127.0.0.1, so the job uses 4\n"},
		{"rejects a negative rate limit", "-1", "invalid_rate_limit: --rate-limit -1 is below 0", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			root := &cobra.Command{Use: "atak-dl"}
			root.PersistentFlags().String("mapsources-dir", "", "")
			root.AddCommand(NewDownloadCmd(testUserAgent))
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			root.SetArgs([]string{
				"--mapsources-dir", writeSource(t, srv),
				"download",
				"--source", "Test TLS",
				"--bbox", oneTileBBox(),
				"--min-zoom", "10", "--max-zoom", "10",
				"--concurrent", "1",
				"--rate-limit", tt.rateLimit,
				"--output", filepath.Join(t.TempDir(), "out.sqlite"),
			})

			err := root.Execute()
			if got := errorString(err); got != tt.wantErr {
				t.Fatalf("Execute() returned %q, want %q", got, tt.wantErr)
			}
			if tt.wantStderr != "" && !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to hold %q", stderr.String(), tt.wantStderr)
			}
			if tt.wantErr == "" {
				checkOneRequestWithTheUserAgent(t, srv)
			}
		})
	}
}

// errorString returns the message of err, or an empty string for nil.
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// checkOneRequestWithTheUserAgent reports an error when srv did not get exactly one request with testUserAgent.
func checkOneRequestWithTheUserAgent(t *testing.T, srv *testtiles.Server) {
	t.Helper()
	log := srv.Log()
	if len(log) != 1 {
		t.Fatalf("server got %d tile requests, want 1", len(log))
	}
	if log[0].UserAgent != testUserAgent {
		t.Errorf("tile request has User-Agent %q, want %q", log[0].UserAgent, testUserAgent)
	}
}
