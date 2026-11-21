package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/web"
)

// serveAuth sends req to h and returns the response.
func serveAuth(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPageRoutesServeHTML(t *testing.T) {
	h := newTestHandler(t, nil)
	tests := []struct {
		path  string
		title string
	}{
		{"/jobs", "<title>Jobs"},
		{"/signin", "<title>Sign in"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := serve(newServerHandler(h), http.MethodGet, tt.path, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s returned %d, want 200", tt.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
			}
			if !strings.Contains(rec.Body.String(), tt.title) {
				t.Errorf("GET %s does not serve a page with %q", tt.path, tt.title)
			}
		})
	}
}

func TestJobsPageRedirectsToSigninOnAServerThatIsNotOnLoopback(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		cookie   string
		status   int
		location string
	}{
		{name: "redirects GET /jobs with no token", path: "/jobs", status: http.StatusFound, location: "/signin"},
		{name: "redirects the script of the Jobs page with no token", path: "/static/js/jobs.js", status: http.StatusFound, location: "/signin"},
		{name: "serves GET /jobs with the token cookie", path: "/jobs", cookie: testToken, status: http.StatusOK},
		{name: "serves GET /signin with no token", path: "/signin", status: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveAuth(newAuthServer(t, nil), authRequest(http.MethodGet, tt.path, "", "192.0.2.1", "", tt.cookie))
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := rec.Header().Get("Location"); got != tt.location {
				t.Errorf("Location = %q, want %q", got, tt.location)
			}
		})
	}
}

func TestSigninPageLoadsOnlyFilesThatNeedNoToken(t *testing.T) {
	// A file that needs the token redirects to /signin, so the sign-in page could not load it.
	local := regexp.MustCompile(`(?:src|href)="(/static/[^"]+)"`).FindAllStringSubmatch(string(web.SigninHTML()), -1)
	if len(local) == 0 {
		t.Fatal("signin.html loads no local file, so the pattern is stale")
	}
	h := newAuthServer(t, nil)
	for _, m := range local {
		if !publicPaths[m[1]] {
			t.Errorf("signin.html loads %s, which is not in publicPaths", m[1])
		}
		if rec := serveAuth(h, authRequest(http.MethodGet, m[1], "", "192.0.2.1", "", "")); rec.Code != http.StatusOK {
			t.Errorf("GET %s with no token returned %d, want 200", m[1], rec.Code)
		}
	}
}

func TestConfigReportsTheServerFooterFields(t *testing.T) {
	h := newTestHandler(t, nil)
	h.addr = "0.0.0.0:5000"
	h.workers = 3
	h.version = "1.2.3"
	rec := serve(h, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config returned %d, want 200: %s", rec.Code, rec.Body)
	}
	var body struct {
		Addr    string `json:"addr"`
		DataDir string `json:"data_dir"`
		Workers int    `json:"workers"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Addr != "0.0.0.0:5000" || body.DataDir != h.outputDir || body.Workers != 3 || body.Version != "1.2.3" {
		t.Errorf("GET /api/config = %s, want addr 0.0.0.0:5000, data_dir %s, workers 3 and version 1.2.3", rec.Body, h.outputDir)
	}
}

func TestJobRecordNamesTheFirstAreaAndKeepsTheNameAfterARestart(t *testing.T) {
	named := `{"source":"Test Tiles","areas":[{"kind":"polygon","geometry":` + rectangleJSON(fourTiles) +
		fmt.Sprintf(`,"min_zoom":%d,"max_zoom":%d,"name":"North camp"}]}`, fourTiles.Zoom, fourTiles.Zoom)
	tests := []struct {
		name string
		body string
		want string
	}{
		{"names the first area", named, "North camp"},
		{"has no name for a legacy bounds job", jobBody(fourTiles), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			s := newDataDirJobServer(t, nil, "Test Tiles", "png", dir)
			rec := s.startJob(t, tt.body)
			if rec.AreaName != tt.want {
				t.Errorf("area_name = %q, want %q", rec.AreaName, tt.want)
			}
			s.finalRecord(t, rec.ID)
			// jobs.jsonl holds the record, so a restart reads the same area name (FR-team-server-8).
			reopened := newDataDirHandler(t, map[string]string{}, dir)
			if got := reopened.jobs[rec.ID].snapshot().AreaName; got != tt.want {
				t.Errorf("area_name after a restart = %q, want %q", got, tt.want)
			}
		})
	}
}
