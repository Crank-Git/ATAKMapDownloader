// Package commands holds the cobra commands of the atak-dl CLI.
package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// The exit codes of feature 07 that the download command sets. Codes 2 and 3 are in terms.go and output.go.
const (
	// exitTooManyTiles is the exit code for a job above --max-tiles (FR-robust-downloads-26).
	exitTooManyTiles = 4
	// exitJobFailed is the exit code for a job that fails above its failure tolerance.
	exitJobFailed = 5
	// exitCancelled is the exit code for a cancelled job. It is 128 plus the number of SIGINT, as shells report it.
	exitCancelled = 130
)

// NewDownloadCmd returns the download command, which writes the tiles of an area to an output file.
// Every tile request of the command sends userAgent.
func NewDownloadCmd(userAgent string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download tiles for an area to an output file",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDownload(cmd, userAgent)
		},
	}
	cmd.Flags().String("source", "", "Map source name (required)")
	cmd.Flags().String("bbox", "", "Bounding box as min_lat,min_lon,max_lat,max_lon (required)")
	cmd.Flags().Int("min-zoom", 1, "Minimum zoom level")
	cmd.Flags().Int("max-zoom", 15, "Maximum zoom level")
	cmd.Flags().String("format", output.MBTiles, "Output format: "+strings.Join(output.Formats, ", "))
	cmd.Flags().String("output", "", "Output file path (default: downloads/<name> with the extension of --format)")
	cmd.Flags().String("name", "", "Custom filename (without extension)")
	cmd.Flags().Int("concurrent", 5, "Concurrent downloads")
	cmd.Flags().Bool("ignore-errors", false, "Continue despite failures")
	cmd.Flags().Bool("insecure", false, "Do not verify the TLS certificates of the tile server")
	cmd.Flags().Float64("rate-limit", 0, "Requests per second, below the policy rate of the source (default: the policy rate)")
	cmd.Flags().Bool("accept-terms", false, "Accept the terms of use of a restricted map source and start the job")
	cmd.Flags().Bool("resume", false, "Continue the job into the existing output file and skip the tiles it holds")
	cmd.Flags().Bool("overwrite", false, "Delete the existing output file before the job starts")
	cmd.Flags().Bool("force", false, "With --resume, accept an output file that names no map source (a v0.1 file)")
	cmd.Flags().Int("max-tiles", defaultMaxTiles, "Refuse a job above this tile count")
	cmd.MarkFlagsMutuallyExclusive("resume", "overwrite")
	cobra.CheckErr(cmd.MarkFlagRequired("source"))
	cobra.CheckErr(cmd.MarkFlagRequired("bbox"))
	return cmd
}

func runDownload(cmd *cobra.Command, userAgent string) error {
	parser := getParser(cmd)
	source := sourceFlag(cmd, parser)

	bboxStr, _ := cmd.Flags().GetString("bbox")
	bounds, _ := parseBBox(bboxStr)

	format, err := cmd.Flags().GetString("format")
	if err != nil {
		return fmt.Errorf("read --format: %w", err)
	}
	if code, message := checkFormat("--format", format); code != "" {
		return fmt.Errorf("%s: %s", code, message)
	}

	minZoom, _ := cmd.Flags().GetInt("min-zoom")
	maxZoom, _ := cmd.Flags().GetInt("max-zoom")
	concurrent, err := cmd.Flags().GetInt("concurrent")
	if err != nil {
		return fmt.Errorf("read --concurrent: %w", err)
	}
	maxTiles, err := cmd.Flags().GetInt("max-tiles")
	if err != nil {
		return fmt.Errorf("read --max-tiles: %w", err)
	}
	if code, message := checkJobLimits(source, bounds, minZoom, maxZoom, concurrent, maxTiles); code != "" {
		// The flags are valid in form, so the usage text does not help the user.
		cmd.SilenceUsage = true
		err := fmt.Errorf("%s: %s", code, message)
		if code == "too_many_tiles" {
			return &ExitError{Code: exitTooManyTiles, Err: err}
		}
		return err
	}
	if err := checkTerms(cmd, source); err != nil {
		return err
	}
	// Nobody has tested WebP tiles in ATAK, so the user gets the warning before the job starts.
	if strings.EqualFold(source.TileType, "webp") {
		if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "warning: ATAK support for WebP tiles is unverified"); err != nil {
			return fmt.Errorf("print the WebP warning: %w", err)
		}
	}

	zoomLevels := zoomLevelsBetween(minZoom, maxZoom)

	output, _ := cmd.Flags().GetString("output")
	customName, _ := cmd.Flags().GetString("name")
	if output == "" {
		dlDir := download.DownloadsDir()
		fname := download.GenerateOutputFilename(source.Name, bounds, minZoom, maxZoom, customName, format)
		output = filepath.Join(dlDir, fname)
	}
	cfg, err := downloadConfig(cmd, source, concurrent, userAgent)
	if err != nil {
		return err
	}
	cfg.Format = format
	// --overwrite deletes the file, so the check runs after every flag is valid.
	if err := checkOutput(cmd, output, source.Name, format); err != nil {
		return err
	}

	engine := download.NewEngine(cfg)
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	out := cmd.OutOrStdout()
	// The engine calls the callback from every tile goroutine, so the mutex guards lastPercent.
	var mu sync.Mutex
	var lastPercent float64
	res, err := engine.DownloadTiles(ctx, source, bounds, zoomLevels, output, func(p download.Progress) bool {
		mu.Lock()
		defer mu.Unlock()
		percent := p.ProgressPercent()
		if percent-lastPercent >= 1 || percent >= 100 {
			// The progress line is only for the user, so a failed write does not stop the job.
			_, _ = fmt.Fprintf(out, "\r%s   ", progressLine(p))
			lastPercent = percent
		}
		return true
	})
	return finishDownload(ctx, cmd, res, err, output)
}

// finishDownload prints the final line of a job and returns the error that sets the exit code of atak-dl.
// It returns an ExitError with exitCancelled for a cancelled job, and with exitJobFailed above the failure tolerance.
// It returns other job errors with exit code 1.
// A write failure outranks a cancel, because the output file lost tiles that the cancel line counts as stored.
func finishDownload(ctx context.Context, cmd *cobra.Command, res download.Result, err error, output string) error {
	out := cmd.OutOrStdout()
	// The final line replaces the progress line, so it starts on a new line.
	if _, perr := fmt.Fprintln(out); perr != nil {
		return fmt.Errorf("print the final line: %w", perr)
	}
	// The job error is not a flag error, so the usage text does not help the user.
	cmd.SilenceUsage = true
	if errors.Is(err, download.ErrWriteFailed) {
		return err
	}
	if ctx.Err() != nil {
		// The cancel line tells the user the result, so the error text adds nothing.
		cmd.SilenceErrors = true
		if _, perr := fmt.Fprintf(out, "Cancelled. %s of %s tiles stored. Run again with --resume.\n",
			formatCount(res.Downloaded+res.Skipped), formatCount(int64(res.TotalTiles))); perr != nil {
			return fmt.Errorf("print the cancel line: %w", perr)
		}
		return &ExitError{Code: exitCancelled, Err: ctx.Err()}
	}
	var rateErr *download.FailureRateError
	if err != nil && !errors.As(err, &rateErr) {
		return err
	}
	if _, perr := fmt.Fprintln(out, summaryLine(res.Progress, res.Elapsed(), output)); perr != nil {
		return fmt.Errorf("print the summary line: %w", perr)
	}
	if rateErr != nil {
		return &ExitError{Code: exitJobFailed, Err: err}
	}
	return nil
}

// checkOutput applies the --resume, --overwrite and --force flags of cmd to the output file at output of format.
// It returns an ExitError with exitOutputConflict when the flags do not accept the existing file.
func checkOutput(cmd *cobra.Command, output, sourceName, format string) error {
	rule := ifExistsFail
	for _, flag := range []struct {
		name string
		rule ifExists
	}{{"resume", ifExistsResume}, {"overwrite", ifExistsOverwrite}} {
		set, err := cmd.Flags().GetBool(flag.name)
		if err != nil {
			return fmt.Errorf("read --%s: %w", flag.name, err)
		}
		if set {
			rule = flag.rule
		}
	}
	force, err := cmd.Flags().GetBool("force")
	if err != nil {
		return fmt.Errorf("read --force: %w", err)
	}
	err = prepareOutput(output, rule, sourceName, format, force)
	var conflict *outputConflict
	if errors.As(err, &conflict) {
		// The refusal protects the file and is not a flag error, so the usage text does not help the user.
		cmd.SilenceUsage = true
		return &ExitError{Code: exitOutputConflict, Err: err}
	}
	return err
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
