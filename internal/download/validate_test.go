package download

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// tileFailure is one failed tile that the engine reports.
type tileFailure struct {
	tile   testtiles.Tile
	reason Reason
}

// failureLog collects the failed tiles that an engine reports from its tile goroutines.
type failureLog struct {
	mu   sync.Mutex
	list []tileFailure
}

// record adds one failed tile. The engine calls it from many goroutines.
func (l *failureLog) record(z, x, y int, reason Reason) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.list = append(l.list, tileFailure{testtiles.Tile{Z: z, X: x, Y: y}, reason})
}

// failures returns a copy of the failed tiles.
func (l *failureLog) failures() []tileFailure {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]tileFailure(nil), l.list...)
}

// singleTile is one tile at zoom level 10, so a job sends one tile request.
var singleTile = tilecalc.TileBounds{MinX: 300, MaxX: 300, MinY: 400, MaxY: 400, Zoom: 10}

func TestValidateTile(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want Reason
	}{
		{"accepts the PNG signature", []byte("\x89PNG\r\n\x1a\nrest"), ""},
		{"accepts the JPEG signature", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, ""},
		{"accepts the GIF signature", []byte("GIF89a rest"), ""},
		{"accepts the WebP signature", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), ""},
		{"rejects a body of 0 bytes with reason empty", nil, ReasonEmpty},
		{"rejects an HTML body with reason not_image", []byte("<!DOCTYPE html><html></html>"), ReasonNotImage},
		{"rejects a RIFF body that is not WebP with reason not_image", []byte("RIFF\x10\x00\x00\x00WAVEfmt "), ReasonNotImage},
		{"rejects a body shorter than the PNG signature with reason not_image", []byte("\x89PNG"), ReasonNotImage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := validateTile(tt.data, nil); got != tt.want {
				t.Errorf("validateTile = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateTileRejectsABlockedImage(t *testing.T) {
	// The fixture is a valid GIF signature, so only the hash match rejects it.
	fixture := []byte("GIF89a blocked placeholder fixture")
	blocked := map[[sha256.Size]byte]struct{}{sha256.Sum256(fixture): {}}

	if got := validateTile(fixture, blocked); got != ReasonBlockedImage {
		t.Errorf("validateTile = %q, want %q", got, ReasonBlockedImage)
	}
	if got := validateTile([]byte("GIF89a another image"), blocked); got != "" {
		t.Errorf("validateTile of an image that is not blocked = %q, want accept", got)
	}
}

func TestLoadBlocked(t *testing.T) {
	sum := sha256.Sum256([]byte("fixture"))
	hexSum := fmt.Sprintf("%x", sum)
	tests := []struct {
		name    string
		data    string
		want    int
		wantErr bool
	}{
		{"loads an empty list", `[]`, 0, false},
		{"loads one hash", `["` + hexSum + `"]`, 1, false},
		{"loads an upper-case hash", `["` + strings.ToUpper(hexSum) + `"]`, 1, false},
		{"rejects a hash that is too short", `["abcd"]`, 0, true},
		{"rejects a hash that is not hexadecimal", `["` + strings.Repeat("z", 64) + `"]`, 0, true},
		{"rejects a file that is not a JSON list", `{"hash": 1}`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadBlocked([]byte(tt.data))
			if (err != nil) != tt.wantErr {
				t.Fatalf("loadBlocked error = %v, want error %v", err, tt.wantErr)
			}
			if len(got) != tt.want {
				t.Errorf("loadBlocked holds %d hashes, want %d", len(got), tt.want)
			}
		})
	}
}

func TestTheEmbeddedBlockedFileLoads(t *testing.T) {
	if _, err := loadBlocked(blockedJSON); err != nil {
		t.Fatalf("blocked.json does not load: %v", err)
	}
}

func TestEngineRejectsTilesThatAreNotValidImages(t *testing.T) {
	tests := []struct {
		name string
		kind testtiles.Kind
		want Reason
	}{
		{"rejects an HTML body with status 200 with reason not_image", testtiles.HTML, ReasonNotImage},
		{"rejects bytes that are not an image with reason not_image", testtiles.Garbage, ReasonNotImage},
		{"rejects an empty body with reason empty", testtiles.Empty, ReasonEmpty},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
			srv := testtiles.New(t, testtiles.Table{tile: {Kind: tt.kind}})
			source := loadFixture(t, "xyz.xml", srv)
			output := filepath.Join(t.TempDir(), "out.sqlite")
			var log failureLog
			e := NewEngine(testConfig())
			e.onTileFailed = log.record

			res, err := e.DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, output, nil)
			if res.OK || err == nil {
				t.Errorf("DownloadTiles = %v, %v; want false and an error", res.OK, err)
			}
			if res.Failed != 1 || res.Downloaded != 0 {
				t.Errorf("DownloadTiles counts %d failed and %d downloaded, want 1 and 0", res.Failed, res.Downloaded)
			}
			if rows := len(storedTiles(t, output)); rows != 0 {
				t.Errorf("output file holds %d tile rows, want 0", rows)
			}
			want := []tileFailure{{tile, tt.want}}
			if got := log.failures(); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("failed tiles = %v, want %v", got, want)
			}
			// A body with status 200 is not a refusal, so the engine does not retry it.
			if n := srv.Requests(tile); n != 1 {
				t.Errorf("tile got %d requests, want 1", n)
			}
		})
	}
}

func TestEngineRejectsABlockedImage(t *testing.T) {
	tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	output := filepath.Join(t.TempDir(), "out.sqlite")
	var log failureLog
	e := NewEngine(testConfig())
	e.onTileFailed = log.record
	e.blocked = map[[sha256.Size]byte]struct{}{sha256.Sum256(fetchBody(t, srv, tile)): {}}

	res, _ := e.DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, output, nil) // The test checks the counts and the reason, not the job error.
	if res.Failed != 1 {
		t.Errorf("DownloadTiles counts %d failed, want 1", res.Failed)
	}
	if rows := len(storedTiles(t, output)); rows != 0 {
		t.Errorf("output file holds %d tile rows, want 0", rows)
	}
	want := []tileFailure{{tile, ReasonBlockedImage}}
	if got := log.failures(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("failed tiles = %v, want %v", got, want)
	}
}

func TestEngineReportsTheReasonOfAnHTTPError(t *testing.T) {
	tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
	srv := testtiles.New(t, testtiles.Table{tile: {Kind: testtiles.NotFound}})
	source := loadFixture(t, "xyz.xml", srv)
	var log failureLog
	e := NewEngine(testConfig())
	e.onTileFailed = log.record

	_, _ = e.DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, filepath.Join(t.TempDir(), "out.sqlite"), nil) // The test checks the reason, not the job error.
	want := []tileFailure{{tile, "http_404"}}
	if got := log.failures(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("failed tiles = %v, want %v", got, want)
	}
}

func TestEngineReportsTheReasonOfATLSFailure(t *testing.T) {
	tile := testtiles.Tile{Z: singleTile.Zoom, X: singleTile.MinX, Y: singleTile.MinY}
	srv := testtiles.NewTLS(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	var log failureLog
	e := NewEngine(testConfig())
	e.backoffBase = 0
	e.onTileFailed = log.record

	_, _ = e.DownloadTiles(t.Context(), source, boundsOf(t, singleTile), []int{singleTile.Zoom}, filepath.Join(t.TempDir(), "out.sqlite"), nil) // The test checks the reason, not the job error.
	want := []tileFailure{{tile, ReasonTLS}}
	if got := log.failures(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("failed tiles = %v, want %v", got, want)
	}
}

func TestHTTPReason(t *testing.T) {
	if got := HTTPReason(http.StatusInternalServerError); got != "http_500" {
		t.Errorf("HTTPReason(500) = %q, want %q", got, "http_500")
	}
}

func TestTileErrorUnwrapsItsCause(t *testing.T) {
	cause := errors.New("connection reset")
	err := error(&TileError{Reason: ReasonNetwork, Err: cause})
	if !errors.Is(err, cause) {
		t.Error("errors.Is(TileError, cause) = false, want true")
	}
	if got := reasonOf(fmt.Errorf("wrap: %w", err)); got != ReasonNetwork {
		t.Errorf("reasonOf = %q, want %q", got, ReasonNetwork)
	}
	if got := reasonOf(cause); got != "" {
		t.Errorf("reasonOf of a plain error = %q, want empty", got)
	}
}

func TestPlaceholderCheck(t *testing.T) {
	tests := []struct {
		name string
		// total is the number of tiles. same reports whether tile i has the shared hash.
		total int
		same  func(i int) bool
		want  int
	}{
		{"warns once when 51 of the first 100 tiles share one hash", 200, func(i int) bool { return i < 51 }, 1},
		{"does not warn when 50 of the first 100 tiles share one hash", 100, func(i int) bool { return i%2 == 0 }, 0},
		{"does not count the tiles after the first 100", 200, func(i int) bool { return i >= 100 }, 0},
	}
	shared := sha256.Sum256([]byte("placeholder"))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p placeholderCheck
			warnings := 0
			for i := range tt.total {
				sum := sha256.Sum256(fmt.Appendf(nil, "tile %d", i))
				if tt.same(i) {
					sum = shared
				}
				if p.add(sum) {
					warnings++
				}
			}
			if warnings != tt.want {
				t.Errorf("placeholderCheck warns %d times, want %d", warnings, tt.want)
			}
		})
	}
}

// lockedBuffer is a log destination that the tile goroutines share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestEngineWarnsWhenTheMapSourceReturnsOnePlaceholderImage(t *testing.T) {
	// The test server sends the same PNG for every tile, so 121 tiles share one hash.
	tb := tilecalc.TileBounds{MinX: 300, MaxX: 310, MinY: 400, MaxY: 410, Zoom: 10}
	var logs lockedBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	srv := testtiles.New(t, nil)
	source := loadFixture(t, "xyz.xml", srv)
	cfg := testConfig()
	cfg.MaxConcurrent = 8
	// The default rate of a local host gives a job of 121 tiles about 30 s, so the test raises it.
	res, err := politeEngine(cfg, 1000).DownloadTiles(t.Context(), source, boundsOf(t, tb), []int{tb.Zoom}, filepath.Join(t.TempDir(), "out.sqlite"), nil)
	if !res.OK || err != nil {
		t.Fatalf("DownloadTiles = %v, %v; want true, nil", res.OK, err)
	}

	const message = "the map source may return a placeholder image"
	if n := strings.Count(logs.String(), message); n != 1 {
		t.Errorf("log holds %d placeholder warnings, want 1. Log:\n%s", n, logs.String())
	}
}

// fetchBody returns the body that srv sends for tile.
func fetchBody(t *testing.T, srv *testtiles.Server, tile testtiles.Tile) []byte {
	t.Helper()
	url := fmt.Sprintf("%s/%d/%d/%d.png", srv.URL, tile.Z, tile.X, tile.Y)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // The URL comes from the test server.
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }() // The body is fully read, so a close error loses nothing.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}
