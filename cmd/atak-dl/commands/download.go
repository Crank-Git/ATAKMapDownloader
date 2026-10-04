package commands

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"context"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

func NewDownloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download tiles for an area to MBTiles file",
		RunE:  runDownload,
	}
	cmd.Flags().String("source", "", "Map source name (required)")
	cmd.Flags().String("bbox", "", "Bounding box as min_lat,min_lon,max_lat,max_lon (required)")
	cmd.Flags().Int("min-zoom", 1, "Minimum zoom level")
	cmd.Flags().Int("max-zoom", 15, "Maximum zoom level")
	cmd.Flags().String("output", "", "Output file path (default: downloads/<name>.sqlite)")
	cmd.Flags().String("name", "", "Custom filename (without extension)")
	cmd.Flags().Int("concurrent", 5, "Concurrent downloads")
	cmd.Flags().Bool("ignore-errors", false, "Continue despite failures")
	cmd.MarkFlagRequired("source")
	cmd.MarkFlagRequired("bbox")
	return cmd
}

func runDownload(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	sourceName, _ := cmd.Flags().GetString("source")
	source := parser.GetSource(sourceName)
	if source == nil {
		exitErr("Source '" + sourceName + "' not found")
	}

	bboxStr, _ := cmd.Flags().GetString("bbox")
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		exitErr("Bounding box must have 4 values: min_lat,min_lon,max_lat,max_lon")
	}
	var coords [4]float64
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			exitErr("Invalid bbox value: " + p)
		}
		coords[i] = v
	}

	bounds, err := tilecalc.NewGeographicBounds(coords[0], coords[2], coords[1], coords[3])
	if err != nil {
		exitErr(err.Error())
	}

	minZoom, _ := cmd.Flags().GetInt("min-zoom")
	maxZoom, _ := cmd.Flags().GetInt("max-zoom")
	if minZoom > maxZoom {
		exitErr("min-zoom cannot be greater than max-zoom")
	}

	var zoomLevels []int
	for z := minZoom; z <= maxZoom; z++ {
		zoomLevels = append(zoomLevels, z)
	}

	output, _ := cmd.Flags().GetString("output")
	customName, _ := cmd.Flags().GetString("name")
	if output == "" {
		dlDir := download.DownloadsDir()
		os.MkdirAll(dlDir, 0755)
		fname := download.GenerateOutputFilename(sourceName, bounds, minZoom, maxZoom, customName)
		output = filepath.Join(dlDir, fname)
	}

	cfg := download.DefaultConfig()
	cfg.MaxConcurrent, _ = cmd.Flags().GetInt("concurrent")
	cfg.IgnoreErrors, _ = cmd.Flags().GetBool("ignore-errors")

	engine := download.NewEngine(cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var lastPercent float64
	success, err := engine.DownloadTiles(ctx, source, bounds, zoomLevels, output, func(p *download.Progress) bool {
		percent := p.ProgressPercent()
		if percent-lastPercent >= 1 || percent >= 100 {
			fmt.Printf("\r%.1f%% (%d/%d) - %.1f tiles/s - ETA: %.0fs   ",
				percent, p.Downloaded, p.TotalTiles, p.TilesPerSecond(), p.ETASeconds())
			lastPercent = percent
		}
		return true
	})

	fmt.Println()
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("Download cancelled.")
		} else {
			exitErr("Download failed: " + err.Error())
		}
		return err
	}
	if !success {
		exitErr("Download failed - too many errors")
		return nil
	}
	fmt.Printf("Download complete. Saved to %s\n", output)
	return nil
}
