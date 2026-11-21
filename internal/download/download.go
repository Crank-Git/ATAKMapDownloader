// Package download provides tile download functionality.
package download

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
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

const (
	// maxRefusals is the number of consecutive refusals that stops a job (FR-polite-downloading-12).
	maxRefusals = 20
	// maxRetryAfter is the longest Retry-After wait that the engine accepts. A longer one stops the job.
	maxRetryAfter = 300 * time.Second
	// maxAttempts is the number of requests that the engine sends for one tile.
	maxAttempts = 3
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

// Progress reports download progress.
type Progress struct {
	TotalTiles  int
	Downloaded  int64
	Failed      int64
	CurrentTile [3]int
	CurrentURL  string
	StartTime   time.Time
}

// ProgressPercent returns 0-100.
func (p *Progress) ProgressPercent() float64 {
	if p.TotalTiles == 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&p.Downloaded)) / float64(p.TotalTiles) * 100
}

// Elapsed returns elapsed time.
func (p *Progress) Elapsed() time.Duration {
	return time.Since(p.StartTime)
}

// TilesPerSecond returns download rate.
func (p *Progress) TilesPerSecond() float64 {
	elapsed := p.Elapsed().Seconds()
	if elapsed == 0 {
		return 0
	}
	return float64(atomic.LoadInt64(&p.Downloaded)) / elapsed
}

// ETASeconds estimates remaining time.
func (p *Progress) ETASeconds() float64 {
	rate := p.TilesPerSecond()
	if rate == 0 {
		return 0
	}
	remaining := p.TotalTiles - int(atomic.LoadInt64(&p.Downloaded))
	return float64(remaining) / rate
}

// ProgressCallback is called with progress updates. Return false to cancel.
type ProgressCallback func(*Progress) bool

// Engine downloads map tiles.
type Engine struct {
	cfg    Config
	calc   *tilecalc.TileCalculator
	client *http.Client
	gates  *gateRegistry
	// backoffBase is the first wait of the backoff schedule. A test makes it short.
	backoffBase time.Duration
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
	}
}

// DownloadTiles downloads tiles and writes to MBTiles.
func (e *Engine) DownloadTiles(
	ctx context.Context,
	source *xmlparser.MapSource,
	bounds *tilecalc.GeographicBounds,
	zoomLevels []int,
	outputPath string,
	cb ProgressCallback,
) (success bool, err error) {
	var totalTiles int
	var tileBoundsList []*tilecalc.TileBounds

	for _, z := range zoomLevels {
		tb := e.calc.CalculateTileBounds(bounds, z)
		tileBoundsList = append(tileBoundsList, tb)
		totalTiles += tb.TileCount()
	}

	slog.Info("starting download", "tiles", totalTiles, "zoom_levels", len(zoomLevels))

	db, err := mbtiles.Create(outputPath, outputMetadata(source, bounds, zoomLevels))
	if err != nil {
		return false, err
	}
	defer db.Close() //nolint:errcheck // A close error does not change the result today (#25).

	progress := &Progress{
		TotalTiles: totalTiles,
		StartTime:  time.Now(),
	}

	jobCtx, jg := e.newJobGate(ctx, source)
	defer jg.stop(nil)

	sem := make(chan struct{}, e.cfg.MaxConcurrent)
	var wg sync.WaitGroup
	var c tileCounters

	for _, tb := range tileBoundsList {
		for pair := range tb.IterTiles() {
			x, y := pair[0], pair[1]
			z := tb.Zoom

			select {
			case <-jobCtx.Done():
				// A refusal also ends jobCtx, but only a cancel of ctx removes the output file.
				if ctx.Err() != nil {
					atomic.StoreInt32(&c.cancelled, 1)
				}
				goto wait
			default:
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(x, y, z int) {
				defer wg.Done()
				defer func() { <-sem }()
				e.fetchTile(jobCtx, jg, db, source, progress, cb, &c, x, y, z)
			}(x, y, z)
		}
	}

wait:
	wg.Wait()

	downloaded, failed := c.downloaded, c.failed
	progress.Downloaded = atomic.LoadInt64(&c.downloaded)
	progress.Failed = atomic.LoadInt64(&c.failed)
	if cb != nil {
		cb(progress)
	}

	slog.Info("download complete",
		"downloaded", downloaded,
		"failed", failed,
		"total", totalTiles,
		"cancelled", atomic.LoadInt32(&c.cancelled) == 1)

	return e.result(ctx, jobCtx, &c, outputPath)
}

// result returns the result of a job after its tile goroutines end.
// A refusal stop returns its ErrServerRefused cause and keeps the output file.
// A cancel removes the output file and returns the context error.
func (e *Engine) result(ctx, jobCtx context.Context, c *tileCounters, outputPath string) (bool, error) {
	if cause := context.Cause(jobCtx); errors.Is(cause, ErrServerRefused) {
		return false, cause
	}
	if atomic.LoadInt32(&c.cancelled) == 1 || ctx.Err() != nil {
		// A failed removal leaves a partial output file. #26 owns the output file rules.
		_ = os.Remove(outputPath)
		return false, ctx.Err()
	}
	return e.outcome(atomic.LoadInt64(&c.downloaded), atomic.LoadInt64(&c.failed))
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
	}
}

// outcome returns the result of a job that was not cancelled.
// It returns an error when no tile was processed or when the failure rate is above the limit.
func (e *Engine) outcome(downloaded, failed int64) (bool, error) {
	if e.cfg.IgnoreErrors {
		return true, nil
	}

	processed := downloaded + failed
	if processed == 0 {
		return false, fmt.Errorf("no tiles processed")
	}
	failureRate := float64(failed) / float64(processed)
	if failureRate > e.cfg.MaxFailureRate {
		return false, fmt.Errorf("failure rate %.1f%% exceeds threshold %.1f%%", failureRate*100, e.cfg.MaxFailureRate*100)
	}

	return true, nil
}

// tileCounters holds the counts that the tile goroutines of one job share.
type tileCounters struct {
	downloaded int64
	failed     int64
	cancelled  int32
}

// fetchTile downloads one tile, stores it in db, and updates c and progress.
// It sets c.cancelled when the callback returns false.
func (e *Engine) fetchTile(
	ctx context.Context,
	jg *jobGate,
	db *sql.DB,
	source *xmlparser.MapSource,
	progress *Progress,
	cb ProgressCallback,
	c *tileCounters,
	x, y, z int,
) {
	if atomic.LoadInt32(&c.cancelled) == 1 {
		return
	}

	serverPart := source.GetServerPart(int(atomic.LoadInt64(&c.downloaded)))
	url := source.FormatURL(x, y, z, serverPart)

	progress.CurrentTile = [3]int{x, y, z}
	progress.CurrentURL = url

	data, err := e.downloadTileWithRetry(ctx, jg, url, fmt.Sprintf("%d/%d/%d", z, x, y))
	if err != nil {
		atomic.AddInt64(&c.failed, 1)
		if cb != nil && !cb(progress) {
			atomic.StoreInt32(&c.cancelled, 1)
		}
		return
	}

	tmsY := (1 << z) - 1 - y
	if err := mbtiles.InsertTile(db, z, x, tmsY, data); err != nil {
		slog.Error("insert tile failed", "x", x, "y", y, "z", z, "err", err)
		atomic.AddInt64(&c.failed, 1)
		return
	}

	atomic.AddInt64(&c.downloaded, 1)
	progress.Downloaded = atomic.LoadInt64(&c.downloaded)
	progress.Failed = atomic.LoadInt64(&c.failed)
	if cb != nil && !cb(progress) {
		atomic.StoreInt32(&c.cancelled, 1)
	}
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

// downloadTileWithRetry returns the data of the tile at url, with at most maxAttempts requests.
// It returns ErrServerRefused when a refusal stops the job, and the context error when the job ends.
func (e *Engine) downloadTileWithRetry(ctx context.Context, jg *jobGate, url, tile string) ([]byte, error) {
	var lastErr error
	for attempt := range maxAttempts {
		resp, err := e.downloadTile(ctx, jg, url)
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			lastErr = err
			if attempt < maxAttempts-1 {
				// A network error says nothing about the host load, so the wait holds only this tile.
				if err := wait(ctx, backoffDelay(e.backoffBase, attempt+1, 0)); err != nil {
					return nil, err
				}
			}
		case resp.status == http.StatusOK:
			jg.refusals.Store(0)
			return resp.data, nil
		case resp.status == http.StatusForbidden || resp.status == http.StatusTooManyRequests || resp.status == http.StatusServiceUnavailable:
			lastErr = fmt.Errorf("HTTP %d", resp.status)
			if err := e.refuse(jg, resp, tile); err != nil {
				return nil, err
			}
		default:
			jg.refusals.Store(0)
			return nil, fmt.Errorf("HTTP %d", resp.status)
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
