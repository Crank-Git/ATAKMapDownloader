package testtiles

import (
	"bytes"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// get sends one GET request and returns the status, the headers and the body.
func get(t *testing.T, url string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // The URL comes from the test server.
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, body
}

// checkHeader reports an error when header holds a value other than want for key.
// An empty key or an empty want skips the check.
func checkHeader(t *testing.T, header http.Header, key, want string) {
	t.Helper()
	if key != "" && want != "" && header.Get(key) != want {
		t.Errorf("%s = %q, want %q", key, header.Get(key), want)
	}
}

// responseCases holds one case for each response kind of FR-foundation-10.
var responseCases = []struct {
	name        string
	response    Response
	wantStatus  int
	wantType    string
	wantHeader  [2]string
	checkBody   func(t *testing.T, body []byte)
	minDuration time.Duration
}{
	{
		name:       "returns a decodable PNG",
		response:   Response{Kind: PNG},
		wantStatus: http.StatusOK,
		wantType:   "image/png",
		checkBody: func(t *testing.T, body []byte) {
			t.Helper()
			if _, err := png.Decode(bytes.NewReader(body)); err != nil {
				t.Errorf("decode PNG: %v", err)
			}
		},
	},
	{
		name:       "returns a decodable JPEG",
		response:   Response{Kind: JPEG},
		wantStatus: http.StatusOK,
		wantType:   "image/jpeg",
		checkBody: func(t *testing.T, body []byte) {
			t.Helper()
			if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
				t.Errorf("decode JPEG: %v", err)
			}
		},
	},
	{
		name:       "returns 404",
		response:   Response{Kind: NotFound},
		wantStatus: http.StatusNotFound,
	},
	{
		name:       "returns 429 with the default Retry-After",
		response:   Response{Kind: TooManyRequests},
		wantStatus: http.StatusTooManyRequests,
		wantHeader: [2]string{"Retry-After", "1"},
	},
	{
		name:       "returns 429 with the configured Retry-After",
		response:   Response{Kind: TooManyRequests, RetryAfter: "7"},
		wantStatus: http.StatusTooManyRequests,
		wantHeader: [2]string{"Retry-After", "7"},
	},
	{
		name:       "returns 503",
		response:   Response{Kind: Unavailable},
		wantStatus: http.StatusServiceUnavailable,
	},
	{
		name:       "returns an HTML body with status 200",
		response:   Response{Kind: HTML},
		wantStatus: http.StatusOK,
		wantType:   "text/html; charset=utf-8",
		checkBody: func(t *testing.T, body []byte) {
			t.Helper()
			if !strings.Contains(string(body), "<html>") {
				t.Errorf("body = %q, want an HTML page", body)
			}
		},
	},
	{
		name:       "returns an empty body with status 200",
		response:   Response{Kind: Empty},
		wantStatus: http.StatusOK,
		checkBody: func(t *testing.T, body []byte) {
			t.Helper()
			if len(body) != 0 {
				t.Errorf("body has %d bytes, want 0", len(body))
			}
		},
	},
	{
		name:       "returns a body that is not an image with status 200",
		response:   Response{Kind: Garbage},
		wantStatus: http.StatusOK,
		wantType:   "application/octet-stream",
		checkBody: func(t *testing.T, body []byte) {
			t.Helper()
			if len(body) == 0 || strings.Contains(string(body), "<html>") {
				t.Errorf("body = %q, want bytes that are not an image and not HTML", body)
			}
			if _, err := png.Decode(bytes.NewReader(body)); err == nil {
				t.Error("body decodes as a PNG, want bytes that are not an image")
			}
		},
	},
	{
		name:        "delays the response",
		response:    Response{Kind: PNG, Delay: 50 * time.Millisecond},
		wantStatus:  http.StatusOK,
		wantType:    "image/png",
		minDuration: 50 * time.Millisecond,
	},
}

func TestServerResponses(t *testing.T) {
	tile := Tile{Z: 3, X: 2, Y: 1}
	for _, tt := range responseCases {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(t, Table{tile: tt.response})
			start := time.Now()
			status, header, body := get(t, srv.URL+"/3/2/1.png")
			elapsed := time.Since(start)

			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			checkHeader(t, header, "Content-Type", tt.wantType)
			checkHeader(t, header, tt.wantHeader[0], tt.wantHeader[1])
			if tt.checkBody != nil {
				tt.checkBody(t, body)
			}
			if elapsed < tt.minDuration {
				t.Errorf("response took %v, want at least %v", elapsed, tt.minDuration)
			}
		})
	}
}

func TestServerReturnsThePNGForATileThatTheTableOmits(t *testing.T) {
	srv := New(t, nil)
	status, header, _ := get(t, srv.URL+"/5/10/12.png")
	if status != http.StatusOK || header.Get("Content-Type") != "image/png" {
		t.Errorf("got status %d and type %q, want 200 and image/png", status, header.Get("Content-Type"))
	}
}

func TestServerCountsRequestsPerTile(t *testing.T) {
	srv := New(t, nil)
	get(t, srv.URL+"/1/0/1.png")
	get(t, srv.URL+"/1/0/1.png")
	get(t, srv.URL+"/1/1/1.png")

	tests := []struct {
		name string
		tile Tile
		want int
	}{
		{"counts two requests for a tile requested twice", Tile{Z: 1, X: 0, Y: 1}, 2},
		{"counts one request for a tile requested once", Tile{Z: 1, X: 1, Y: 1}, 1},
		{"counts no request for a tile never requested", Tile{Z: 1, X: 0, Y: 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := srv.Requests(tt.tile); got != tt.want {
				t.Errorf("Requests(%v) = %d, want %d", tt.tile, got, tt.want)
			}
		})
	}
	if got := srv.TotalRequests(); got != 3 {
		t.Errorf("TotalRequests() = %d, want 3", got)
	}
}

func TestServerPaths(t *testing.T) {
	srv := New(t, Table{{Z: 3, X: 5, Y: 2}: {Kind: NotFound}})
	tests := []struct {
		name       string
		path       string
		wantStatus int
	}{
		{"maps an XYZ path to its tile", "/3/5/2.png", http.StatusNotFound},
		{"maps an XYZ path without an extension to its tile", "/3/5/2", http.StatusNotFound},
		{"maps a quadkey path to its tile", "/q/" + Quadkey(Tile{Z: 3, X: 5, Y: 2}) + ".png", http.StatusNotFound},
		{"rejects a path that names no tile", "/robots.txt", http.StatusBadRequest},
		{"rejects a quadkey with a digit above 3", "/q/014", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if status, _, _ := get(t, srv.URL+tt.path); status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
		})
	}
}

func TestQuadkey(t *testing.T) {
	tests := []struct {
		name string
		tile Tile
		want string
	}{
		{"encodes zoom level 0 as an empty key", Tile{Z: 0, X: 0, Y: 0}, ""},
		{"encodes the Bing example tile", Tile{Z: 3, X: 3, Y: 5}, "213"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Quadkey(tt.tile); got != tt.want {
				t.Errorf("Quadkey(%v) = %q, want %q", tt.tile, got, tt.want)
			}
		})
	}
}

func TestURLTemplateUsesTheMapSourcePlaceholders(t *testing.T) {
	srv := New(t, nil)
	want := srv.URL + "/{$z}/{$x}/{$y}.png"
	if got := srv.URLTemplate(); got != want {
		t.Errorf("URLTemplate() = %q, want %q", got, want)
	}
}

func TestServerSendsAResponseWithTimesOnlyForTheFirstRequests(t *testing.T) {
	srv := New(t, Table{{Z: 1, X: 0, Y: 0}: {Kind: Unavailable, Times: 2}})
	want := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK}
	for i, w := range want {
		if status, _, _ := get(t, srv.URL+"/1/0/0.png"); status != w {
			t.Errorf("request %d got status %d, want %d", i+1, status, w)
		}
	}
}

func TestServerLogsEachRequestWithItsUserAgent(t *testing.T) {
	srv := New(t, nil)
	before := time.Now()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/2/1/3.png", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("User-Agent", "test-agent/1")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // The URL comes from the test server.
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close() // The test reads no body, so a close error changes no result.

	log := srv.Log()
	if len(log) != 1 {
		t.Fatalf("Log() holds %d requests, want 1", len(log))
	}
	if got := log[0]; got.Tile != (Tile{Z: 2, X: 1, Y: 3}) || got.UserAgent != "test-agent/1" || got.Time.Before(before) {
		t.Errorf("Log()[0] = %+v, want tile 2/1/3, User-Agent test-agent/1 and a time after %v", got, before)
	}
}
