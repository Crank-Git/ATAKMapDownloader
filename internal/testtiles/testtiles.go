// Package testtiles provides an httptest tile server for tests.
// The server returns a configured response for each tile and counts the requests per tile.
package testtiles

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Kind selects the response that the server sends for a tile.
type Kind int

// The response kinds that FR-foundation-10 names.
const (
	// PNG sends a 256×256 PNG with status 200.
	PNG Kind = iota
	// JPEG sends a 256×256 JPEG with status 200.
	JPEG
	// NotFound sends status 404.
	NotFound
	// TooManyRequests sends status 429 with a Retry-After header.
	TooManyRequests
	// Unavailable sends status 503.
	Unavailable
	// HTML sends an HTML page with status 200.
	HTML
)

// Response is the configured response for one tile.
type Response struct {
	Kind Kind
	// RetryAfter is the Retry-After value for TooManyRequests. An empty value sends "1".
	RetryAfter string
	// Delay is the time that the server waits before it sends the response.
	Delay time.Duration
	// Times is the number of requests for the tile that get this response.
	// Later requests get a PNG. Zero gives this response to every request.
	Times int
}

// Tile identifies one tile by zoom level, column and XYZ row.
type Tile struct {
	Z, X, Y int
}

// Table maps a tile to its response. A tile that the table omits gets a PNG.
type Table map[Tile]Response

// Request is one tile request that the server received.
type Request struct {
	Tile Tile
	// Time is the time at which the server received the request.
	Time      time.Time
	UserAgent string
}

// Server is a test tile server.
type Server struct {
	// URL is the base URL of the server, with no trailing slash.
	URL string

	table Table
	png   []byte
	jpeg  []byte

	mu       sync.Mutex
	requests map[Tile]int
	log      []Request
}

// New starts a tile server that sends the responses in table.
// It stops the server when the test ends.
//
// The server serves two path forms:
//   - /{z}/{x}/{y} with an optional extension
//   - /q/{quadkey} with an optional extension
//
// A path that names no tile gets status 400.
func New(t testing.TB, table Table) *Server {
	t.Helper()
	return start(t, table, false)
}

// NewTLS starts a tile server like New, but it serves HTTPS with a self-signed certificate.
// A client that verifies TLS certificates rejects the server, because no trusted authority signed the certificate.
func NewTLS(t testing.TB, table Table) *Server {
	t.Helper()
	return start(t, table, true)
}

// start starts the tile server for New and NewTLS, and stops it when the test ends.
func start(t testing.TB, table Table, useTLS bool) *Server {
	t.Helper()
	s := &Server{
		table:    table,
		png:      encode(t, PNG),
		jpeg:     encode(t, JPEG),
		requests: make(map[Tile]int),
	}
	// NewTestServer uses an in-memory network, and a client with its own transport cannot reach it.
	srv := httptest.NewUnstartedServer(http.HandlerFunc(s.serveTile))
	if useTLS {
		// A rejected handshake is the expected result of a TLS test, so the server does not log it.
		srv.Config.ErrorLog = log.New(io.Discard, "", 0)
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	return s
}

// URLTemplate returns a map source URL template that points at the server.
func (s *Server) URLTemplate() string {
	return s.URL + "/{$z}/{$x}/{$y}.png"
}

// Requests returns the number of requests that the server received for tile.
func (s *Server) Requests(tile Tile) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[tile]
}

// TotalRequests returns the number of tile requests that the server received.
func (s *Server) TotalRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.requests {
		total += n
	}
	return total
}

// Log returns the tile requests that the server received, in the order of arrival.
func (s *Server) Log() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// Quadkey returns the Bing Maps quadkey of tile.
func Quadkey(tile Tile) string {
	var b strings.Builder
	for i := tile.Z; i > 0; i-- {
		mask := 1 << (i - 1)
		digit := 0
		if tile.X&mask != 0 {
			digit++
		}
		if tile.Y&mask != 0 {
			digit += 2
		}
		b.WriteByte(byte('0' + digit))
	}
	return b.String()
}

func (s *Server) serveTile(w http.ResponseWriter, r *http.Request) {
	tile, ok := parsePath(r.URL.Path)
	if !ok {
		http.Error(w, "the path names no tile", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	s.requests[tile]++
	n := s.requests[tile]
	s.log = append(s.log, Request{Tile: tile, Time: time.Now(), UserAgent: r.UserAgent()})
	s.mu.Unlock()

	resp := s.table[tile]
	if resp.Times > 0 && n > resp.Times {
		resp = Response{Kind: PNG}
	}
	if resp.Delay > 0 {
		// The select stops the wait when the client leaves, so Close does not block on it.
		select {
		case <-time.After(resp.Delay):
		case <-r.Context().Done():
			return
		}
	}

	switch resp.Kind {
	case PNG:
		writeBody(w, "image/png", s.png)
	case JPEG:
		writeBody(w, "image/jpeg", s.jpeg)
	case NotFound:
		http.Error(w, "no tile", http.StatusNotFound)
	case TooManyRequests:
		retryAfter := resp.RetryAfter
		if retryAfter == "" {
			retryAfter = "1"
		}
		w.Header().Set("Retry-After", retryAfter)
		http.Error(w, "too many requests", http.StatusTooManyRequests)
	case Unavailable:
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	case HTML:
		writeBody(w, "text/html; charset=utf-8", []byte("<!DOCTYPE html><html><body>Access denied</body></html>"))
	}
}

// writeBody sends body with status 200.
func writeBody(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	// A write error means that the client left, and the test that owns the client reports it.
	_, _ = w.Write(body)
}

// parsePath returns the tile that an XYZ path or a quadkey path names.
func parsePath(path string) (Tile, bool) {
	path = strings.TrimPrefix(path, "/")
	if dot := strings.LastIndexByte(path, '.'); dot >= 0 {
		path = path[:dot]
	}
	parts := strings.Split(path, "/")
	switch {
	case len(parts) == 2 && parts[0] == "q":
		return parseQuadkey(parts[1])
	case len(parts) == 3:
		var n [3]int
		for i, p := range parts {
			v, err := strconv.Atoi(p)
			if err != nil || v < 0 {
				return Tile{}, false
			}
			n[i] = v
		}
		return Tile{Z: n[0], X: n[1], Y: n[2]}, true
	}
	return Tile{}, false
}

// parseQuadkey returns the tile that a Bing Maps quadkey names.
func parseQuadkey(key string) (Tile, bool) {
	tile := Tile{Z: len(key)}
	for i, c := range key {
		if c < '0' || c > '3' {
			return Tile{}, false
		}
		mask := 1 << (len(key) - 1 - i)
		digit := int(c - '0')
		if digit&1 != 0 {
			tile.X |= mask
		}
		if digit&2 != 0 {
			tile.Y |= mask
		}
	}
	return tile, true
}

// encode returns a 256×256 tile image in the format that kind names.
// The image is made at test time, so the repository holds no binary fixture.
func encode(t testing.TB, kind Kind) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := range 256 {
		for x := range 256 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255}) //nolint:gosec // x and y are below 256.
		}
	}
	var buf bytes.Buffer
	var err error
	if kind == JPEG {
		err = jpeg.Encode(&buf, img, nil)
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatalf("encode the tile image: %v", err)
	}
	return buf.Bytes()
}
