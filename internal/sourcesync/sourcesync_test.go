package sourcesync

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testCommit = "bc5d9d541ca1f2086ecf21c78e3dd9f459e13f40"

// entry is one file in a test archive.
type entry struct {
	name string
	body string
}

// archive returns a gzip tarball with a pax global header that names testCommit, as git archive writes it.
func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := tw.WriteHeader(&tar.Header{
		Typeflag:   tar.TypeXGlobalHeader,
		Name:       "pax_global_header",
		PAXRecords: map[string]string{"comment": testCommit},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		hdr := &tar.Header{Typeflag: tar.TypeReg, Name: e.name, Mode: 0o644, Size: int64(len(e.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeGitHub is a fake GitHub API that serves one release and its tarball.
type fakeGitHub struct {
	*httptest.Server
	mu sync.Mutex
	// paths holds the path of each request, in order.
	paths []string
	// agents holds the User-Agent of each request, in order.
	agents []string
}

// newFakeGitHub starts a fake GitHub API with release tag and the tarball body.
// A request for another release tag returns 404.
func newFakeGitHub(t *testing.T, tag string, body []byte) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	mux := http.NewServeMux()
	release := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","target_commitish":"master","tarball_url":"` +
			f.URL + `/repos/joshuafuller/ATAK-Maps/tarball/` + tag + `"}`))
	}
	mux.HandleFunc("GET /repos/joshuafuller/ATAK-Maps/releases/latest", release)
	mux.HandleFunc("GET /repos/joshuafuller/ATAK-Maps/releases/tags/"+tag, release)
	mux.HandleFunc("GET /repos/joshuafuller/ATAK-Maps/tarball/"+tag, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	})
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.agents = append(f.agents, r.UserAgent())
		f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.Close)
	return f
}

// options returns the sync options for the fake API f and the user source directory dir.
func options(f *httptest.Server, dir string) Options {
	return Options{
		APIURL:    f.URL,
		Repo:      "joshuafuller/ATAK-Maps",
		Dir:       dir,
		UserAgent: "atak-map-downloader/test",
		Client:    f.Client(),
	}
}

// writeFile writes body to dir/rel and creates the parent directories.
func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tree returns every regular file under dir as a map from slash path to content.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // The path is under the test directory.
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// equalTrees reports a test error for each difference between got and want.
func equalTrees(t *testing.T, got, want map[string]string) {
	t.Helper()
	for name, body := range want {
		if g, ok := got[name]; !ok {
			t.Errorf("file %s is missing", name)
		} else if g != body {
			t.Errorf("file %s = %q, want %q", name, g, body)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("file %s is unexpected", name)
		}
	}
}

func TestSyncWritesTheXMLFilesUnderMapsAndVERSION(t *testing.T) {
	body := archive(t,
		entry{"joshuafuller-ATAK-Maps-bc5d9d5/README.md", "readme"},
		entry{"joshuafuller-ATAK-Maps-bc5d9d5/maps/base/osm/osm.xml", "<osm/>"},
		entry{"joshuafuller-ATAK-Maps-bc5d9d5/maps/overlays/roads.xml", "<roads/>"},
		entry{"joshuafuller-ATAK-Maps-bc5d9d5/maps/overlays/notes.txt", "notes"},
		entry{"joshuafuller-ATAK-Maps-bc5d9d5/docs/example.xml", "<example/>"},
	)
	f := newFakeGitHub(t, "v2.4.0", body)
	dir := filepath.Join(t.TempDir(), "mapsources")

	res, err := Sync(t.Context(), options(f.Server, dir))
	if err != nil {
		t.Fatalf("Sync() returned %v", err)
	}

	equalTrees(t, tree(t, dir), map[string]string{
		"atak-maps/maps/base/osm/osm.xml":   "<osm/>",
		"atak-maps/maps/overlays/roads.xml": "<roads/>",
		"atak-maps/VERSION":                 "tag: v2.4.0\ncommit: " + testCommit + "\n",
	})
	want := Result{Tag: "v2.4.0", Commit: testCommit, Added: 2}
	if res != want {
		t.Errorf("Sync() = %+v, want %+v", res, want)
	}
	if got := f.paths[0]; got != "/repos/joshuafuller/ATAK-Maps/releases/latest" {
		t.Errorf("first request path = %q, want the latest release", got)
	}
}

func TestSyncWithATagRequestsThatRelease(t *testing.T) {
	f := newFakeGitHub(t, "v2.3.1", archive(t, entry{"top/maps/a.xml", "<a/>"}))

	opts := options(f.Server, t.TempDir())
	opts.Tag = "v2.3.1"
	res, err := Sync(t.Context(), opts)
	if err != nil {
		t.Fatalf("Sync() returned %v", err)
	}
	if res.Tag != "v2.3.1" {
		t.Errorf("Sync() tag = %q, want v2.3.1", res.Tag)
	}
	if got := f.paths[0]; got != "/repos/joshuafuller/ATAK-Maps/releases/tags/v2.3.1" {
		t.Errorf("first request path = %q, want the release of tag v2.3.1", got)
	}
}

func TestSyncSendsTheUserAgentOnEveryRequest(t *testing.T) {
	f := newFakeGitHub(t, "v2.4.0", archive(t, entry{"top/maps/a.xml", "<a/>"}))

	if _, err := Sync(t.Context(), options(f.Server, t.TempDir())); err != nil {
		t.Fatalf("Sync() returned %v", err)
	}
	if len(f.agents) != 2 {
		t.Fatalf("the fake API got %d requests, want 2", len(f.agents))
	}
	for i, agent := range f.agents {
		if agent != "atak-map-downloader/test" {
			t.Errorf("request %d User-Agent = %q, want atak-map-downloader/test", i, agent)
		}
	}
}

func TestSyncCountsTheAddedChangedAndRemovedSourceFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "atak-maps/maps/same.xml", "<same/>")
	writeFile(t, dir, "atak-maps/maps/changed.xml", "<old/>")
	writeFile(t, dir, "atak-maps/maps/removed.xml", "<removed/>")
	writeFile(t, dir, "atak-maps/VERSION", "tag: v2.3.1\ncommit: old\n")
	f := newFakeGitHub(t, "v2.4.0", archive(t,
		entry{"top/maps/same.xml", "<same/>"},
		entry{"top/maps/changed.xml", "<new/>"},
		entry{"top/maps/added1.xml", "<added1/>"},
		entry{"top/maps/sub/added2.xml", "<added2/>"},
	))

	res, err := Sync(t.Context(), options(f.Server, dir))
	if err != nil {
		t.Fatalf("Sync() returned %v", err)
	}

	want := Result{Tag: "v2.4.0", Commit: testCommit, Added: 2, Changed: 1, Removed: 1}
	if res != want {
		t.Errorf("Sync() = %+v, want %+v", res, want)
	}
	equalTrees(t, tree(t, dir), map[string]string{
		"atak-maps/maps/same.xml":       "<same/>",
		"atak-maps/maps/changed.xml":    "<new/>",
		"atak-maps/maps/added1.xml":     "<added1/>",
		"atak-maps/maps/sub/added2.xml": "<added2/>",
		"atak-maps/VERSION":             "tag: v2.4.0\ncommit: " + testCommit + "\n",
	})
}

func TestSyncKeepsTheUserFilesBesideAtakMaps(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "my-source.xml", "<mine/>")
	writeFile(t, dir, "team/other.xml", "<team/>")
	writeFile(t, dir, "atak-maps/maps/old.xml", "<old/>")
	f := newFakeGitHub(t, "v2.4.0", archive(t, entry{"top/maps/a.xml", "<a/>"}))

	if _, err := Sync(t.Context(), options(f.Server, dir)); err != nil {
		t.Fatalf("Sync() returned %v", err)
	}

	equalTrees(t, tree(t, dir), map[string]string{
		"my-source.xml":        "<mine/>",
		"team/other.xml":       "<team/>",
		"atak-maps/maps/a.xml": "<a/>",
		"atak-maps/VERSION":    "tag: v2.4.0\ncommit: " + testCommit + "\n",
	})
}

// userTree writes a user source directory with a user file and an earlier sync, and returns its files.
func userTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	writeFile(t, dir, "my-source.xml", "<mine/>")
	writeFile(t, dir, "atak-maps/maps/old.xml", "<old/>")
	writeFile(t, dir, "atak-maps/VERSION", "tag: v2.3.1\ncommit: old\n")
	return tree(t, dir)
}

func TestSyncRejectsAnArchivePathThatLeavesTheTarget(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"rejects a path that starts with ..", "../evil.xml"},
		{"rejects a path with .. under maps", "top/maps/../../evil.xml"},
		{"rejects a path with .. that stays under maps", "top/maps/sub/../evil.xml"},
		{"rejects an absolute path", "/top/maps/evil.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "mapsources")
			before := userTree(t, dir)
			f := newFakeGitHub(t, "v2.4.0", archive(t,
				entry{"top/maps/good.xml", "<good/>"},
				entry{tt.path, "<evil/>"},
			))

			_, err := Sync(t.Context(), options(f.Server, dir))
			if err == nil {
				t.Fatal("Sync() returned nil, want an error")
			}
			equalTrees(t, tree(t, dir), before)
			// A path that escapes the user source directory must not reach its parent either.
			if _, err := os.Stat(filepath.Join(root, "evil.xml")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("evil.xml beside the user source directory: Stat() returned %v, want not exist", err)
			}
		})
	}
}

func TestSyncWithNoMapsDirectoryChangesNothing(t *testing.T) {
	dir := t.TempDir()
	before := userTree(t, dir)
	f := newFakeGitHub(t, "v3.0.0", archive(t,
		entry{"top/README.md", "readme"},
		entry{"top/sources/a.xml", "<a/>"},
	))

	_, err := Sync(t.Context(), options(f.Server, dir))
	if err == nil || !strings.Contains(err.Error(), "maps/") {
		t.Fatalf("Sync() returned %v, want an error that names maps/", err)
	}
	equalTrees(t, tree(t, dir), before)
}

func TestSyncKeepsTheSourceFilesOnAnHTTPError(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		header  map[string]string
		message string
	}{
		{"keeps the files on a 404 for the release", http.StatusNotFound, nil, "404"},
		{"keeps the files on a 500", http.StatusInternalServerError, nil, "500"},
		{
			"reports the reset time on a 403 with no remaining requests", http.StatusForbidden,
			map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "1795279542"}, "rate limit",
		},
		{"reports Retry-After on a 429", http.StatusTooManyRequests, map[string]string{"Retry-After": "60"}, "60 s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			before := userTree(t, dir)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tt.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			_, err := Sync(t.Context(), options(srv, dir))
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("Sync() returned %v, want an error that holds %q", err, tt.message)
			}
			equalTrees(t, tree(t, dir), before)
		})
	}
}

func TestSyncKeepsTheSourceFilesWhenTheServerIsUnreachable(t *testing.T) {
	dir := t.TempDir()
	before := userTree(t, dir)
	srv := httptest.NewServer(http.NotFoundHandler())
	opts := options(srv, dir)
	srv.Close()

	if _, err := Sync(t.Context(), opts); err == nil {
		t.Fatal("Sync() returned nil, want an error")
	}
	equalTrees(t, tree(t, dir), before)
}

func TestSyncKeepsTheSourceFilesWhenTheTarballFails(t *testing.T) {
	dir := t.TempDir()
	before := userTree(t, dir)
	f := newFakeGitHub(t, "v2.4.0", []byte("not a gzip stream"))

	if _, err := Sync(t.Context(), options(f.Server, dir)); err == nil {
		t.Fatal("Sync() returned nil, want an error")
	}
	equalTrees(t, tree(t, dir), before)
}
