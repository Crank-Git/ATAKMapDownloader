package download

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// tenByTen is a 10×10 tile area at zoom level 10.
var tenByTen = tilecalc.TileBounds{MinX: 300, MaxX: 309, MinY: 400, MaxY: 409, Zoom: 10}

func TestEngineProgressReaches100PercentWhen10PercentOfTilesReturn404(t *testing.T) {
	table := testtiles.Table{}
	for x := tenByTen.MinX; x <= tenByTen.MaxX; x++ {
		table[testtiles.Tile{Z: tenByTen.Zoom, X: x, Y: tenByTen.MinY}] = testtiles.Response{Kind: testtiles.NotFound}
	}
	srv := testtiles.New(t, table)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")

	var mu sync.Mutex
	var last float64
	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, tenByTen), output,
		func(p Progress) bool {
			mu.Lock()
			defer mu.Unlock()
			last = p.ProgressPercent()
			return true
		})
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	if res.Downloaded != 90 || res.Failed != 10 {
		t.Errorf("result = %d downloaded, %d failed; want 90, 10", res.Downloaded, res.Failed)
	}
	if got := res.ProgressPercent(); got != 100 {
		t.Errorf("result progress = %v%%, want 100%%", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if last != 100 {
		t.Errorf("last callback progress = %v%%, want 100%%", last)
	}
}

func TestEngineReturnsAFailureRateErrorAboveTheFailureTolerance(t *testing.T) {
	srv := testtiles.New(t, tableOf(oneTile, testtiles.Response{Kind: testtiles.NotFound}))
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")

	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, oneTile), output, nil)
	var rateErr *FailureRateError
	if res.OK || !errors.As(err, &rateErr) {
		t.Fatalf("DownloadTiles = %v, %v; want false and a *FailureRateError", res.OK, err)
	}
	// The web UI shows this text, so the error type keeps it (feature 04).
	if want := "failure rate 100.0% exceeds threshold 20.0%"; err.Error() != want {
		t.Errorf("DownloadTiles error = %q, want %q", err.Error(), want)
	}
}

func TestProgressPercent(t *testing.T) {
	tests := []struct {
		name string
		p    Progress
		want float64
	}{
		{"counts downloaded, skipped and failed tiles", Progress{TotalTiles: 200, Downloaded: 50, Skipped: 30, Failed: 20}, 50},
		{"stops at 100 when the processed count is above the total", Progress{TotalTiles: 9, Downloaded: 10}, 100},
		{"returns 0 for a job with no tiles", Progress{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.p.ProgressPercent(); got != tt.want {
				t.Errorf("ProgressPercent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEngineProgressStaysAt100WhenTheRetryPassStoresATileOutsideTheBounds(t *testing.T) {
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	// An earlier job with other bounds left this failed tile row, and a resume retries the whole table.
	w, err := mbtiles.Open(output, map[string]string{"atakdl_source": source.Name})
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	if err := w.Fail(oneTile.Zoom, oneTile.MinX+50, oneTile.MinY+50, "http_503"); err != nil {
		t.Fatalf("store the failed tile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close output file: %v", err)
	}

	var mu sync.Mutex
	var highest float64
	res, err := politeEngine(testConfig(), 1e6).DownloadTiles(t.Context(), source, areasOf(t, oneTile), output,
		func(p Progress) bool {
			mu.Lock()
			defer mu.Unlock()
			highest = max(highest, p.ProgressPercent())
			return true
		})
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	if res.Downloaded != 2 || res.TotalTiles != 1 {
		t.Fatalf("result = %d downloaded of %d; want 2 of 1, the job tile and the retried tile", res.Downloaded, res.TotalTiles)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := res.ProgressPercent(); got != 100 || highest != 100 {
		t.Errorf("progress = %v%%, highest callback progress = %v%%; want 100%% for both", got, highest)
	}
}

func TestEngineStopsAllRequestsWithin500msOfACancel(t *testing.T) {
	tests := []struct {
		name string
		// callbackCancels makes the progress callback return false, in place of a cancel of the job context.
		callbackCancels bool
	}{
		{name: "stops after a cancel of the job context"},
		{name: "stops after the progress callback returns false", callbackCancels: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Every 503 starts a backoff wait, so the tiles in flight keep a retry open when the cancel arrives.
			table := tableOf(largeArea, testtiles.Response{Kind: testtiles.Unavailable})
			// The first tile succeeds at once, so the callback runs while the other tiles wait.
			delete(table, testtiles.Tile{Z: largeArea.Zoom, X: largeArea.MinX, Y: largeArea.MinY})
			srv := testtiles.New(t, table)
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")
			e := politeEngine(testConfig(), 1e6)
			e.backoffBase = 100 * time.Millisecond
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			var stop cancelTime
			cb := func(Progress) bool {
				if tt.callbackCancels {
					stop.mark()
				}
				return !tt.callbackCancels
			}
			done := make(chan error, 1)
			go func() {
				_, err := e.DownloadTiles(ctx, source, areasOf(t, largeArea), output, cb)
				done <- err
			}()
			if !tt.callbackCancels {
				for srv.TotalRequests() < 3 {
					time.Sleep(5 * time.Millisecond)
				}
				stop.mark()
				cancel()
			}
			checkStopWithin500ms(t, srv, done, &stop)
		})
	}
}

// cancelTime records the time of the first cancel. The callback runs in many goroutines, so a mutex guards it.
type cancelTime struct {
	mu sync.Mutex
	at time.Time
}

func (c *cancelTime) mark() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() {
		c.at = time.Now()
	}
}

func (c *cancelTime) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

// checkStopWithin500ms checks that the job that sends its error to done returns within 500 ms of the cancel in c with context.Canceled.
// It also checks that srv gets no request later than 500 ms after the cancel.
func checkStopWithin500ms(t *testing.T, srv *testtiles.Server, done <-chan error, c *cancelTime) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("DownloadTiles error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DownloadTiles did not return within 5 s of the cancel")
	}
	// The callback marks the cancel inside the job, so the check reads the time after the job returns.
	stop := c.get()
	if elapsed := time.Since(stop); elapsed > 500*time.Millisecond {
		t.Errorf("DownloadTiles returned %v after the cancel, want at most 500 ms", elapsed)
	}
	// A request that the engine sends late arrives after the return, so the check waits before it reads the log.
	time.Sleep(300 * time.Millisecond)
	for _, r := range srv.Log() {
		if late := r.Time.Sub(stop); late > 500*time.Millisecond {
			t.Errorf("server got a request for tile %v %v after the cancel, want none after 500 ms", r.Tile, late)
		}
	}
}
