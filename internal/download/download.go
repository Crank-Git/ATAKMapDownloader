// Package download provides tile download functionality.
package download

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// Config holds download engine configuration.
type Config struct {
	MaxConcurrent       int
	Timeout              time.Duration
	DelayBetweenBatches time.Duration
	MaxFailureRate      float64
	IgnoreErrors        bool
	VerifySSL           bool
}

// DefaultConfig returns default download config.
func DefaultConfig() Config {
	return Config{
		MaxConcurrent:       5,
		Timeout:              30 * time.Second,
		DelayBetweenBatches:  200 * time.Millisecond,
		MaxFailureRate:       0.2,
		IgnoreErrors:         false,
		VerifySSL:            false,
	}
}

// Progress reports download progress.
type Progress struct {
	TotalTiles     int
	Downloaded     int64
	Failed         int64
	CurrentTile    [3]int
	CurrentURL     string
	StartTime      time.Time
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
}

// NewEngine creates a new download engine.
func NewEngine(cfg Config) *Engine {
	tlsConfig := &tls.Config{}
	if !cfg.VerifySSL {
		tlsConfig.InsecureSkipVerify = true
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
		cfg:    cfg,
		calc:   tilecalc.NewTileCalculator(),
		client: client,
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

	metadata := map[string]string{
		"name":        fmt.Sprintf("%s - %.4f,%.4f to %.4f,%.4f", source.Name, bounds.MinLat, bounds.MinLon, bounds.MaxLat, bounds.MaxLon),
		"type":        "baselayer",
		"version":    "1.0",
		"description": "Downloaded from " + source.Name,
		"format":      source.TileType,
		"bounds":      fmt.Sprintf("%.4f,%.4f,%.4f,%.4f", bounds.MinLon, bounds.MinLat, bounds.MaxLon, bounds.MaxLat),
		"minzoom":     fmt.Sprintf("%d", zoomLevels[0]),
		"maxzoom":     fmt.Sprintf("%d", zoomLevels[len(zoomLevels)-1]),
	}

	db, err := mbtiles.Create(outputPath, metadata)
	if err != nil {
		return false, err
	}
	defer db.Close()

	progress := &Progress{
		TotalTiles: totalTiles,
		StartTime:  time.Now(),
	}

	sem := make(chan struct{}, e.cfg.MaxConcurrent)
	var wg sync.WaitGroup
	var cancelled int32
	var downloaded, failed int64

	for _, tb := range tileBoundsList {
		for pair := range tb.IterTiles() {
			x, y := pair[0], pair[1]
			z := tb.Zoom

			select {
			case <-ctx.Done():
				atomic.StoreInt32(&cancelled, 1)
				goto wait
			default:
			}

			wg.Add(1)
			sem <- struct{}{}
			go func(x, y, z int) {
				defer wg.Done()
				defer func() { <-sem }()

				if atomic.LoadInt32(&cancelled) == 1 {
					return
				}

				serverPart := source.GetServerPart(int(atomic.LoadInt64(&downloaded)))
				url := source.FormatURL(x, y, z, serverPart)

				progress.CurrentTile = [3]int{x, y, z}
				progress.CurrentURL = url

				data, err := e.downloadTileWithRetry(url, x, y, z)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					if cb != nil && !cb(progress) {
						atomic.StoreInt32(&cancelled, 1)
					}
					return
				}

				tmsY := (1 << z) - 1 - y
				if err := mbtiles.InsertTile(db, z, x, tmsY, data); err != nil {
					slog.Error("insert tile failed", "x", x, "y", y, "z", z, "err", err)
					atomic.AddInt64(&failed, 1)
					return
				}

				atomic.AddInt64(&downloaded, 1)
				progress.Downloaded = atomic.LoadInt64(&downloaded)
				progress.Failed = atomic.LoadInt64(&failed)
				if cb != nil && !cb(progress) {
					atomic.StoreInt32(&cancelled, 1)
				}
			}(x, y, z)
		}
	}

wait:
	wg.Wait()

	progress.Downloaded = atomic.LoadInt64(&downloaded)
	progress.Failed = atomic.LoadInt64(&failed)
	if cb != nil {
		cb(progress)
	}

	slog.Info("download complete",
		"downloaded", downloaded,
		"failed", failed,
		"total", totalTiles,
		"cancelled", atomic.LoadInt32(&cancelled) == 1)

	if atomic.LoadInt32(&cancelled) == 1 {
		os.Remove(outputPath)
		return false, ctx.Err()
	}

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

func (e *Engine) downloadTileWithRetry(url string, x, y, z int) ([]byte, error) {
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		data, status, err := e.downloadTile(url)
		if err != nil {
			if attempt < maxRetries-1 {
				time.Sleep(time.Duration(math.Pow(2, float64(attempt))) * time.Second)
				continue
			}
			return nil, err
		}
		if status == 200 {
			return data, nil
		}
		if status == 403 || status == 429 || status == 503 {
			if attempt < maxRetries-1 {
				delay := time.Duration(math.Pow(2, float64(attempt))) * time.Second
				slog.Warn("retrying after error", "status", status, "tile", fmt.Sprintf("%d/%d/%d", z, x, y), "attempt", attempt+1)
				time.Sleep(delay)
				continue
			}
		}
		return nil, fmt.Errorf("HTTP %d", status)
	}
	return nil, fmt.Errorf("max retries exceeded")
}

func (e *Engine) downloadTile(url string) ([]byte, int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "ATAK-Map-Downloader/1.0")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
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
