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
