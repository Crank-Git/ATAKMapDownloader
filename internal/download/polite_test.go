package download

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/policy"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"golang.org/x/time/rate"
)

// testUserAgent has the form of FR-polite-downloading-14.
const testUserAgent = "atak-map-downloader/test (+https://github.com/Crank-Git/ATAKMapDownloader)"

// wideArea is a 4×3 tile area at zoom level 10. At 8 requests per second, a job takes about 1.4 s.
var wideArea = tilecalc.TileBounds{MinX: 300, MaxX: 303, MinY: 400, MaxY: 402, Zoom: 10}

// politeEngine returns an engine with its own host gates, so no other test shares a pause or a rate.
// The policy entry of cfg gives the host the rate rps.
func politeEngine(cfg Config, rps float64) *Engine {
	cfg.Policy = policy.Entry{HostPatterns: []string{"127.0.0.1"}, PolicyClass: policy.Open, RequestsPerSecond: rps}
	cfg.UserAgent = testUserAgent
	e := NewEngine(cfg)
	e.gates = newGateRegistry()
	return e
}

// tableOf returns a table that gives resp to every tile of tb.
func tableOf(tb tilecalc.TileBounds, resp testtiles.Response) testtiles.Table {
	table := testtiles.Table{}
	for y := tb.MinY; y <= tb.MaxY; y++ {
		for x := tb.MinX; x <= tb.MaxX; x++ {
			table[testtiles.Tile{Z: tb.Zoom, X: x, Y: y}] = resp
		}
	}
	return table
}

// maxInWindow returns the highest number of requests in log that arrive within one window.
func maxInWindow(log []testtiles.Request, window time.Duration) int {
	most := 0
	for i := range log {
		n := 0
		for j := i; j < len(log) && log[j].Time.Sub(log[i].Time) <= window; j++ {
			n++
		}
		most = max(most, n)
	}
	return most
}

func TestEngineLimitsTheRequestRateOfAHost(t *testing.T) {
	tests := []struct {
		name      string
		policyRPS float64
		rateLimit float64
		want      int
	}{
		{"sends at most 9 requests in any 1 s window to an open host at 8 per second", 8, 0, 9},
		{"keeps the policy rate when the rate limit is above it", 8, 50, 9},
		{"sends at most 5 requests in any 1 s window when the rate limit lowers the rate to 4", 8, 4, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			source := loadFixture(t, "xyz.xml", srv)
			cfg := testConfig()
			cfg.RateLimit = tt.rateLimit
			output := filepath.Join(t.TempDir(), "out.sqlite")

			res, err := politeEngine(cfg, tt.policyRPS).DownloadTiles(t.Context(), source, boundsOf(t, wideArea), []int{wideArea.Zoom}, output, nil)
			if !res.OK || err != nil {
				t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
			}
			log := srv.Log()
			if len(log) != wideArea.TileCount() {
				t.Fatalf("server got %d requests, want %d", len(log), wideArea.TileCount())
			}
			if got := maxInWindow(log, time.Second); got > tt.want {
				t.Errorf("server got %d requests in one 1 s window, want at most %d", got, tt.want)
			}
		})
	}
}

func TestEngineWaitsForRetryAfterBeforeTheNextRequest(t *testing.T) {
	refused := testtiles.Tile{Z: oneTile.Zoom, X: oneTile.MinX, Y: oneTile.MinY}
	srv := testtiles.New(t, testtiles.Table{refused: {Kind: testtiles.TooManyRequests, RetryAfter: "2", Times: 1}})
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")

	res, err := politeEngine(testConfig(), 1000).DownloadTiles(t.Context(), source, boundsOf(t, oneTile), []int{oneTile.Zoom}, output, nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	log := srv.Log()
	if len(log) != 2 {
		t.Fatalf("server got %d requests, want 2: the 429 and the retry", len(log))
	}
	if gap := log[1].Time.Sub(log[0].Time); gap < 2*time.Second {
		t.Errorf("the retry came %v after the 429 response, want at least 2 s", gap)
	}
}

func TestEngineStopsTheJobWithServerRefused(t *testing.T) {
	tests := []struct {
		name  string
		table testtiles.Table
		// slots is the concurrency of the job. Zero keeps the default.
		slots        int
		wantRequests int
	}{
		{
			name:         "stops after 20 consecutive 503 responses",
			table:        tableOf(area, testtiles.Response{Kind: testtiles.Unavailable}),
			wantRequests: 20,
		},
		{
			name:  "stops at once when Retry-After is above 300 seconds",
			table: tableOf(area, testtiles.Response{Kind: testtiles.TooManyRequests, RetryAfter: "301"}),
			// With 5 slots, a second request can leave before the first 429 stops the job, so the case uses 1 slot.
			slots:        1,
			wantRequests: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, tt.table)
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")
			cfg := testConfig()
			if tt.slots > 0 {
				cfg.MaxConcurrent = tt.slots
			}
			e := politeEngine(cfg, 1000)
			// A short base keeps 20 backoff waits inside the test time.
			e.backoffBase = time.Millisecond

			res, err := e.DownloadTiles(t.Context(), source, areaBounds(t), []int{area.Zoom}, output, nil)
			if res.OK || !errors.Is(err, ErrServerRefused) || !strings.HasPrefix(errorText(err), "server_refused") {
				t.Errorf("DownloadTiles = %v, %v; want false and an error that starts with server_refused", res.OK, err)
			}
			if n := srv.TotalRequests(); n != tt.wantRequests {
				t.Errorf("server got %d requests, want %d", n, tt.wantRequests)
			}
		})
	}
}

func TestEngineStopsWithin500msWhenTheJobIsCancelledDuringABackoffWait(t *testing.T) {
	srv := testtiles.New(t, tableOf(area, testtiles.Response{Kind: testtiles.Unavailable}))
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := politeEngine(testConfig(), 1000).DownloadTiles(ctx, source, areaBounds(t), []int{area.Zoom}, output, nil)
		done <- err
	}()
	// The first 503 starts a backoff wait of at least 800 ms.
	for srv.TotalRequests() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	cancel()
	cancelled := time.Now()

	select {
	case err := <-done:
		if elapsed := time.Since(cancelled); elapsed > 500*time.Millisecond {
			t.Errorf("DownloadTiles returned %v after the cancel, want at most 500 ms", elapsed)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("DownloadTiles error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DownloadTiles did not return within 5 s of the cancel")
	}
}

func TestEngineSendsTheUserAgentOnEveryRequest(t *testing.T) {
	refused := testtiles.Tile{Z: area.Zoom, X: area.MinX, Y: area.MinY}
	srv := testtiles.New(t, testtiles.Table{refused: {Kind: testtiles.Unavailable, Times: 1}})
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	e := politeEngine(testConfig(), 1000)
	e.backoffBase = time.Millisecond

	if res, err := e.DownloadTiles(t.Context(), source, areaBounds(t), []int{area.Zoom}, output, nil); !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}
	log := srv.Log()
	// The 9 tiles and the retry of the refused tile give 10 requests.
	if len(log) != 10 {
		t.Errorf("server got %d requests, want 10", len(log))
	}
	for _, r := range log {
		if r.UserAgent != testUserAgent {
			t.Errorf("request for tile %v has User-Agent %q, want %q", r.Tile, r.UserAgent, testUserAgent)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"reads a number of seconds", "2", 2 * time.Second, true},
		{"reads an HTTP date as the time until that date", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		{"reads an HTTP date in the past as no wait", now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{"rejects an empty value", "", 0, false},
		{"rejects a negative number", "-1", 0, false},
		{"rejects a value that is not a number or a date", "soon", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.value, now)
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, %v", tt.value, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestBackoffDelay(t *testing.T) {
	tests := []struct {
		name     string
		refusals int
		jitter   float64
		want     time.Duration
	}{
		{"waits 1 s after the first refusal", 1, 0, time.Second},
		{"waits 2 s after the second refusal", 2, 0, 2 * time.Second},
		{"waits 4 s after the third refusal", 3, 0, 4 * time.Second},
		{"waits 8 s after the fourth refusal", 4, 0, 8 * time.Second},
		{"waits 8 s at most", 12, 0, 8 * time.Second},
		{"adds 20% for the highest jitter", 1, 0.2, 1200 * time.Millisecond},
		{"removes 20% for the lowest jitter", 2, -0.2, 1600 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoffDelay(time.Second, tt.refusals, tt.jitter); got != tt.want {
				t.Errorf("backoffDelay(1s, %d, %v) = %v, want %v", tt.refusals, tt.jitter, got, tt.want)
			}
		})
	}
}

func TestHostGateAllowsAtMost4RequestsAtOnce(t *testing.T) {
	g := newHostGate(rate.Inf)
	for i := range maxHostRequests {
		if err := g.acquire(t.Context()); err != nil {
			t.Fatalf("acquire %d returned %v, want nil", i+1, err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := g.acquire(ctx); err == nil {
		t.Fatal("acquire 5 returned nil while 4 requests are open, want an error")
	}
	g.release()
	if err := g.acquire(t.Context()); err != nil {
		t.Errorf("acquire after a release returned %v, want nil", err)
	}
}

func TestHostGatePauseHoldsEveryRequestToTheHost(t *testing.T) {
	g := newHostGate(rate.Inf)
	g.pause(200 * time.Millisecond)
	start := time.Now()
	if err := g.acquire(t.Context()); err != nil {
		t.Fatalf("acquire returned %v, want nil", err)
	}
	if waited := time.Since(start); waited < 200*time.Millisecond {
		t.Errorf("acquire returned after %v, want at least 200 ms", waited)
	}
}

func TestTwoSourcesOfOnePolicyEntryShareOneRate(t *testing.T) {
	srv := testtiles.New(t, nil)
	twoTiles := tilecalc.TileBounds{MinX: area.MinX, MaxX: area.MinX + 1, MinY: area.MinY, MaxY: area.MinY, Zoom: area.Zoom}
	// One registry stands for the process, as hostGates does for two web jobs.
	shared := newGateRegistry()
	errs := make(chan error, 2)
	// The two hosts belong to one operator, so the jobs share the 1 request per second of its entry.
	for _, host := range []string{"tile.openstreetmap.org", "a.tile.openstreetmap.org"} {
		cfg := testConfig()
		cfg.UserAgent = testUserAgent
		cfg.Policy = policy.Lookup(host)
		e := NewEngine(cfg)
		e.gates = shared
		source := loadFixture(t, "xyz.xml", srv)
		output := filepath.Join(t.TempDir(), "out.sqlite")
		go func() {
			res, err := e.DownloadTiles(t.Context(), source, boundsOf(t, twoTiles), []int{twoTiles.Zoom}, output, nil)
			if err == nil && !res.OK {
				err = errors.New("the job failed")
			}
			errs <- err
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	log := srv.Log()
	if len(log) != 2*twoTiles.TileCount() {
		t.Fatalf("server got %d requests, want %d", len(log), 2*twoTiles.TileCount())
	}
	// At 1 request per second, any 1.5 s window holds 2 requests. Two gates put 4 requests in the first window.
	if got := maxInWindow(log, 1500*time.Millisecond); got > 2 {
		t.Errorf("server got %d requests in one 1.5 s window, want at most 2 at 1 request per second", got)
	}
}
