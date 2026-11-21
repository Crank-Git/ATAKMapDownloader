package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// fileMetadata is the metadata of an output file that the engine writes for one job.
var fileMetadata = map[string]string{
	"format":        "png",
	"bounds":        "-74.1000,40.7000,-74.0000,40.8000",
	"minzoom":       "10",
	"maxzoom":       "12",
	"atakdl_source": "Test Tiles",
}

// listFiles returns the decoded body of GET /api/files and fails the test when the response is not a 200 JSON array.
func listFiles(t *testing.T, h http.Handler) []outputFileItem {
	t.Helper()
	rec := serve(h, http.MethodGet, "/api/files", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/files status = %d, want 200, body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var items []outputFileItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode GET /api/files: %v, body %s", err, rec.Body)
	}
	return items
}

// fileNames returns the names of items in their order.
func fileNames(items []outputFileItem) []string {
	names := []string{}
	for _, it := range items {
		names = append(names, it.Name)
	}
	return names
}

// filesHandler returns a handler whose output directory is a new temporary directory.
func filesHandler(t *testing.T) *WebHandler {
	t.Helper()
	h := newTestHandler(t, map[string]string{"test.xml": testSourceXML})
	h.outputDir = t.TempDir()
	return h
}

func TestFileListReturnsTheMetadataOfEachOutputFile(t *testing.T) {
	h := filesHandler(t)
	path := filepath.Join(h.outputDir, "area_z10-12.mbtiles")
	writeOutputFile(t, path, fileMetadata, oneTile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	items := listFiles(t, h)
	if len(items) != 1 {
		t.Fatalf("GET /api/files = %+v, want one file", items)
	}
	got := items[0]
	want := outputFileItem{
		Name:     "area_z10-12.mbtiles",
		Size:     info.Size(),
		Format:   "png",
		Source:   "Test Tiles",
		MinZoom:  10,
		MaxZoom:  12,
		Bounds:   []float64{-74.1, 40.7, -74.0, 40.8},
		Modified: got.Modified,
	}
	if got.Name != want.Name || got.Size != want.Size || got.Format != want.Format || got.Source != want.Source ||
		got.MinZoom != want.MinZoom || got.MaxZoom != want.MaxZoom || !slices.Equal(got.Bounds, want.Bounds) || got.InProgress {
		t.Errorf("GET /api/files item = %+v, want %+v", got, want)
	}
	if !got.Modified.Equal(info.ModTime()) {
		t.Errorf("modified = %v, want %v", got.Modified, info.ModTime())
	}
}

func TestFileListPutsTheNewestFileFirst(t *testing.T) {
	h := filesHandler(t)
	now := time.Now()
	for i, name := range []string{"old.mbtiles", "new.mbtiles", "middle.mbtiles"} {
		path := filepath.Join(h.outputDir, name)
		writeOutputFile(t, path, fileMetadata)
		age := map[int]time.Duration{0: 3 * time.Hour, 1: time.Minute, 2: time.Hour}[i]
		if err := os.Chtimes(path, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"new.mbtiles", "middle.mbtiles", "old.mbtiles"}
	if got := fileNames(listFiles(t, h)); !slices.Equal(got, want) {
		t.Errorf("file order = %v, want %v", got, want)
	}
}

func TestFileListOmitsNamesOutsideTheAllowedCharacters(t *testing.T) {
	h := filesHandler(t)
	for _, name := range []string{"good_area-1.0.mbtiles", "two words.mbtiles", "semi;colon.mbtiles", "ümlaut.mbtiles", "quote\".mbtiles"} {
		writeOutputFile(t, filepath.Join(h.outputDir, name), fileMetadata)
	}
	if got := fileNames(listFiles(t, h)); !slices.Equal(got, []string{"good_area-1.0.mbtiles"}) {
		t.Errorf("file names = %v, want only good_area-1.0.mbtiles", got)
	}
}

func TestFileListOmitsEntriesThatAreNotOutputFiles(t *testing.T) {
	h := filesHandler(t)
	writeOutputFile(t, filepath.Join(h.outputDir, "real.mbtiles"), fileMetadata)
	if err := os.WriteFile(filepath.Join(h.outputDir, "notes.txt"), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.outputDir, "folder.mbtiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(h.outputDir, "real.mbtiles"), filepath.Join(h.outputDir, "link.mbtiles")); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(listFiles(t, h)); !slices.Equal(got, []string{"real.mbtiles"}) {
		t.Errorf("file names = %v, want only real.mbtiles", got)
	}
}

func TestFileListIsEmptyWhenTheOutputDirectoryIsMissing(t *testing.T) {
	h := filesHandler(t)
	h.outputDir = filepath.Join(h.outputDir, "missing")
	if items := listFiles(t, h); len(items) != 0 {
		t.Errorf("GET /api/files = %+v, want an empty list", items)
	}
	if _, err := os.Stat(h.outputDir); !os.IsNotExist(err) {
		t.Errorf("stat the output directory after the list: %v, want it still missing", err)
	}
}

func TestFileRoutesReturnFileNotFoundForANameOutsideTheOutputDirectory(t *testing.T) {
	h := filesHandler(t)
	// The parent of a t.TempDir directory belongs to the test, so the test removes the outside file too.
	outside := filepath.Join(filepath.Dir(h.outputDir), "outside.mbtiles")
	writeOutputFile(t, outside, fileMetadata)
	if err := os.Symlink(outside, filepath.Join(h.outputDir, "escape.mbtiles")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(h.outputDir, "folder.mbtiles"), 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
	}{
		{"rejects an encoded parent path", "/api/files/..%2Fgo.mod"},
		{"rejects an encoded parent path to the outside file", "/api/files/..%2Foutside.mbtiles"},
		{"rejects a parent path", "/api/files/../outside.mbtiles"},
		{"rejects a lone parent element", "/api/files/.."},
		{"rejects an encoded lone parent element", "/api/files/%2E%2E"},
		{"rejects a lone dot", "/api/files/."},
		{"rejects a symbolic link that leaves the output directory", "/api/files/escape.mbtiles"},
		{"rejects a directory", "/api/files/folder.mbtiles"},
		{"rejects an unknown name", "/api/files/missing.mbtiles"},
		{"rejects an empty name", "/api/files/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serve(h, http.MethodGet, tt.path, "")
			checkError(t, rec.Result(), http.StatusNotFound, "file_not_found")
		})
	}
}

func TestFileRoutesRejectAMethodOtherThanGET(t *testing.T) {
	h := filesHandler(t)
	writeOutputFile(t, filepath.Join(h.outputDir, "area.mbtiles"), fileMetadata)
	for _, path := range []string{"/api/files", "/api/files/area.mbtiles"} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			t.Run(method+" "+path, func(t *testing.T) {
				checkError(t, serve(h, method, path, "").Result(), http.StatusMethodNotAllowed, "method_not_allowed")
			})
		}
	}
}

func TestACompletedJobOutputFileDownloadsWithTheListedSize(t *testing.T) {
	area := tilecalc.TileBounds{MinX: 300, MaxX: 301, MinY: 400, MaxY: 401, Zoom: 10}
	s := newJobServer(t, nil)
	rec := s.startJob(t, jobBody(area))
	if final := s.finalRecord(t, rec.ID); final.State != jobComplete {
		t.Fatalf("final state = %q, want complete", final.State)
	}

	items := listFiles(t, s.handler)
	if len(items) == 0 || items[0].Name != rec.OutputFile {
		t.Fatalf("GET /api/files names = %v, want %s first", fileNames(items), rec.OutputFile)
	}
	listed := items[0]
	if listed.InProgress || listed.Source != "Test Tiles" || listed.Format != "png" || listed.MinZoom != 10 || listed.MaxZoom != 10 {
		t.Errorf("listed file = %+v, want a finished png file of Test Tiles at zoom 10", listed)
	}

	checkDownload(t, s, listed)
}

// checkDownload fails the test when GET /api/files/{name} does not return the bytes of the listed output file as an attachment.
func checkDownload(t *testing.T, s *jobServer, listed outputFileItem) {
	t.Helper()
	resp := s.request(t, http.MethodGet, "/api/files/"+listed.Name, "")
	defer closeBody(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/files/%s status = %d, want 200", listed.Name, resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Disposition"), `attachment; filename="`+listed.Name+`"`; got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) != listed.Size {
		t.Errorf("downloaded %d bytes, want the listed size %d", len(body), listed.Size)
	}
	stored, err := os.ReadFile(filepath.Join(s.handler.outputDir, listed.Name))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(stored) {
		t.Error("the downloaded bytes differ from the output file")
	}
}

func TestFileListMarksTheOutputFileOfARunningJobInProgress(t *testing.T) {
	s := newJobServer(t, slowTable(slowArea, 100*time.Millisecond))
	rec := s.startJob(t, jobBody(slowArea))
	stream, r := s.openEvents(t, rec.ID)
	defer closeBody(stream)
	// A progress event shows that the engine opened the output file.
	readToProgress(t, r)

	inProgress := func() map[string]bool {
		got := map[string]bool{}
		for _, it := range listFiles(t, s.handler) {
			got[it.Name] = it.InProgress
		}
		return got
	}
	if got := inProgress(); !got[rec.OutputFile] {
		t.Errorf("in_progress while the job runs = %v, want %s true", got, rec.OutputFile)
	}

	closeBody(s.request(t, http.MethodDelete, "/api/jobs/"+rec.ID, ""))
	readAllEvents(t, r)
	got := inProgress()
	if inProg, listed := got[rec.OutputFile]; !listed || inProg {
		t.Errorf("in_progress after the job ends = %v, want %s listed and false", got, rec.OutputFile)
	}
	for name := range got {
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			t.Errorf("the list holds the SQLite side file %s", name)
		}
	}
}

// The web UI reads output_format to lock the output format of a resumed job.
func TestFileListNamesTheOutputFormatOfEachFile(t *testing.T) {
	for _, format := range []string{"mbtiles", "osmdroid", "gpkg"} {
		t.Run(format, func(t *testing.T) {
			s := newJobServer(t, nil)
			rec := s.startJob(t, strings.TrimSuffix(jobBody(fourTiles), "}")+`,"output_format":"`+format+`"}`)
			if final := s.finalRecord(t, rec.ID); final.State != jobComplete {
				t.Fatalf("job state = %q, error %q, want complete", final.State, final.Error)
			}
			items := listFiles(t, s.handler)
			if len(items) != 1 || items[0].Name != rec.OutputFile || items[0].OutputFormat != format {
				t.Errorf("GET /api/files = %+v, want %s with output_format %s", items, rec.OutputFile, format)
			}
		})
	}
}

func TestFileRoutesAcceptOnlyTheOutputFileExtensions(t *testing.T) {
	h := filesHandler(t)
	writeOutputFile(t, filepath.Join(h.outputDir, "real.mbtiles"), fileMetadata)
	// An MBTiles file with another extension is not an output file of a job.
	writeOutputFile(t, filepath.Join(h.outputDir, "copy.db"), fileMetadata)
	line := `{"record":{"id":"0123456789ab"}}` + "\n"
	if err := os.WriteFile(filepath.Join(h.outputDir, "jobs.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := fileNames(listFiles(t, h)); !slices.Equal(got, []string{"real.mbtiles"}) {
		t.Errorf("file names = %v, want only real.mbtiles", got)
	}
	for _, name := range []string{"jobs.jsonl", "copy.db"} {
		t.Run("rejects "+name, func(t *testing.T) {
			checkError(t, serve(h, http.MethodGet, "/api/files/"+name, "").Result(), http.StatusNotFound, "file_not_found")
			checkError(t, serve(h, http.MethodGet, "/api/files/"+name+"/tiles/10/0/0", "").Result(), http.StatusNotFound, "file_not_found")
		})
	}
	if rec := serve(h, http.MethodGet, "/api/files/real.mbtiles", ""); rec.Code != http.StatusOK {
		t.Errorf("GET /api/files/real.mbtiles status = %d, want 200", rec.Code)
	}
}
