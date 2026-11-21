// Package commands holds the cobra commands of the atak-dl CLI.
package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/spf13/cobra"
)

// NewDownloadCmd returns the download command, which writes the tiles of an area to an MBTiles file.
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
	cmd.Flags().Bool("insecure", false, "Do not verify the TLS certificates of the tile server")
	cobra.CheckErr(cmd.MarkFlagRequired("source"))
	cobra.CheckErr(cmd.MarkFlagRequired("bbox"))
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
	bounds, _ := parseBBox(bboxStr)

	minZoom, _ := cmd.Flags().GetInt("min-zoom")
	maxZoom, _ := cmd.Flags().GetInt("max-zoom")
	concurrent, err := cmd.Flags().GetInt("concurrent")
	if err != nil {
		return fmt.Errorf("read --concurrent: %w", err)
	}
	if code, message := checkJobLimits(source, minZoom, maxZoom, concurrent); code != "" {
		// The flags are valid in form, so the usage text does not help the user.
		cmd.SilenceUsage = true
		return fmt.Errorf("%s: %s", code, message)
	}

	zoomLevels := zoomLevelsBetween(minZoom, maxZoom)

	output, _ := cmd.Flags().GetString("output")
	customName, _ := cmd.Flags().GetString("name")
	if output == "" {
		dlDir := download.DownloadsDir()
		fname := download.GenerateOutputFilename(sourceName, bounds, minZoom, maxZoom, customName)
		output = filepath.Join(dlDir, fname)
	}

	cfg := download.DefaultConfig()
	cfg.MaxConcurrent = concurrent
	cfg.IgnoreErrors, _ = cmd.Flags().GetBool("ignore-errors")
	insecure, err := cmd.Flags().GetBool("insecure")
	if err != nil {
		return fmt.Errorf("read --insecure: %w", err)
	}
	cfg.VerifyTLS = !insecure
	if insecure {
		// The job does not start without the warning, because FR-security-hardening-6 requires it.
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: TLS certificate verification is off for this job"); err != nil {
			return fmt.Errorf("print the TLS warning: %w", err)
		}
	}

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
