// Package commands holds the cobra commands of the atak-dl CLI.
package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// NewDownloadCmd returns the download command, which writes the tiles of an area to an MBTiles file.
// Every tile request of the command sends userAgent.
func NewDownloadCmd(userAgent string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download tiles for an area to MBTiles file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDownload(cmd, userAgent)
		},
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
	cmd.Flags().Float64("rate-limit", 0, "Requests per second, below the policy rate of the source (default: the policy rate)")
	cmd.Flags().Bool("accept-terms", false, "Accept the terms of use of a restricted map source and start the job")
	cobra.CheckErr(cmd.MarkFlagRequired("source"))
	cobra.CheckErr(cmd.MarkFlagRequired("bbox"))
	return cmd
}

func runDownload(cmd *cobra.Command, userAgent string) error {
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
	if err := checkTerms(cmd, source); err != nil {
		return err
	}

	zoomLevels := zoomLevelsBetween(minZoom, maxZoom)

	output, _ := cmd.Flags().GetString("output")
	customName, _ := cmd.Flags().GetString("name")
	if output == "" {
		dlDir := download.DownloadsDir()
		fname := download.GenerateOutputFilename(sourceName, bounds, minZoom, maxZoom, customName)
		output = filepath.Join(dlDir, fname)
	}

	cfg, err := downloadConfig(cmd, source, concurrent, userAgent)
	if err != nil {
		return err
	}

	engine := download.NewEngine(cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// The engine calls the callback from every tile goroutine, so the mutex guards lastPercent.
	var mu sync.Mutex
	var lastPercent float64
	res, err := engine.DownloadTiles(ctx, source, bounds, zoomLevels, output, func(p download.Progress) bool {
		mu.Lock()
		defer mu.Unlock()
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
	if !res.OK {
		exitErr("Download failed - too many errors")
		return nil
	}
	fmt.Printf("Download complete. Saved to %s\n", output)
	return nil
}

// downloadConfig returns the engine configuration of the download command for source.
// It prints the TLS warning when --insecure is set, and it returns the error of setPoliteness.
func downloadConfig(cmd *cobra.Command, source *xmlparser.MapSource, concurrent int, userAgent string) (download.Config, error) {
	cfg := download.DefaultConfig()
	cfg.MaxConcurrent = concurrent
	if err := setPoliteness(cmd, source, userAgent, &cfg); err != nil {
		cmd.SilenceUsage = true
		return cfg, err
	}
	ignoreErrors, err := cmd.Flags().GetBool("ignore-errors")
	if err != nil {
		return cfg, fmt.Errorf("read --ignore-errors: %w", err)
	}
	cfg.IgnoreErrors = ignoreErrors
	insecure, err := cmd.Flags().GetBool("insecure")
	if err != nil {
		return cfg, fmt.Errorf("read --insecure: %w", err)
	}
	cfg.VerifyTLS = !insecure
	if insecure {
		// The job does not start without the warning, because FR-security-hardening-6 requires it.
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: TLS certificate verification is off for this job"); err != nil {
			return cfg, fmt.Errorf("print the TLS warning: %w", err)
		}
	}
	return cfg, nil
}

// setPoliteness sets the User-Agent, the policy entry of source and the --rate-limit value in cfg.
// A rate limit above the policy rate gives the policy rate and a note on stderr, because a rate limit only lowers the rate.
// It returns an invalid_rate_limit error for a negative rate limit.
func setPoliteness(cmd *cobra.Command, source *xmlparser.MapSource, userAgent string, cfg *download.Config) error {
	cfg.UserAgent = userAgent
	cfg.Policy = sourcePolicy(source)
	limit, err := cmd.Flags().GetFloat64("rate-limit")
	if err != nil {
		return fmt.Errorf("read --rate-limit: %w", err)
	}
	if limit < 0 {
		return fmt.Errorf("invalid_rate_limit: --rate-limit %g is below 0", limit)
	}
	rps := cfg.Policy.RequestsPerSecond
	if limit > rps {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "note: --rate-limit %g is above the policy rate %g of %s, so the job uses %g\n",
			limit, rps, cfg.Policy.Hosts(), rps); err != nil {
			return fmt.Errorf("print the rate limit note: %w", err)
		}
		limit = rps
	}
	cfg.RateLimit = limit
	return nil
}

// checkTerms returns an ExitError with exitTermsNotAccepted when source is restricted and cmd has no --accept-terms.
// When source is restricted and cmd has --accept-terms, it prints the terms warning to stderr (FR-polite-downloading-17, -18).
func checkTerms(cmd *cobra.Command, source *xmlparser.MapSource) error {
	acceptTerms, err := cmd.Flags().GetBool("accept-terms")
	if err != nil {
		return fmt.Errorf("read --accept-terms: %w", err)
	}
	entry, restricted := restrictedEntry(source)
	if !restricted {
		return nil
	}
	if !acceptTerms {
		// The refusal is a policy decision and not a flag error, so the usage text does not help the user.
		cmd.SilenceUsage = true
		return &ExitError{Code: exitTermsNotAccepted, Err: fmt.Errorf(
			"terms_not_accepted: %s (%s); add --accept-terms to accept the terms", entry.Reason, entry.ReferenceURL)}
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s terms restrict bulk download: %s (%s)\n",
		entry.Hosts(), entry.Reason, entry.ReferenceURL); err != nil {
		return fmt.Errorf("print the terms warning: %w", err)
	}
	return nil
}
