// Package download provides tile download functionality.
package download

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/policy"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"golang.org/x/time/rate"
)

// ErrServerRefused is the failure reason of a job that the tile server refuses (FR-polite-downloading-12).
var ErrServerRefused = errors.New("server_refused")

// ErrWriteFailed is the failure reason of a job whose output file write fails, for example on a full disk.
var ErrWriteFailed = errors.New("write_failed")

const (
	// maxRefusals is the number of consecutive refusals that stops a job (FR-polite-downloading-12).
	maxRefusals = 20
	// maxRetryAfter is the longest Retry-After wait that the engine accepts. A longer one stops the job.
	maxRetryAfter = 300 * time.Second
	// maxAttempts is the number of requests that the main pass sends for one tile.
	maxAttempts = 3
	// retryAttempts is the number of requests that the retry pass sends for one tile (feature 07 behavior rules).
	retryAttempts = 1
)

// Config holds download engine configuration.
type Config struct {
	MaxConcurrent  int
	Timeout        time.Duration
	MaxFailureRate float64
	IgnoreErrors   bool
	// VerifyTLS makes the engine reject a tile server whose TLS certificate does not verify.
	// A tile that fails verification counts as failed.
	VerifyTLS bool
	// UserAgent is the User-Agent header of every tile request (FR-polite-downloading-14).
	UserAgent string
	// Policy is the policy entry of the map source. The gate of the entry limits every request.
	// A zero Policy makes the engine read the entry with policy.ForSource.
	Policy policy.Entry
	// RateLimit lowers the request rate of the job, in requests per second.
	// Zero, or a value above the policy rate, keeps the policy rate.
	RateLimit float64
}

// DefaultConfig returns default download config.
func DefaultConfig() Config {
	return Config{
		MaxConcurrent:  5,
		Timeout:        30 * time.Second,
		MaxFailureRate: 0.2,
		IgnoreErrors:   false,
		VerifyTLS:      true,
	}
}

// Progress is a copy of the progress counters of a job at one time.
// The engine updates the counters with atomic operations, and each callback gets a new copy (FR-robust-downloads-4).
type Progress struct {
	TotalTiles int
	Downloaded int64
	// Skipped counts the tiles that the output file already holds.
	Skipped   int64
	Failed    int64
	StartTime time.Time
}

// ProgressPercent returns the share of processed tiles from 0 to 100 (FR-robust-downloads-28).
// A failed tile counts as processed, so a job with failed tiles reaches 100.
// The retry pass can store a failed tile of an earlier job outside the bounds, so the percent stops at 100.
func (p Progress) ProgressPercent() float64 {
	if p.TotalTiles == 0 {
		return 0
	}
	return min(float64(p.Processed())/float64(p.TotalTiles)*100, 100)
}

// Processed returns the number of downloaded, skipped and failed tiles.
func (p Progress) Processed() int64 {
	return p.Downloaded + p.Skipped + p.Failed
}

// Elapsed returns elapsed time.
func (p Progress) Elapsed() time.Duration {
	return time.Since(p.StartTime)
}

// TilesPerSecond returns download rate.
func (p Progress) TilesPerSecond() float64 {
	elapsed := p.Elapsed().Seconds()
	if elapsed == 0 {
		return 0
	}
	return float64(p.Downloaded) / elapsed
}

// ETASeconds estimates remaining time.
func (p Progress) ETASeconds() float64 {
	rate := p.TilesPerSecond()
	if rate == 0 {
		return 0
	}
	// A retried tile from outside the bounds can make Processed larger than TotalTiles.
	remaining := max(p.TotalTiles-int(p.Processed()), 0)
	return float64(remaining) / rate
}

// ProgressCallback is called with progress updates. Return false to cancel.
type ProgressCallback func(Progress) bool

// Result is the result of one job.
type Result struct {
	// OK is true when the job ends within its failure tolerance.
	OK bool
	// Progress holds the counters of the job when it ends.
	Progress
}

// Engine downloads map tiles.
type Engine struct {
	cfg    Config
	calc   *tilecalc.TileCalculator
	client *http.Client
	gates  *gateRegistry
	// backoffBase is the first wait of the backoff schedule. A test makes it short.
	backoffBase time.Duration
	// blocked is the set of SHA-256 hashes of the tiles that the engine rejects. A test adds a fixture hash.
	blocked map[[sha256.Size]byte]struct{}
	// onTileFailed gets each failed tile and its reason. A test sets it, and it is nil in production.
	onTileFailed func(z, x, y int, reason Reason)
}

// NewEngine creates a new download engine.
func NewEngine(cfg Config) *Engine {
	tlsConfig := &tls.Config{}
	if !cfg.VerifyTLS {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // The user turns verification off for one job, and the CLI warns about it.
	}
	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		MaxIdleConns:    cfg.MaxConcurrent * 2,
	}
	client := &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
	}
	return &Engine{
		cfg:         cfg,
		calc:        tilecalc.NewTileCalculator(),
		client:      client,
		gates:       hostGates,
		backoffBase: time.Second,
		blocked:     blockedHashes,
	}
}

// DownloadTiles downloads the tiles of bounds at zoomLevels, and one writer goroutine writes them to outputPath.
// It opens an existing output file and skips each tile that the file holds, so the caller decides whether a file may exist.
// After the main pass, a retry pass requests each tile in the atakdl_failed_tiles table of the file once more.
// The error is ErrServerRefused or ErrWriteFailed when either stops the job.
// It is the context error when the job is cancelled. Every outcome keeps the output file.
func (e *Engine) DownloadTiles(
	ctx context.Context,
	source *xmlparser.MapSource,
	bounds *tilecalc.GeographicBounds,
	zoomLevels []int,
	outputPath string,
	cb ProgressCallback,
) (Result, error) {
	var totalTiles int
	var tileBoundsList []*tilecalc.TileBounds

	for _, z := range zoomLevels {
		tb := e.calc.CalculateTileBounds(bounds, z)
		tileBoundsList = append(tileBoundsList, tb)
		totalTiles += tb.TileCount()
	}

	slog.Info("starting download", "tiles", totalTiles, "zoom_levels", len(zoomLevels))

	w, err := mbtiles.Open(outputPath, outputMetadata(source, bounds, zoomLevels))
	if err != nil {
		return Result{}, err
	}

	c := tileCounters{total: totalTiles, start: time.Now()}

	jobCtx, jg := e.newJobGate(ctx, source)
	defer jg.stop(nil)

	sem := make(chan struct{}, e.cfg.MaxConcurrent)
	var wg sync.WaitGroup

	for _, tb := range tileBoundsList {
		// jobCtx ends when the job stops early, so the producer goroutine of IterTiles returns.
		for pair := range tb.IterTiles(jobCtx) {
			x, y := pair[0], pair[1]
			z := tb.Zoom

			select {
			case <-jobCtx.Done():
				// A refusal also ends jobCtx, but only a cancel of ctx is a cancelled job.
				if ctx.Err() != nil {
					c.cancelled.Store(true)
				}
				goto wait
			default:
			}

			// A full semaphore waits for a free slot, and a cancel ends that wait at once (FR-robust-downloads-30).
			select {
			case sem <- struct{}{}:
			case <-jobCtx.Done():
				goto wait
			}
			wg.Add(1)
			go func(x, y, z int) {
				defer wg.Done()
				defer func() { <-sem }()
				e.fetchTile(jobCtx, jg, w, source, cb, &c, x, y, z, false)
			}(x, y, z)
		}
	}

wait:
	wg.Wait()
	if jobCtx.Err() == nil && !c.cancelled.Load() {
		e.retryPass(jobCtx, jg, w, source, cb, &c)
	}

	// Close commits the last transaction, so the output file holds every tile that the job downloaded.
	closeErr := w.Close()
	progress := c.snapshot()
	if cb != nil {
		cb(progress)
	}

	slog.Info("download complete",
		"downloaded", progress.Downloaded,
		"skipped", progress.Skipped,
		"failed", progress.Failed,
		"total", totalTiles,
		"cancelled", c.cancelled.Load())

	ok, err := e.result(ctx, jobCtx, &c, closeErr)
	return Result{OK: ok, Progress: progress}, err
}

// result returns the result of a job after its tile goroutines end and its writer closes.
// A refusal stop or a write failure returns its cause.
// A cancel of ctx returns the context error, and a cancel from the callback returns context.Canceled.
func (e *Engine) result(ctx, jobCtx context.Context, c *tileCounters, closeErr error) (bool, error) {
	if cause := context.Cause(jobCtx); errors.Is(cause, ErrServerRefused) || errors.Is(cause, ErrWriteFailed) {
		return false, cause
	}
	if closeErr != nil {
		return false, fmt.Errorf("%w: %w", ErrWriteFailed, closeErr)
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if c.cancelled.Load() {
		return false, context.Canceled
	}
	return e.outcome(c.downloaded.Load(), c.skipped.Load(), c.failed.Load())
}

// outputMetadata returns the MBTiles metadata rows for a job.
func outputMetadata(source *xmlparser.MapSource, bounds *tilecalc.GeographicBounds, zoomLevels []int) map[string]string {
	return map[string]string{
		"name":        fmt.Sprintf("%s - %.4f,%.4f to %.4f,%.4f", source.Name, bounds.MinLat, bounds.MinLon, bounds.MaxLat, bounds.MaxLon),
		"type":        "baselayer",
		"version":     "1.0",
		"description": "Downloaded from " + source.Name,
		"format":      source.TileType,
		"bounds":      fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", bounds.MinLon, bounds.MinLat, bounds.MaxLon, bounds.MaxLat),
		"minzoom":     fmt.Sprintf("%d", zoomLevels[0]),
		"maxzoom":     fmt.Sprintf("%d", zoomLevels[len(zoomLevels)-1]),
		// The resume check of the CLI reads this row (FR-robust-downloads-10).
		"atakdl_source": source.Name,
	}
}

// outcome returns the result of a job that was not cancelled.
// It returns an error when no tile was processed or when the failure rate is above the limit.
// A job that skips every tile succeeds, because a resume of a complete output file is not an error.
func (e *Engine) outcome(downloaded, skipped, failed int64) (bool, error) {
	if e.cfg.IgnoreErrors {
		return true, nil
	}

	processed := downloaded + failed
	if processed == 0 && skipped > 0 {
		return true, nil
	}
	if processed == 0 {
		return false, fmt.Errorf("no tiles processed")
	}
	failureRate := float64(failed) / float64(processed)
	if failureRate > e.cfg.MaxFailureRate {
		return false, &FailureRateError{Rate: failureRate, Limit: e.cfg.MaxFailureRate}
	}

	return true, nil
}

// FailureRateError is the error of a job whose failure rate is above the failure tolerance.
// The CLI exits with code 5 for it.
type FailureRateError struct {
	// Rate is the failure rate of the job, from 0 to 1.
	Rate float64
	// Limit is the failure tolerance of the job, from 0 to 1.
	Limit float64
}

// Error returns the text that the web UI shows (feature 04).
func (e *FailureRateError) Error() string {
	return fmt.Sprintf("failure rate %.1f%% exceeds threshold %.1f%%", e.Rate*100, e.Limit*100)
}

// tileCounters holds the counts that the tile goroutines of one job share.
type tileCounters struct {
	total      int
	start      time.Time
	downloaded atomic.Int64
	skipped    atomic.Int64
	failed     atomic.Int64
	cancelled  atomic.Bool
	// placeholder counts the hashes of the first tiles of the job (FR-robust-downloads-20).
	placeholder placeholderCheck

	// failedTiles holds the tiles that failed in this job, so that a tile that fails in both passes counts once.
	mu          sync.Mutex
	failedTiles map[[3]int]struct{}
}

// countFailure adds the failed tile z, x, y to c, once per job.
func (c *tileCounters) countFailure(z, x, y int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := [3]int{z, x, y}
	if _, ok := c.failedTiles[k]; ok {
		return
	}
	if c.failedTiles == nil {
		c.failedTiles = make(map[[3]int]struct{})
	}
	c.failedTiles[k] = struct{}{}
	c.failed.Add(1)
}

// countDownload adds the downloaded tile z, x, y to c.
// A tile that failed earlier in the job moves from failed to downloaded, because the retry pass stored it.
func (c *tileCounters) countDownload(z, x, y int) {
	c.mu.Lock()
	k := [3]int{z, x, y}
	if _, ok := c.failedTiles[k]; ok {
		delete(c.failedTiles, k)
		c.failed.Add(-1)
	}
	c.mu.Unlock()
	c.downloaded.Add(1)
}

// snapshot returns a copy of the counters in c.
func (c *tileCounters) snapshot() Progress {
	return Progress{
		TotalTiles: c.total,
		Downloaded: c.downloaded.Load(),
		Skipped:    c.skipped.Load(),
		Failed:     c.failed.Load(),
		StartTime:  c.start,
	}
}

// fetchTile downloads one tile, sends it to w, and updates c.
// The main pass skips a tile that w holds (FR-robust-downloads-8).
// The retry pass sets retry, because a stale failed tile row can name a tile that w holds (FR-robust-downloads-24).
// A tile that the end of the job context ends counts as neither downloaded nor failed.
// It cancels the job when the callback returns false, and it stops the job when w fails.
func (e *Engine) fetchTile(
	ctx context.Context,
	jg *jobGate,
	w *mbtiles.Writer,
	source *xmlparser.MapSource,
	cb ProgressCallback,
	c *tileCounters,
	x, y, z int,
	retry bool,
) {
	if c.cancelled.Load() {
		return
	}
	if !retry && w.Has(z, x, y) {
		c.skipped.Add(1)
		report(cb, jg, c)
		return
	}

	// Every server part shares the gate of the policy entry, so the part changes the host but not the request rate.
	url := source.FormatURL(x, y, z, source.GetServerPart(x, y))
	attempts := maxAttempts
	if retry {
		attempts = retryAttempts
	}

	data, err := e.downloadTileWithRetry(ctx, jg, url, fmt.Sprintf("%d/%d/%d", z, x, y), attempts)
	if err != nil {
		reason := reasonOf(err)
		if reason == "" {
			// A cancel or a refusal stop ended the tile before it used its attempts, so a resume requests it again.
			return
		}
		c.countFailure(z, x, y)
		e.tileFailed(jg, w, z, x, y, reason)
		report(cb, jg, c)
		return
	}

	if c.placeholder.add(sha256.Sum256(data)) {
		slog.Warn("the map source may return a placeholder image",
			"source", source.Name, "tile", fmt.Sprintf("%d/%d/%d", z, x, y))
	}

	if err := w.Put(z, x, y, data); err != nil {
		slog.Error("write tile failed", "x", x, "y", y, "z", z, "err", err)
		c.countFailure(z, x, y)
		e.tileFailed(jg, w, z, x, y, ReasonWriteFailed)
		jg.stop(fmt.Errorf("%w: %w", ErrWriteFailed, err))
		return
	}

	c.countDownload(z, x, y)
	report(cb, jg, c)
}

// report sends a copy of c to cb.
// A false return cancels the job context, so the waits and requests of the other tiles end too (FR-robust-downloads-30).
func report(cb ProgressCallback, jg *jobGate, c *tileCounters) {
	if cb != nil && !cb(c.snapshot()) {
		c.cancelled.Store(true)
		jg.stop(context.Canceled)
	}
}

// tileFailed records one failed tile and its reason in w (FR-robust-downloads-21).
// It stops the job when w fails.
func (e *Engine) tileFailed(jg *jobGate, w *mbtiles.Writer, z, x, y int, reason Reason) {
	if e.onTileFailed != nil {
		e.onTileFailed(z, x, y, reason)
	}
	if err := w.Fail(z, x, y, string(reason)); err != nil {
		jg.stop(fmt.Errorf("%w: %w", ErrWriteFailed, err))
	}
}

// retryPass sends each tile in atakdl_failed_tiles through fetchTile one more time (FR-robust-downloads-22).
// It reads the whole table, so a resume also retries the failed tiles of an earlier job (FR-robust-downloads-24).
// It skips a not_found tile, because the server has no such tile (FR-robust-downloads-25).
func (e *Engine) retryPass(
	ctx context.Context,
	jg *jobGate,
	w *mbtiles.Writer,
	source *xmlparser.MapSource,
	cb ProgressCallback,
	c *tileCounters,
) {
	failed, err := w.Failed()
	if err != nil {
		jg.stop(fmt.Errorf("%w: %w", ErrWriteFailed, err))
		return
	}
	sem := make(chan struct{}, e.cfg.MaxConcurrent)
	var wg sync.WaitGroup
	for _, f := range failed {
		if Reason(f.Reason) == ReasonNotFound {
			continue
		}
		if c.cancelled.Load() {
			break
		}
		// A full semaphore waits for a free slot, and a cancel ends that wait at once (FR-robust-downloads-30).
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			e.fetchTile(ctx, jg, w, source, cb, c, f.X, f.Y, f.Z, true)
		}()
	}
	wg.Wait()
}

// jobGate holds the request limits of one job.
type jobGate struct {
	host *hostGate
	// limiter lowers the rate of the job below the policy rate. It is nil when the job keeps the policy rate.
	limiter *rate.Limiter
	// refusals counts the consecutive 403, 429 and 503 responses of the host.
	refusals atomic.Int64
	// stop ends the job context with the cause of the stop.
	stop context.CancelCauseFunc
}

// newJobGate returns the job context and the request limits of a job for source.
// The caller calls stop on the gate when the job ends.
func (e *Engine) newJobGate(ctx context.Context, source *xmlparser.MapSource) (context.Context, *jobGate) {
	entry := e.cfg.Policy
	if entry.RequestsPerSecond == 0 {
		entry = policy.ForSource(source)
	}
	jobCtx, stop := context.WithCancelCause(ctx)
	jg := &jobGate{host: e.gates.gate(entry.ID(), entry.RequestsPerSecond), stop: stop}
	if r := e.cfg.RateLimit; r > 0 && r < entry.RequestsPerSecond {
		jg.limiter = rate.NewLimiter(rate.Limit(r), 1)
	}
	return jobCtx, jg
}

// acquire returns when the job and the host let one request go.
// The caller calls jg.host.release after the response.
func (jg *jobGate) acquire(ctx context.Context) error {
	if jg.limiter != nil {
		if err := jg.limiter.Wait(ctx); err != nil {
			return fmt.Errorf("wait for the job rate limit: %w", err)
		}
	}
	return jg.host.acquire(ctx)
}

// tileResponse is the part of a tile response that the engine reads.
type tileResponse struct {
	data       []byte
	status     int
	retryAfter string
}

// downloadTileWithRetry returns the data of the tile at url, with at most attempts requests.
// The retry pass also calls it, so a tile from either pass gets the same validation.
// A failed tile returns a *TileError that holds the failure reason.
// It returns ErrServerRefused when a refusal stops the job, and the context error when the job ends.
func (e *Engine) downloadTileWithRetry(ctx context.Context, jg *jobGate, url, tile string, attempts int) ([]byte, error) {
	var lastErr error
	for attempt := range attempts {
		resp, err := e.downloadTile(ctx, jg, url)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			lastErr = networkError(err)
			if attempt < attempts-1 {
				// A network error says nothing about the host load, so the wait holds only this tile.
				if err := wait(ctx, backoffDelay(e.backoffBase, attempt+1, 0)); err != nil {
					return nil, err
				}
			}
		case resp.status == http.StatusOK:
			jg.refusals.Store(0)
			// The same body comes back on a retry, so the engine does not retry a rejected body.
			if reason := validateTile(resp.data, e.blocked); reason != "" {
				return nil, &TileError{Reason: reason}
			}
			return resp.data, nil
		case resp.status == http.StatusForbidden || resp.status == http.StatusTooManyRequests || resp.status == http.StatusServiceUnavailable:
			lastErr = &TileError{Reason: HTTPReason(resp.status)}
			if err := e.refuse(jg, resp, tile); err != nil {
				return nil, err
			}
		case resp.status == http.StatusNotFound:
			jg.refusals.Store(0)
			return nil, &TileError{Reason: ReasonNotFound}
		default:
			jg.refusals.Store(0)
			return nil, &TileError{Reason: HTTPReason(resp.status)}
		}
	}
	return nil, lastErr
}

// refuse counts the refusal in resp and pauses the host for the Retry-After value or the backoff wait.
// It stops the job and returns ErrServerRefused after maxRefusals refusals in a row,
// or when Retry-After asks for more than maxRetryAfter.
func (e *Engine) refuse(jg *jobGate, resp tileResponse, tile string) error {
	n := jg.refusals.Add(1)
	if n >= maxRefusals {
		err := fmt.Errorf("%w: the tile server refused %d tile requests in a row", ErrServerRefused, n)
		jg.stop(err)
		return err
	}
	// FR-polite-downloading-9 reads Retry-After only on 429 and 503.
	d, ok := time.Duration(0), false
	if resp.status != http.StatusForbidden {
		d, ok = parseRetryAfter(resp.retryAfter, time.Now())
	}
	if ok && d > maxRetryAfter {
		err := fmt.Errorf("%w: the tile server asks for a wait of %v, and the limit is %v", ErrServerRefused, d, maxRetryAfter)
		jg.stop(err)
		return err
	}
	if !ok {
		// The jitter only spreads the retries, so it needs no secure random source.
		d = backoffDelay(e.backoffBase, int(n), rand.Float64()*0.4-0.2) //nolint:gosec // See the comment above.
	}
	slog.Warn("tile server refused a request", "status", resp.status, "tile", tile, "wait", d, "refusals", n)
	jg.host.pause(d)
	return nil
}

// downloadTile sends one request for the tile at url when the job gate lets it go.
func (e *Engine) downloadTile(ctx context.Context, jg *jobGate, url string) (tileResponse, error) {
	if err := jg.acquire(ctx); err != nil {
		return tileResponse{}, err
	}
	defer jg.host.release()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return tileResponse{}, fmt.Errorf("make the tile request: %w", err)
	}
	req.Header.Set("User-Agent", e.cfg.UserAgent)

	resp, err := e.client.Do(req)
	if err != nil {
		return tileResponse{}, fmt.Errorf("send the tile request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // The body is fully read, so a close error loses nothing.

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return tileResponse{status: resp.StatusCode}, fmt.Errorf("read the tile response: %w", err)
	}
	return tileResponse{data: data, status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}, nil
}

// GenerateOutputFilename creates output path for downloaded tiles.
func GenerateOutputFilename(sourceName string, bounds *tilecalc.GeographicBounds, minZoom, maxZoom int, customName string) string {
	safe := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == ' ' || r == '-' || r == '_' {
				b.WriteRune(r)
			}
		}
		return strings.ReplaceAll(strings.TrimSpace(b.String()), " ", "_")
	}

	if customName != "" {
		return fmt.Sprintf("%s_z%d-%d.sqlite", safe(customName), minZoom, maxZoom)
	}
	safeName := safe(sourceName)
	return fmt.Sprintf("%s_%.4f_%.4f_to_%.4f_%.4f_z%d-%d.sqlite",
		safeName, bounds.MinLat, bounds.MinLon, bounds.MaxLat, bounds.MaxLon, minZoom, maxZoom)
}

// DownloadsDir returns the downloads directory path.
func DownloadsDir() string {
	return filepath.Join(".", "downloads")
}
