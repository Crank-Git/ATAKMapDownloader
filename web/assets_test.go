package web

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// The online base map is the only external host that the web UI files name (FR-offline-web-ui-2).
// Preview tile URLs come from the /api/preview response at run time, so no file names them.
var allowedExternalURLs = map[string]bool{
	"https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png": true,
}

var externalURL = regexp.MustCompile(`(?i)(?:https?:)?//[a-z0-9{}.-]+\.[a-z{}]+[^\s"'<>)]*`)

// ownFiles returns index.html and every JavaScript and CSS file outside static/vendor, keyed by path.
func ownFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{"index.html": IndexHTML()}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && p == "static/vendor" {
			return fs.SkipDir
		}
		if ext := path.Ext(p); !d.IsDir() && (ext == ".js" || ext == ".css") {
			b, err := staticFS.ReadFile(p)
			if err != nil {
				return err
			}
			files[p] = b
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static: %v", err)
	}
	return files
}

func TestOwnFilesNameNoExternalHostOutsideTheAllowList(t *testing.T) {
	for name, body := range ownFiles(t) {
		for _, u := range externalURL.FindAllString(string(body), -1) {
			if !allowedExternalURLs[u] {
				t.Errorf("%s names the external URL %q", name, u)
			}
		}
	}
}

func TestWebFilesNameNoCDNAndNoBootstrapOrFontAwesome(t *testing.T) {
	// The vendored files are part of the check, because the acceptance criterion greps all of web/.
	// The names are split, because the acceptance criterion greps web/ for them and this file is in web/.
	cdn := regexp.MustCompile("un" + "pkg|jsdel" + "ivr|cdn" + "js")
	// The vendored files mention neither library, so the check covers every file.
	removed := regexp.MustCompile(`(?i)bootstrap|font-awesome|fontawesome|\bfas fa-|data-bs-`)
	files := ownFiles(t)
	err := fs.WalkDir(staticFS, "static/vendor", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		files[p] = b
		return err
	})
	if err != nil {
		t.Fatalf("walk static/vendor: %v", err)
	}
	for name, body := range files {
		if m := cdn.Find(body); m != nil {
			t.Errorf("%s names the CDN %q", name, m)
		}
		if m := removed.Find(body); m != nil {
			t.Errorf("%s names %q, which belongs to Bootstrap or Font Awesome (FR-offline-web-ui-3)", name, m)
		}
	}
}

func TestVendoredCSSLoadsNoExternalURL(t *testing.T) {
	// A url() or an @import in a stylesheet makes the browser send a request, so only data: and relative URLs pass.
	load := regexp.MustCompile(`(?i)(?:url\(\s*["']?|@import\s+["'])((?:https?:)?//[^"')\s]+)`)
	err := fs.WalkDir(staticFS, "static/vendor", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".css" {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		// A data: URL holds an SVG namespace URI, which is a name and not a request, so data: URLs are removed first.
		b = regexp.MustCompile(`url\(\s*'data:[^']*'\s*\)|url\(\s*"data:[^"]*"\s*\)`).ReplaceAll(b, nil)
		for _, m := range load.FindAllSubmatch(b, -1) {
			t.Errorf("%s loads the external URL %q", p, m[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static/vendor: %v", err)
	}
}

// licensedFiles reads the file table of static/vendor/LICENSES.md and returns the SHA-256 of each listed path.
func licensedFiles(t *testing.T) map[string]string {
	t.Helper()
	b, err := staticFS.ReadFile("static/vendor/LICENSES.md")
	if err != nil {
		t.Fatalf("read LICENSES.md: %v", err)
	}
	row := regexp.MustCompile("^\\|\\s*`([^`]+)`\\s*\\|\\s*`([0-9a-f]{64})`\\s*\\|")
	sums := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if m := row.FindStringSubmatch(sc.Text()); m != nil {
			sums[m[1]] = m[2]
		}
	}
	if len(sums) == 0 {
		t.Fatal("LICENSES.md lists no file with a SHA-256")
	}
	return sums
}

func TestEveryVendoredFileMatchesTheSHA256InLICENSES(t *testing.T) {
	sums := licensedFiles(t)
	seen := map[string]bool{}
	err := fs.WalkDir(staticFS, "static/vendor", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == "static/vendor/LICENSES.md" {
			return err
		}
		rel := strings.TrimPrefix(p, "static/vendor/")
		seen[rel] = true
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		want, ok := sums[rel]
		switch {
		case !ok:
			t.Errorf("LICENSES.md does not list %s", rel)
		case hex.EncodeToString(sum[:]) != want:
			t.Errorf("%s has SHA-256 %x, LICENSES.md says %s", rel, sum, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk static/vendor: %v", err)
	}
	for rel := range sums {
		if !seen[rel] {
			t.Errorf("LICENSES.md lists %s, which is not in static/vendor", rel)
		}
	}
}

func TestIndexLoadsTheVendoredLibrariesFromTheServer(t *testing.T) {
	index := string(IndexHTML())
	for _, want := range []string{
		`/static/vendor/leaflet/leaflet.css`,
		`/static/vendor/leaflet/leaflet.js`,
		`/static/vendor/leaflet-geoman/leaflet-geoman.css`,
		`/static/vendor/leaflet-geoman/leaflet-geoman.min.js`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html does not load %s", want)
		}
	}
	// A stale path is a request that fails, so every local asset of the page must exist in the embedded files.
	local := regexp.MustCompile(`(?:src|href)="/static/([^"]+)"`)
	for _, m := range local.FindAllStringSubmatch(index, -1) {
		if _, err := fs.Stat(staticFS, "static/"+m[1]); err != nil {
			t.Errorf("index.html loads /static/%s, which is not embedded", m[1])
		}
	}
}

func TestIndexHoldsTheIndicatorTheBannerAndTheJobPanel(t *testing.T) {
	index := string(IndexHTML())
	tests := []struct {
		name string
		want []string
	}{
		{"connection indicator in the header (FR-offline-web-ui-17)", []string{`id="connStatus" class="chip ok"`}},
		{"loading state of the source picker (FR-offline-web-ui-18)", []string{`id="sourceLoading"`}},
		{"error banner with Retry (FR-offline-web-ui-19, 20)", []string{
			`id="errorBanner" class="banner err" hidden`,
			`id="errorMessage"`,
			`id="errorRetry"`,
			`>Retry</button>`,
		}},
		{"job panel counts (FR-offline-web-ui-21)", []string{
			`id="jobDownloaded"`,
			`id="jobSkipped"`,
			`id="jobFailed"`,
			`id="jobRate"`,
			`id="jobEta"`,
		}},
		{"if the file exists choice (FR-offline-web-ui-22)", []string{
			`<label for="ifExists">If the file exists</label>`,
			`<option value="fail" selected>Fail</option>`,
			`<option value="resume">Resume</option>`,
			`<option value="overwrite">Overwrite</option>`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.want {
				if !strings.Contains(index, s) {
					t.Errorf("index.html does not contain %q", s)
				}
			}
		})
	}
}

func TestAppJSSendsTheStateFieldsAndAcceptsOneZoomLevel(t *testing.T) {
	b, err := staticFS.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(b)
	for _, want := range []string{
		"if_exists: document.getElementById('ifExists').value",
		"this.onJobComplete(record)",
		"this.connectionState",
	} {
		if !strings.Contains(app, want) {
			t.Errorf("app.js does not contain %q", want)
		}
	}
	// The server picks the size per tile from the map source, so the estimate request names it (FR-offline-web-ui-23).
	if !regexp.MustCompile(`'/api/estimate'[^}]*\{\s*source:`).MatchString(app) {
		t.Error("app.js sends no source field in the /api/estimate request")
	}
	// A zoom range with one level is valid (FR-offline-web-ui-24), so no check rejects min equal to max.
	if strings.Contains(app, "minZoom >= maxZoom") {
		t.Error("app.js rejects a zoom range where min_zoom equals max_zoom")
	}
}

func TestIndexHoldsTheBaseMapSwitcherWithTheOnlineBaseMap(t *testing.T) {
	index := string(IndexHTML())
	for _, want := range []string{
		`id="baseMap" class="basemap"`,
		`<select id="baseMapSelect">`,
		`<option value="">Online (OpenStreetMap)</option>`,
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html does not contain %q", want)
		}
	}
}

// methodBody returns the source of the app.js method name, from its signature to the closing brace at 4 spaces of indent.
func methodBody(t *testing.T, app, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?ms)^    (?:async )?` + regexp.QuoteMeta(name) + `\([^)]*\) \{\n.*?^    \}\n`).FindString(app)
	if m == "" {
		t.Fatalf("app.js has no method %s", name)
	}
	return m
}

func TestAppJSOffersOneLocalBaseMapPerOutputFile(t *testing.T) {
	b, err := staticFS.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(b)
	// The list refresh after a job also refreshes the switcher, so a new output file appears in both (FR-offline-web-ui-15).
	if !strings.Contains(methodBody(t, app, "renderOutputFiles"), "this.renderBaseMaps(files)") {
		t.Error("renderOutputFiles does not call renderBaseMaps")
	}
	render := methodBody(t, app, "renderBaseMaps")
	for _, want := range []string{"option.value = file.name", "option.textContent = file.name"} {
		if !strings.Contains(render, want) {
			t.Errorf("renderBaseMaps does not contain %q", want)
		}
	}
	if !strings.Contains(app, "'/api/files/' + encodeURIComponent(name) + '/tiles/{z}/{x}/{y}'") {
		t.Error("app.js does not request the tiles of an output file from the tile route")
	}
}

func TestAppJSSelectsTheNewestOutputFileWhenOfflineAtLoad(t *testing.T) {
	b, err := staticFS.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(b)
	fallback := methodBody(t, app, "fallBackToLocalBaseMap")
	for _, want := range []string{
		"navigator.onLine === false",
		"this.onlineTileFailed",
		// GET /api/files sorts newest first, so the first file is the most recent one (FR-offline-web-ui-16).
		"this.outputFiles[0]",
		"this.showError('baseMap', 'Offline: no local base map'",
	} {
		if !strings.Contains(fallback, want) {
			t.Errorf("fallBackToLocalBaseMap does not contain %q", want)
		}
	}
	// An offline indicator means that the server is down, and the local base map comes from the same server.
	if strings.Contains(fallback, "connectionState") {
		t.Error("fallBackToLocalBaseMap reads the connection state, which tells nothing about the internet")
	}
	if strings.Contains(methodBody(t, app, "setConnection"), "this.fallBackToLocalBaseMap()") {
		t.Error("setConnection calls fallBackToLocalBaseMap, so a server outage changes the base map")
	}
}

func TestAppJSDisablesStartWhileTheIndicatorReadsOffline(t *testing.T) {
	b, err := staticFS.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	app := string(b)
	// One method sets the disabled state of Start, so no other code path enables it while offline.
	if n := strings.Count(app, "getElementById('downloadBtn').disabled ="); n != 1 {
		t.Errorf("app.js sets the disabled state of Start in %d places, want 1", n)
	}
	gate := methodBody(t, app, "setStartEnabled")
	if !strings.Contains(gate, "getElementById('downloadBtn').disabled = !ready || this.connectionState === 'offline'") {
		t.Error("setStartEnabled does not disable Start while the indicator reads offline")
	}
	// A reconnect enables Start again only when it was ready before the server went offline.
	if !strings.Contains(methodBody(t, app, "setConnection"), "this.setStartEnabled(this.startReady)") {
		t.Error("setConnection does not apply the connection state to Start")
	}
}

func TestEveryElementIDThatAppJSReadsIsInIndex(t *testing.T) {
	app, err := staticFS.ReadFile("static/js/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	// app.js creates some elements itself and gives them an id, so those ids need no markup.
	created := map[string]bool{}
	for _, m := range regexp.MustCompile(`\.id = '([^']+)'`).FindAllSubmatch(app, -1) {
		created[string(m[1])] = true
	}
	index := string(IndexHTML())
	reads := regexp.MustCompile(`getElementById\('([^']+)'\)`).FindAllSubmatch(app, -1)
	if len(reads) == 0 {
		t.Fatal("app.js reads no element id, so the pattern is stale")
	}
	for _, m := range reads {
		id := string(m[1])
		if !created[id] && !strings.Contains(index, `id="`+id+`"`) {
			t.Errorf("app.js reads the element id %q, which index.html does not have", id)
		}
	}
}
