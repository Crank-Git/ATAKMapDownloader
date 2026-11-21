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
	root.AddCommand(NewDownloadCmd())
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
