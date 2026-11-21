package commands

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/datapackage"
	"github.com/crank-git/atak-map-downloader/internal/snapshot"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// zipEntries returns the content of each entry of the zip at path, by entry name.
func zipEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open the zip %s: %v", path, err)
	}
	defer func() { _ = r.Close() }() // The zip is read-only here, so a close error loses nothing.
	entries := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open the entry %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close() // The entry is read-only here, so a close error loses nothing.
		if err != nil {
			t.Fatalf("read the entry %s: %v", f.Name, err)
		}
		entries[f.Name] = string(data)
	}
	return entries
}

// manifestEntries returns the sorted zipEntry values of the manifest in entries.
func manifestEntries(t *testing.T, entries map[string]string) []string {
	t.Helper()
	var m struct {
		Contents []struct {
			ZipEntry string `xml:"zipEntry,attr"`
		} `xml:"Contents>Content"`
	}
	if err := xml.Unmarshal([]byte(entries[datapackage.ManifestEntry]), &m); err != nil {
		t.Fatalf("parse the manifest: %v", err)
	}
	var listed []string
	for _, c := range m.Contents {
		listed = append(listed, c.ZipEntry)
	}
	slices.Sort(listed)
	return listed
}

// checkDataPackage checks that the zip at path holds the manifest and exactly the files want, and that the manifest lists each file.
func checkDataPackage(t *testing.T, path string, want ...string) map[string]string {
	t.Helper()
	entries := zipEntries(t, path)
	var names []string
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	slices.Sort(want)
	if wantNames := slices.Sorted(slices.Values(append([]string{datapackage.ManifestEntry}, want...))); !slices.Equal(names, wantNames) {
		t.Errorf("zip entries = %v, want %v", names, wantNames)
	}
	if listed := manifestEntries(t, entries); !slices.Equal(listed, want) {
		t.Errorf("manifest zipEntry values = %v, want %v", listed, want)
	}
	return entries
}

func TestDownloadWithDataPackageAndIncludeSourceWritesTheZip(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "hood.mbtiles")
	stderr, err := runDownloadInto(t, srv, path, []string{"--data-package", "--include-source", "--package-name", "Mt Hood SAR"})
	if err != nil {
		t.Fatalf("download error = %v, stderr %q", err, stderr)
	}

	entries := checkDataPackage(t, filepath.Join(dir, "Mt_Hood_SAR.zip"), "hood.mbtiles", "tls.xml")
	if !strings.Contains(entries["tls.xml"], "<name>Test TLS</name>") {
		t.Errorf("entry tls.xml = %q, want the source file of Test TLS", entries["tls.xml"])
	}
	if !strings.HasPrefix(entries["hood.mbtiles"], "SQLite format 3\x00") {
		t.Errorf("entry hood.mbtiles is not an SQLite file")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Stat(%s) error = %v, want the unzipped output file removed", path, err)
	}
}

func TestDownloadWithDataPackageWithoutIncludeSourceZipsOnlyTheOutputFile(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	stderr, err := runDownloadInto(t, srv, filepath.Join(dir, "hood.gpkg"), []string{"--format", "gpkg", "--data-package", "--package-name", "hood"})
	if err != nil {
		t.Fatalf("download error = %v, stderr %q", err, stderr)
	}
	checkDataPackage(t, filepath.Join(dir, "hood.zip"), "hood.gpkg")
}

func TestDownloadRejectsDataPackageFlagsThatAreNotValid(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{"rejects a package name with a slash", []string{"--data-package", "--package-name", "a/b"}, "invalid_package_name"},
		{"rejects a package name of 65 characters", []string{"--data-package", "--package-name", strings.Repeat("a", 65)}, "invalid_package_name"},
		{"rejects a data package without a package name", []string{"--data-package"}, "invalid_package_name"},
		{"rejects --include-source without --data-package", []string{"--include-source"}, "--include-source needs --data-package"},
		{"rejects --package-name without --data-package", []string{"--package-name", "a"}, "--package-name needs --data-package"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			path := filepath.Join(t.TempDir(), "out.mbtiles")
			stderr, err := runDownloadInto(t, srv, path, tt.args)
			if got := ExitCode(err); got != 1 {
				t.Errorf("ExitCode(%v) = %d, want 1", err, got)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to hold %q", stderr, tt.wantStderr)
			}
			if n := srv.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
		})
	}
}

func TestDownloadRejectsADataPackageAtTheOutputPath(t *testing.T) {
	srv := testtiles.New(t, nil)
	path := filepath.Join(t.TempDir(), "hood.zip")
	stderr, err := runDownloadInto(t, srv, path, []string{"--data-package", "--package-name", "hood"})
	if got := ExitCode(err); got != 1 || !strings.Contains(stderr, "invalid_package_name") {
		t.Errorf("ExitCode(%v) = %d, stderr %q; want 1 and invalid_package_name", err, got, stderr)
	}
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0", n)
	}
}

func TestDownloadWithDataPackageAndResumeResumesTheOutputFileAndRebuildsTheZip(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "hood.mbtiles")
	writeOutputFile(t, path, map[string]string{"atakdl_source": "Test TLS"}, oneTile)
	zipPath := filepath.Join(dir, "hood.zip")
	if err := os.WriteFile(zipPath, []byte("old zip"), 0o600); err != nil {
		t.Fatal(err)
	}

	stderr, err := runDownloadInto(t, srv, path, []string{"--resume", "--data-package", "--package-name", "hood"})
	if err != nil {
		t.Fatalf("download --resume --data-package error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0, because the output file holds the tile", n)
	}
	checkDataPackage(t, zipPath, "hood.mbtiles")
}

func TestDownloadWithDataPackageRefusesAnExistingZip(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "hood.zip")
	if err := os.WriteFile(zipPath, []byte("old zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runDownloadInto(t, srv, filepath.Join(dir, "hood.mbtiles"), []string{"--data-package", "--package-name", "hood"})
	if got := ExitCode(err); got != exitOutputConflict {
		t.Errorf("ExitCode(%v) = %d, want %d", err, got, exitOutputConflict)
	}
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0", n)
	}
	if got := string(readFile(t, zipPath)); got != "old zip" {
		t.Errorf("zip after the refused job = %q, want it unchanged", got)
	}
}

func TestDownloadWithDataPackageAndOverwriteReplacesTheZip(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "hood.zip")
	if err := os.WriteFile(zipPath, []byte("old zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr, err := runDownloadInto(t, srv, filepath.Join(dir, "hood.mbtiles"), []string{"--overwrite", "--data-package", "--package-name", "hood"})
	if err != nil {
		t.Fatalf("download --overwrite --data-package error = %v, stderr %q", err, stderr)
	}
	checkDataPackage(t, zipPath, "hood.mbtiles")
}

func TestSourceFileReadsASnapshotSourceFileFromTheSnapshot(t *testing.T) {
	const name = "maps/base/bing/bing_road.xml"
	want, err := fs.ReadFile(snapshot.FS(), name)
	if err != nil {
		t.Fatal(err)
	}
	f := sourceFile(&xmlparser.MapSource{Origin: xmlparser.OriginSnapshot, File: name})
	if f.Entry != "bing_road.xml" {
		t.Errorf("Entry = %q, want bing_road.xml", f.Entry)
	}
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("Open error = %v", err)
	}
	defer func() { _ = rc.Close() }() // The file is read-only here, so a close error loses nothing.
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("Open returned %d bytes that differ from the snapshot file", len(got))
	}
}

// dataPackageJob is the body of a job of fourTiles in a data package named "Mt Hood SAR", with the source file.
func dataPackageJob(extra string) string {
	return strings.TrimSuffix(jobBody(fourTiles), "}") + `,"data_package":true,"include_source":true,"package_name":"Mt Hood SAR"` + extra + "}"
}

func TestJobAPIWritesADataPackage(t *testing.T) {
	s := newJobServer(t, nil)
	rec := s.startJob(t, dataPackageJob(""))
	inner := rec.OutputFile
	final := s.finalRecord(t, rec.ID)
	if final.State != jobComplete {
		t.Fatalf("job state = %q, error %q, want complete", final.State, final.Error)
	}
	if final.OutputFile != "Mt_Hood_SAR.zip" {
		t.Errorf("output_file = %q, want Mt_Hood_SAR.zip", final.OutputFile)
	}
	checkDataPackage(t, filepath.Join(s.handler.outputDir, "Mt_Hood_SAR.zip"), inner, "tiles.xml")
	if _, err := os.Stat(filepath.Join(s.handler.outputDir, inner)); !os.IsNotExist(err) {
		t.Errorf("Stat(%s) error = %v, want the unzipped output file removed", inner, err)
	}

	items := listFiles(t, s.handler)
	if len(items) != 1 || items[0].Name != "Mt_Hood_SAR.zip" || items[0].Format != "data_package" || items[0].OutputFormat != "" {
		t.Errorf("GET /api/files = %+v, want Mt_Hood_SAR.zip with format data_package and no output_format", items)
	}
	if code := serve(s.handler, http.MethodGet, "/api/files/Mt_Hood_SAR.zip/tiles/10/300/400", "").Code; code != http.StatusNotFound {
		t.Errorf("tile route status for the data package = %d, want 404", code)
	}
}

func TestJobAPIEndsWithPackageFailedAndKeepsTheOutputFileWhenTheZipFails(t *testing.T) {
	s := newJobServer(t, nil)
	// A directory with a file in it is a target that no rename replaces, so the zip step fails.
	blocker := filepath.Join(s.handler.outputDir, "Mt_Hood_SAR.zip")
	if err := os.MkdirAll(filepath.Join(blocker, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := s.startJob(t, dataPackageJob(`,"if_exists":"resume"`))
	final := s.finalRecord(t, rec.ID)
	if final.State != jobFailed || !strings.HasPrefix(final.Error, "package_failed") {
		t.Errorf("job state = %q, error %q, want failed with package_failed", final.State, final.Error)
	}
	if _, err := os.Stat(filepath.Join(s.handler.outputDir, rec.OutputFile)); err != nil {
		t.Errorf("Stat(%s) error = %v, want the output file kept", rec.OutputFile, err)
	}
}

func TestJobAPIRejectsDataPackageFieldsThatAreNotValid(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{"rejects a package name with a slash", `,"data_package":true,"package_name":"a/b"`},
		{"rejects a data package without a package name", `,"data_package":true`},
		{"rejects a package name of 65 characters", `,"data_package":true,"package_name":"` + strings.Repeat("a", 65) + `"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newJobServer(t, nil)
			body := strings.TrimSuffix(jobBody(fourTiles), "}") + tt.field + "}"
			checkError(t, s.request(t, http.MethodPost, "/api/jobs", body), http.StatusBadRequest, "invalid_package_name")
			if n := s.tiles.TotalRequests(); n != 0 {
				t.Errorf("server got %d tile requests, want 0", n)
			}
		})
	}
}

func TestJobAPIRefusesAnExistingDataPackage(t *testing.T) {
	s := newJobServer(t, nil)
	zipPath := filepath.Join(s.handler.outputDir, "Mt_Hood_SAR.zip")
	if err := os.WriteFile(zipPath, []byte("old zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkError(t, s.request(t, http.MethodPost, "/api/jobs", dataPackageJob("")), http.StatusConflict, "output_exists")
	if got := string(readFile(t, zipPath)); got != "old zip" {
		t.Errorf("zip after the refused job = %q, want it unchanged", got)
	}
}

func TestPackageOutputReturnsAContextThatALaterCancelDoesNotEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hood.mbtiles")
	writeOutputFile(t, path, map[string]string{"atakdl_source": "Test TLS"}, oneTile)
	ctx, cancel := context.WithCancel(t.Context())
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)

	fctx, got, err := packageOutput(ctx, cmd, path, &packageOptions{name: "hood"}, &xmlparser.MapSource{}, nil)
	if err != nil {
		t.Fatalf("packageOutput error = %v", err)
	}
	if want := filepath.Join(dir, "hood.zip"); got != want {
		t.Errorf("packageOutput path = %q, want %q", got, want)
	}
	// The zip is complete and the output file is gone, so a later cancel must not print the resume line.
	cancel()
	if fctx.Err() != nil {
		t.Errorf("context after the cancel = %v, want no error, because the job is complete", fctx.Err())
	}
}

func TestDownloadTrimsThePackageName(t *testing.T) {
	tests := []struct {
		name    string
		pkgName string
		wantZip string
	}{
		{"trims the spaces around the package name", "  hood  ", "hood.zip"},
		{"keeps the inner spaces of the package name", " Mt Hood ", "Mt_Hood.zip"},
		{"rejects a package name of spaces only", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testtiles.New(t, nil)
			dir := t.TempDir()
			stderr, err := runDownloadInto(t, srv, filepath.Join(dir, "hood.mbtiles"), []string{"--data-package", "--package-name", tt.pkgName})
			if tt.wantZip == "" {
				if got := ExitCode(err); got != 1 || !strings.Contains(stderr, "invalid_package_name") {
					t.Errorf("ExitCode(%v) = %d, stderr %q; want 1 and invalid_package_name", err, got, stderr)
				}
				if n := srv.TotalRequests(); n != 0 {
					t.Errorf("server got %d tile requests, want 0", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("download error = %v, stderr %q", err, stderr)
			}
			checkDataPackage(t, filepath.Join(dir, tt.wantZip), "hood.mbtiles")
		})
	}
}

func TestJobAPITrimsThePackageName(t *testing.T) {
	s := newJobServer(t, nil)
	body := strings.TrimSuffix(jobBody(fourTiles), "}") + `,"data_package":true,"package_name":"  Mt Hood  "}`
	rec := s.startJob(t, body)
	if final := s.finalRecord(t, rec.ID); final.State != jobComplete || final.OutputFile != "Mt_Hood.zip" {
		t.Errorf("final record = state %q, output_file %q; want complete and Mt_Hood.zip", final.State, final.OutputFile)
	}
}

func TestJobAPIRejectsAPackageNameOfSpacesOnly(t *testing.T) {
	s := newJobServer(t, nil)
	body := strings.TrimSuffix(jobBody(fourTiles), "}") + `,"data_package":true,"package_name":"   "}`
	checkError(t, s.request(t, http.MethodPost, "/api/jobs", body), http.StatusBadRequest, "invalid_package_name")
	if n := s.tiles.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0", n)
	}
}

// triangleGeoJSON returns a GeoJSON FeatureCollection with one polygon over the lower left half of the tiles of tb.
func triangleGeoJSON(tb tilecalc.TileBounds) string {
	b := boundsOf(tb)
	return fmt.Sprintf(`{"type":"FeatureCollection","features":[{"type":"Feature","properties":{},`+
		`"geometry":{"type":"Polygon","coordinates":[[[%f,%f],[%f,%f],[%f,%f],[%f,%f]]]}}]}`,
		b[0], b[1], b[2], b[1], b[0], b[3], b[0], b[1])
}

func TestDownloadResumeWithDataPackageReadsTheOutputFileOutOfTheZip(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	areaFile := filepath.Join(dir, "area.geojson")
	square := tilecalc.TileBounds{MinX: 300, MaxX: 303, MinY: 400, MaxY: 403, Zoom: 10}
	if err := os.WriteFile(areaFile, []byte(triangleGeoJSON(square)), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hood.gpkg")
	zipPath := filepath.Join(dir, "hood.zip")
	pkgArgs := []string{"--output", path, "--format", "gpkg", "--data-package", "--package-name", "hood"}
	if stderr, err := runDownloadArgs(t, srv, append([]string{"--area", areaFile, "--min-zoom", "10", "--max-zoom", "10"}, pkgArgs...)...); err != nil {
		t.Fatalf("first download error = %v, stderr %q", err, stderr)
	}
	first := srv.TotalRequests()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s) error = %v, want the unzipped output file removed", path, err)
	}

	// The resume has no --area and no --bbox, so it reads the areas from the output file in the zip.
	if stderr, err := runDownloadArgs(t, srv, append([]string{"--resume"}, pkgArgs...)...); err != nil {
		t.Fatalf("download --resume --data-package error = %v, stderr %q", err, stderr)
	}
	if n := srv.TotalRequests(); n != first {
		t.Errorf("server got %d tile requests in total, want %d, because the zip holds every tile", n, first)
	}
	checkDataPackage(t, zipPath, "hood.gpkg")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Stat(%s) error = %v, want the unzipped output file removed after the resume", path, err)
	}
}

func TestDownloadResumeWithDataPackageRefusesAZipWithoutTheOutputFile(t *testing.T) {
	srv := testtiles.New(t, nil)
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "hood.zip")
	if err := os.WriteFile(zipPath, []byte("old zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := runDownloadInto(t, srv, filepath.Join(dir, "hood.mbtiles"), []string{"--resume", "--data-package", "--package-name", "hood"})
	if got := ExitCode(err); got != exitOutputConflict {
		t.Errorf("ExitCode(%v) = %d, want %d", err, got, exitOutputConflict)
	}
	if n := srv.TotalRequests(); n != 0 {
		t.Errorf("server got %d tile requests, want 0", n)
	}
	if got := string(readFile(t, zipPath)); got != "old zip" {
		t.Errorf("zip after the refused job = %q, want it unchanged", got)
	}
}
