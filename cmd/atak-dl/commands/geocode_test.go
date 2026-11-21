package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeNominatim is a test server in place of Nominatim. Tests never send a request to the real Nominatim.
type fakeNominatim struct {
	*httptest.Server
	hits       atomic.Int32
	mu         sync.Mutex
	userAgents []string
	paths      []string
}

// newFakeNominatim returns a fake Nominatim that answers every request with status and body.
func newFakeNominatim(t *testing.T, status int, body string) *fakeNominatim {
	t.Helper()
	f := &fakeNominatim{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		f.userAgents = append(f.userAgents, r.UserAgent())
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) // The test reads the handler response, so a failed write shows there.
	}))
	t.Cleanup(f.Close)
	return f
}

const berlinResult = `[{"display_name":"Berlin, Germany","lat":"52.5170365","lon":"13.3888599","boundingbox":["52.3","52.7","13.0","13.8"],"type":"city","class":"place"}]`

// newGeocodeHandler returns a handler whose geocode requests go to the fake Nominatim f.
func newGeocodeHandler(t *testing.T, f *fakeNominatim) *WebHandler {
	t.Helper()
	h := newTestHandler(t, nil)
	h.geocodeURL = f.URL + "/search"
	return h
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body does not parse as the error shape: %v: %s", err, rec.Body)
	}
	return body.Error.Code
}

func TestGeocodeSendsAtMostTwoRequestsToNominatimForTenQueriesInOneSecond(t *testing.T) {
	f := newFakeNominatim(t, http.StatusOK, berlinResult)
	h := newGeocodeHandler(t, f)
	// A 1 s wait budget keeps the test at 1 s, and FR-polite-downloading-22 does not depend on the budget.
	h.geocodeWait = time.Second

	codes := make([]int, 10)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := serve(h, http.MethodPost, "/api/geocode", fmt.Sprintf(`{"query":"place %d"}`, i))
			codes[i] = rec.Code
			if rec.Code == http.StatusTooManyRequests {
				if code := errorCode(t, rec); code != "geocode_busy" {
					t.Errorf("429 response has code %q, want geocode_busy", code)
				}
			}
		}()
	}
	wg.Wait()

	if got := f.hits.Load(); got > 2 {
		t.Errorf("fake Nominatim got %d requests, want at most 2", got)
	}
	ok := 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
		default:
			t.Errorf("POST /api/geocode returned %d, want 200 or 429", c)
		}
	}
	if ok == 0 {
		t.Error("no geocode request returned 200")
	}
}

func TestGeocodeReturnsTheCachedResultForARepeatedQueryWithin24Hours(t *testing.T) {
	f := newFakeNominatim(t, http.StatusOK, berlinResult)
	h := newGeocodeHandler(t, f)
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	now := start
	h.geocodeCache.now = func() time.Time { return now }

	first := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first POST /api/geocode returned %d, want 200: %s", first.Code, first.Body)
	}
	now = start.Add(24*time.Hour - time.Second)
	// The cache key is the normalized query, so case and spaces do not matter (FR-polite-downloading-23).
	second := serve(h, http.MethodPost, "/api/geocode", `{"query":"  BERLIN  "}`)
	if second.Code != http.StatusOK {
		t.Fatalf("second POST /api/geocode returned %d, want 200: %s", second.Code, second.Body)
	}
	if got := f.hits.Load(); got != 1 {
		t.Errorf("fake Nominatim got %d requests, want 1", got)
	}
	var body struct {
		Count   int `json:"count"`
		Results []struct {
			DisplayName string  `json:"display_name"`
			Lat         float64 `json:"lat"`
		} `json:"results"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || body.Results[0].DisplayName != "Berlin, Germany" || body.Results[0].Lat != 52.5170365 {
		t.Errorf("cached response = %s, want the Berlin result", second.Body)
	}
}

func TestGeocodeSendsTheQueryToNominatimAgainAfter24Hours(t *testing.T) {
	f := newFakeNominatim(t, http.StatusOK, berlinResult)
	h := newGeocodeHandler(t, f)
	start := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	now := start
	h.geocodeCache.now = func() time.Time { return now }

	serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	now = start.Add(24*time.Hour + time.Second)
	rec := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/geocode returned %d, want 200: %s", rec.Code, rec.Body)
	}
	if got := f.hits.Load(); got != 2 {
		t.Errorf("fake Nominatim got %d requests, want 2", got)
	}
}

func TestGeocodeCacheHoldsAtMost1000Queries(t *testing.T) {
	c := newGeocodeCache()
	for i := range geocodeCacheSize + 5 {
		c.put(fmt.Sprintf("q%d", i), nil)
	}
	if got := c.len(); got != geocodeCacheSize {
		t.Errorf("cache holds %d queries, want %d", got, geocodeCacheSize)
	}
	if _, ok := c.get(fmt.Sprintf("q%d", geocodeCacheSize+4)); !ok {
		t.Error("cache lost the newest query")
	}
	if _, ok := c.get("q0"); ok {
		t.Error("cache kept the oldest query")
	}
}

func TestGeocodeReturns502WhenNominatimFails(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"status 500", http.StatusInternalServerError, `{"error":"boom"}`},
		{"a body that is not JSON", http.StatusOK, `<html>`},
		{"a coordinate that is not a number", http.StatusOK, `[{"lat":"north","lon":"1"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeNominatim(t, tt.status, tt.body)
			h := newGeocodeHandler(t, f)
			rec := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("POST /api/geocode returned %d, want 502: %s", rec.Code, rec.Body)
			}
			if code := errorCode(t, rec); code != "geocode_failed" {
				t.Errorf("code = %q, want geocode_failed", code)
			}
			// A failed result must not stay in the cache, so the next query asks Nominatim again.
			serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
			if got := f.hits.Load(); got != 2 {
				t.Errorf("fake Nominatim got %d requests, want 2", got)
			}
		})
	}
}

func TestGeocodeReturns502WhenNominatimIsUnreachable(t *testing.T) {
	f := newFakeNominatim(t, http.StatusOK, berlinResult)
	h := newGeocodeHandler(t, f)
	f.Close()
	rec := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST /api/geocode returned %d, want 502: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "geocode_failed" {
		t.Errorf("code = %q, want geocode_failed", code)
	}
}

func TestGeocodeSendsTheUserAgentWithEveryNominatimRequest(t *testing.T) {
	f := newFakeNominatim(t, http.StatusOK, `[]`)
	h := newGeocodeHandler(t, f)
	h.geocodeLimiter.SetLimit(1000)
	for _, q := range []string{"Berlin", "Paris", "Rome"} {
		serve(h, http.MethodPost, "/api/geocode", `{"query":"`+q+`"}`)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.userAgents) != 3 {
		t.Fatalf("fake Nominatim got %d requests, want 3", len(f.userAgents))
	}
	for i, ua := range f.userAgents {
		if ua != testUserAgent {
			t.Errorf("request %d has User-Agent %q, want %q", i, ua, testUserAgent)
		}
	}
}

// serveFlags returns a test handler with the geocode flags of serve applied from args.
func serveFlags(t *testing.T, args ...string) (*WebHandler, error) {
	t.Helper()
	cmd := NewServeCmd(testUserAgent, "dev")
	if err := cmd.ParseFlags(args); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, nil)
	return h, applyGeocodeFlags(cmd, h)
}

func TestServeGeocoderURLSetsTheNominatimBaseURL(t *testing.T) {
	f := newFakeNominatim(t, http.StatusOK, `[]`)
	h, err := serveFlags(t, "--geocoder-url", f.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/geocode returned %d, want 200: %s", rec.Code, rec.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) != 1 || f.paths[0] != "/search" {
		t.Errorf("fake Nominatim got paths %q, want [/search]", f.paths)
	}
}

func TestServeGeocoderURLDefaultsToNominatim(t *testing.T) {
	h, err := serveFlags(t)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://nominatim.openstreetmap.org/search"; h.geocodeURL != want {
		t.Errorf("geocodeURL = %q, want %q", h.geocodeURL, want)
	}
}

func TestServeRejectsAGeocoderURLThatIsNotHTTP(t *testing.T) {
	for _, u := range []string{"ftp://example.com", "example.com", "http://"} {
		if _, err := serveFlags(t, "--geocoder-url", u); err == nil {
			t.Errorf("--geocoder-url %q gave no error", u)
		}
	}
}

func TestServeNoGeocodeRemovesTheGeocodeRoute(t *testing.T) {
	h, err := serveFlags(t, "--no-geocode")
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/geocode returned %d, want 404: %s", rec.Code, rec.Body)
	}
	if code := errorCode(t, rec); code != "not_found" {
		t.Errorf("code = %q, want not_found", code)
	}
}

func TestConfigReportsWhetherGeocodeIsOn(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"--no-geocode"}, false},
	}
	for _, tt := range tests {
		h, err := serveFlags(t, tt.args...)
		if err != nil {
			t.Fatal(err)
		}
		rec := serve(h, http.MethodGet, "/api/config", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/config returned %d, want 200: %s", rec.Code, rec.Body)
		}
		var body struct {
			Geocode *bool `json:"geocode"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Geocode == nil || *body.Geocode != tt.want {
			t.Errorf("args %q: GET /api/config = %s, want geocode %v", tt.args, rec.Body, tt.want)
		}
	}
}

// The browser behavior needs a manual check, so this test proves only that the served files carry the search box hook (FR-polite-downloading-26).
func TestServedAssetsHoldTheSearchBoxHook(t *testing.T) {
	h := newTestHandler(t, nil)
	tests := []struct {
		path string
		want []string
	}{
		{"/", []string{`id="addressSearchGroup"`}},
		{"/static/js/app.js", []string{
			"fetch('/api/config')",
			"config.geocode === false",
			"getElementById('addressSearchGroup').hidden = true",
			"Place search is unavailable",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := serve(h, http.MethodGet, tt.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			for _, s := range tt.want {
				if !strings.Contains(rec.Body.String(), s) {
					t.Errorf("%s does not contain %q", tt.path, s)
				}
			}
		})
	}
}

func TestConfigRejectsPOST(t *testing.T) {
	h := newTestHandler(t, nil)
	rec := serve(h, http.MethodPost, "/api/config", "{}")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/config returned %d, want 405", rec.Code)
	}
}
