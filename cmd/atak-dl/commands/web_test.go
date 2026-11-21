package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

const testSourceXML = `<?xml version="1.0"?>
<customMapSource>
    <name>Test Source</name>
    <minZoom>0</minZoom>
    <maxZoom>18</maxZoom>
    <tileType>png</tileType>
    <url>https://example.com/{$z}/{$x}/{$y}.png</url>
</customMapSource>`

// newTestHandler returns a handler whose source catalog holds the given XML files.
func newTestHandler(t *testing.T, sourceFiles map[string]string) *WebHandler {
	t.Helper()
	dir := t.TempDir()
	for name, content := range sourceFiles {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return NewWebHandler(xmlparser.New(dir), tilecalc.NewTileCalculator(), dir, testUserAgent)
}

// testUserAgent has the form of FR-polite-downloading-14.
const testUserAgent = "atak-map-downloader/test (+https://github.com/Crank-Git/ATAKMapDownloader)"

func serve(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestErrorResponsesUseTheErrorShape(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{"estimate rejects GET", http.MethodGet, "/api/estimate", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"estimate rejects a body that is not JSON", http.MethodPost, "/api/estimate", "{", http.StatusBadRequest, "invalid_json"},
		{"estimate rejects three bounds values", http.MethodPost, "/api/estimate", `{"bounds":[1,2,3]}`, http.StatusBadRequest, "invalid_bounds"},
		{"estimate rejects inverted bounds", http.MethodPost, "/api/estimate", `{"bounds":[10,10,0,0]}`, http.StatusBadRequest, "invalid_bounds"},
		{"tile-info rejects GET", http.MethodGet, "/api/tile-info", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"tile-info rejects a body that is not JSON", http.MethodPost, "/api/tile-info", "{", http.StatusBadRequest, "invalid_json"},
		{"validate-source rejects GET", http.MethodGet, "/api/validate-source", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"validate-source rejects a body that is not JSON", http.MethodPost, "/api/validate-source", "{", http.StatusBadRequest, "invalid_json"},
		{"validate-source rejects an unknown source", http.MethodPost, "/api/validate-source", `{"source_name":"Nope"}`, http.StatusNotFound, "unknown_source"},
		{"preview rejects GET", http.MethodGet, "/api/preview", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"preview rejects a body that is not JSON", http.MethodPost, "/api/preview", "{", http.StatusBadRequest, "invalid_json"},
		{"preview rejects a missing source name", http.MethodPost, "/api/preview", `{"bounds":[0,0,1,1]}`, http.StatusBadRequest, "unknown_source"},
		{"preview rejects three bounds values", http.MethodPost, "/api/preview", `{"source":"Test Source","bounds":[1,2,3]}`, http.StatusBadRequest, "invalid_bounds"},
		{"preview rejects an unknown source", http.MethodPost, "/api/preview", `{"source":"Nope","bounds":[0,0,1,1]}`, http.StatusNotFound, "unknown_source"},
		{"geocode rejects GET", http.MethodGet, "/api/geocode", "", http.StatusMethodNotAllowed, "method_not_allowed"},
		{"geocode rejects a body that is not JSON", http.MethodPost, "/api/geocode", "{", http.StatusBadRequest, "invalid_json"},
		{"geocode rejects an empty query", http.MethodPost, "/api/geocode", `{"query":"  "}`, http.StatusBadRequest, "invalid_query"},
		{"api rejects an unknown route", http.MethodGet, "/api/nope", "", http.StatusNotFound, "not_found"},
		{"static rejects a missing file", http.MethodGet, "/static/missing.js", "", http.StatusNotFound, "not_found"},
		{"server rejects an unknown path", http.MethodGet, "/nope", "", http.StatusNotFound, "not_found"},
		{"server rejects the removed websocket route", http.MethodGet, "/ws", "", http.StatusNotFound, "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, tt.method, tt.path, tt.body)
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			dec := json.NewDecoder(rec.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&body); err != nil {
				t.Fatalf("body does not parse as the error shape: %v", err)
			}
			if body.Error == nil {
				t.Fatal("body has no error object")
			}
			if body.Error.Code != tt.code {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.code)
			}
			if body.Error.Message == "" {
				t.Error("message is empty")
			}
		})
	}
}

func TestSuccessResponsesSetTheJSONContentType(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"sources", http.MethodGet, "/api/sources", ""},
		{"estimate", http.MethodPost, "/api/estimate", `{"bounds":[0,0,1,1],"min_zoom":1,"max_zoom":2}`},
		{"tile-info", http.MethodPost, "/api/tile-info", `{"lat":1,"lon":1,"zoom":3}`},
		{"validate-source", http.MethodPost, "/api/validate-source", `{"source_name":"Test Source"}`},
		{"preview", http.MethodPost, "/api/preview", `{"source":"Test Source","bounds":[0,0,1,1],"max_zoom":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, tt.method, tt.path, tt.body)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
		})
	}
}

func TestSourcesReturnsAnEmptyArray(t *testing.T) {
	h := newTestHandler(t, nil)
	rec := serve(h, http.MethodGet, "/api/sources", "")
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Errorf("body = %q, want []", got)
	}
}

func TestSourcesReturnsThePolicyOfEachSource(t *testing.T) {
	googleXML := strings.Replace(testSourceXML, "https://example.com/", "https://mt1.google.com/", 1)
	googleXML = strings.Replace(googleXML, "Test Source", "Google Test", 1)
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML, "google.xml": googleXML})
	rec := serve(h, http.MethodGet, "/api/sources", "")

	var got []sourceItem
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := map[string]sourceItem{
		"Google Test": {
			PolicyClass:        "restricted",
			PolicyReferenceURL: "https://developers.google.com/maps/documentation/tile/policies",
			RequestsPerSecond:  1,
			PolicyHost:         "*.google.com",
		},
		"Test Source": {
			PolicyClass:       "conditional",
			RequestsPerSecond: 4,
			PolicyHost:        "example.com",
		},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d sources, want %d", len(got), len(want))
	}
	for _, item := range got {
		w := want[item.Name]
		if item.PolicyClass != w.PolicyClass || item.PolicyReferenceURL != w.PolicyReferenceURL ||
			item.RequestsPerSecond != w.RequestsPerSecond || item.PolicyHost != w.PolicyHost || item.PolicyReason == "" {
			t.Errorf("source %q = %+v, want the policy fields of %+v and a reason", item.Name, item, w)
		}
	}
}

func TestPreviewRejectsInvertedBounds(t *testing.T) {
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	// The south value is above the north value.
	rec := serve(h, http.MethodPost, "/api/preview", `{"source":"Test Source","bounds":[0,10,1,0]}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("body does not parse as the error shape: %v", err)
	}
	if body.Error.Code != "invalid_bounds" {
		t.Errorf("code = %q, want invalid_bounds", body.Error.Code)
	}
}

func TestGeocodeSendsTheUserAgentToNominatim(t *testing.T) {
	var got string
	nominatim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.UserAgent()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`)) // The handler test below reports a failed write as a wrong response.
	}))
	t.Cleanup(nominatim.Close)
	h := newTestHandler(t, nil)
	h.geocodeURL = nominatim.URL

	rec := serve(h, http.MethodPost, "/api/geocode", `{"query":"Berlin"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/geocode returned %d, want 200: %s", rec.Code, rec.Body)
	}
	if got != testUserAgent {
		t.Errorf("Nominatim got User-Agent %q, want %q", got, testUserAgent)
	}
}
