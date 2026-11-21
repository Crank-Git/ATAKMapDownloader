package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveWithHeaders sends one request with the given headers to h and returns the response.
func serveWithHeaders(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCrossSitePostIsRejected(t *testing.T) {
	h := newServerHandler(newTestHandler(t, map[string]string{"test.xml": testSourceXML}))
	crossSite := map[string]string{"Sec-Fetch-Site": "cross-site"}
	// httptest.NewRequest sets the Host header to example.com.
	otherOrigin := map[string]string{"Origin": "http://attacker.test"}
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
	}{
		{"POST /api/jobs with Sec-Fetch-Site cross-site", http.MethodPost, "/api/jobs", `{"source":"Test Source","bounds":[0,0,1,1]}`, crossSite},
		{"DELETE /api/jobs/{id} with Sec-Fetch-Site cross-site", http.MethodDelete, "/api/jobs/000000000000", "", crossSite},
		{"POST /api/estimate with Sec-Fetch-Site cross-site", http.MethodPost, "/api/estimate", `{"bounds":[0,0,1,1],"min_zoom":1,"max_zoom":2}`, crossSite},
		{"POST /api/jobs with an Origin of another host", http.MethodPost, "/api/jobs", `{"source":"Test Source","bounds":[0,0,1,1]}`, otherOrigin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveWithHeaders(h, tt.method, tt.path, tt.body, tt.headers)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			var body apiError
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("body does not parse as the error shape: %v", err)
			}
			if body.Error.Code != "cross_origin" {
				t.Errorf("code = %q, want cross_origin", body.Error.Code)
			}
		})
	}
}

func TestSameOriginPostIsAllowed(t *testing.T) {
	h := newServerHandler(newTestHandler(t, map[string]string{"test.xml": testSourceXML}))
	tests := []struct {
		name    string
		headers map[string]string
	}{
		{"Sec-Fetch-Site same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}},
		{"Origin of the same host", map[string]string{"Origin": "http://example.com"}},
		// curl and other clients that are not browsers send neither header.
		{"no Sec-Fetch-Site and no Origin", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveWithHeaders(h, http.MethodPost, "/api/estimate", `{"bounds":[0,0,1,1],"min_zoom":1,"max_zoom":2}`, tt.headers)
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestNoResponseHasACORSHeader(t *testing.T) {
	h := newServerHandler(newTestHandler(t, map[string]string{"test.xml": testSourceXML}))
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
	}{
		{"GET /", http.MethodGet, "/", "", nil},
		{"GET /api/sources", http.MethodGet, "/api/sources", "", nil},
		{"OPTIONS preflight on /api/jobs", http.MethodOptions, "/api/jobs", "", map[string]string{"Origin": "http://attacker.test", "Access-Control-Request-Method": "POST"}},
		{"POST /api/estimate", http.MethodPost, "/api/estimate", `{"bounds":[0,0,1,1],"min_zoom":1,"max_zoom":2}`, nil},
		{"rejected cross-site POST", http.MethodPost, "/api/jobs", "{}", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		{"unknown route", http.MethodGet, "/nope", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveWithHeaders(h, tt.method, tt.path, tt.body, tt.headers)
			for name := range rec.Header() {
				if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-Allow-") {
					t.Errorf("response has header %s", name)
				}
			}
		})
	}
}
