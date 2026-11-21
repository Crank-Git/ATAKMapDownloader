package commands

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
)

func TestDownloadWithoutOutputWritesACenterOfThreeNumbers(t *testing.T) {
	srv := testtiles.New(t, nil)
	// The default output path is relative to the working directory.
	t.Chdir(t.TempDir())
	if stderr, err := runDownloadInto(t, srv, "", []string{"--name", "Mt Hood"}); err != nil {
		t.Fatalf("download error = %v, stderr %q", err, stderr)
	}

	db, err := sql.Open("sqlite", filepath.Join("downloads", "Mt_Hood_z10-10.mbtiles")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	defer func() { _ = db.Close() }() // The query below reports a failure of the file.
	var center string
	if err := db.QueryRowContext(t.Context(), "SELECT value FROM metadata WHERE name='center'").Scan(&center); err != nil {
		t.Fatalf("read center: %v", err)
	}
	parts := strings.Split(center, ",")
	if len(parts) != 3 {
		t.Fatalf("center = %q, want three comma-separated numbers", center)
	}
	for _, p := range parts {
		if _, err := strconv.ParseFloat(p, 64); err != nil {
			t.Errorf("center = %q: %q is not a number", center, p)
		}
	}
	if parts[2] != "10" {
		t.Errorf("center zoom level = %q, want the minimum zoom level 10", parts[2])
	}
}

func TestDownloadWarnsOnlyForAWebPTileType(t *testing.T) {
	const warning = "warning: ATAK support for WebP tiles is unverified"
	tests := []struct {
		tileType string
		warn     bool
	}{
		{"webp", true},
		{"WEBP", true},
		{"png", false},
	}
	for _, tt := range tests {
		t.Run(tt.tileType, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			dir := t.TempDir()
			xml := fmt.Sprintf(`<customMapSource><name>Test WebP</name><tileType>%s</tileType><url>%s</url></customMapSource>`,
				tt.tileType, srv.URLTemplate())
			if err := os.WriteFile(filepath.Join(dir, "webp.xml"), []byte(xml), 0o600); err != nil {
				t.Fatal(err)
			}
			root := &cobra.Command{Use: "atak-dl"}
			root.PersistentFlags().String("mapsources-dir", "", "")
			root.AddCommand(NewDownloadCmd(testUserAgent))
			var stderr bytes.Buffer
			root.SetErr(&stderr)
			root.SetOut(&bytes.Buffer{})
			root.SetArgs([]string{
				"--mapsources-dir", dir,
				"download", "--source", "Test WebP",
				"--bbox", oneTileBBox(),
				"--min-zoom", "10", "--max-zoom", "10",
				"--output", filepath.Join(t.TempDir(), "out.mbtiles"),
			})
			// The test server sends PNG tiles, so the job result can be a failure, and only the warning matters here.
			_ = root.Execute()
			if got := strings.Contains(stderr.String(), warning); got != tt.warn {
				t.Errorf("stderr = %q; holds %q = %v, want %v", stderr.String(), warning, got, tt.warn)
			}
		})
	}
}
