package xmlparser

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func sourceXML(name, url string) string {
	return fmt.Sprintf(`<?xml version="1.0"?>
<customMapSource>
    <name>%s</name>
    <url>%s</url>
</customMapSource>`, name, url)
}

func testSnapshot() fstest.MapFS {
	return fstest.MapFS{
		"maps/base/alpha.xml":     {Data: []byte(sourceXML("Alpha", "https://snapshot.test/a/{$z}/{$x}/{$y}.png"))},
		"maps/overlays/bravo.xml": {Data: []byte(sourceXML("Bravo", "https://snapshot.test/b/{$z}/{$x}/{$y}.png"))},
		"LICENSE":                 {Data: []byte("MIT License")},
	}
}

func TestDiscoverSourcesLetsAUserSourceReplaceTheSnapshotSourceOfTheSameName(t *testing.T) {
	dir := t.TempDir()
	// The sub-directory shows that the parser reads the user source directory recursively.
	if err := os.MkdirAll(filepath.Join(dir, "atak-maps"), 0o750); err != nil {
		t.Fatal(err)
	}
	createTestXML(t, dir, "atak-maps/bravo.xml", sourceXML("Bravo", "https://user.test/b/{$z}/{$x}/{$y}.png"))
	createTestXML(t, dir, "charlie.xml", sourceXML("Charlie", "https://user.test/c/{$z}/{$x}/{$y}.png"))

	sources := NewWithSnapshot(testSnapshot(), dir).DiscoverSources()

	tests := []struct {
		name   string
		url    string
		origin string
	}{
		{"Alpha", "https://snapshot.test/a/{$z}/{$x}/{$y}.png", OriginSnapshot},
		{"Bravo", "https://user.test/b/{$z}/{$x}/{$y}.png", OriginUser},
		{"Charlie", "https://user.test/c/{$z}/{$x}/{$y}.png", OriginUser},
	}
	if len(sources) != len(tests) {
		t.Errorf("DiscoverSources() returned %d sources, want %d", len(sources), len(tests))
	}
	for _, tt := range tests {
		s := sources[tt.name]
		if s == nil {
			t.Errorf("DiscoverSources() has no source %q", tt.name)
			continue
		}
		if s.URL != tt.url || s.Origin != tt.origin || s.Kind != KindTile {
			t.Errorf("%s = {URL %q, Origin %q, Kind %q}, want {%q, %q, %q}", tt.name, s.URL, s.Origin, s.Kind, tt.url, tt.origin, KindTile)
		}
	}
}

func TestDiscoverSourcesLoadsTheSnapshotWhenTheUserSourceDirectoryIsMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	parser := NewWithSnapshot(testSnapshot(), missing)
	// Each call scans again, so the second call must return the snapshot too.
	for i := range 2 {
		sources := parser.DiscoverSources()
		if len(sources) != 2 || sources["Alpha"] == nil || sources["Bravo"] == nil {
			t.Errorf("call %d: DiscoverSources() = %v, want Alpha and Bravo", i+1, sources)
		}
	}
}

func TestDiscoverSourcesWithoutASnapshotLoadsOnlyTheUserSourceDirectory(t *testing.T) {
	dir := t.TempDir()
	createTestXML(t, dir, "charlie.xml", sourceXML("Charlie", "https://user.test/c/{$z}/{$x}/{$y}.png"))

	sources := New(dir).DiscoverSources()

	if len(sources) != 1 || sources["Charlie"] == nil || sources["Charlie"].Origin != OriginUser {
		t.Errorf("DiscoverSources() = %v, want only Charlie with origin user", sources)
	}
}

func TestDiscoverSourcesRecordsEachSkippedSourceFileWithItsReason(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "good"), 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"a_dup.xml":     sourceXML("Dup", "https://user.test/first/{$z}/{$x}/{$y}.png"),
		"b_dup.xml":     sourceXML("Dup", "https://user.test/second/{$z}/{$x}/{$y}.png"),
		"broken.xml":    "<customMapSource><name>Broken",
		"noname.xml":    sourceXML(" ", "https://user.test/n/{$z}/{$x}/{$y}.png"),
		"nourl.xml":     sourceXML("No URL", ""),
		"wms.xml":       `<?xml version="1.0"?><customWmsMapSource><name>WMS</name><url>https://user.test/wms</url></customWmsMapSource>`,
		"multi.xml":     `<?xml version="1.0"?><customMultiLayerMapSource><name>Multi</name></customMultiLayerMapSource>`,
		"readme.txt":    "not a source file",
		"good/good.xml": sourceXML("Good", "https://user.test/g/{$z}/{$x}/{$y}.png"),
	}
	for name, content := range files {
		createTestXML(t, dir, name, content)
	}

	parser := New(dir)
	sources := parser.DiscoverSources()
	skipped := parser.SkippedSources()

	if len(sources) != 2 || sources["Good"] == nil || sources["Dup"] == nil {
		t.Errorf("DiscoverSources() = %v, want Good and Dup", sources)
	}
	// The walk is in lexical path order, so a_dup.xml loads and b_dup.xml is the duplicate.
	if s := sources["Dup"]; s != nil && s.URL != "https://user.test/first/{$z}/{$x}/{$y}.png" {
		t.Errorf("Dup URL = %q, want the URL of a_dup.xml", s.URL)
	}
	tests := []struct {
		file   string
		reason string
		detail string
	}{
		{"b_dup.xml", SkipDuplicateName, "a_dup.xml"},
		{"broken.xml", SkipParseError, "EOF"},
		{"multi.xml", SkipParseError, "customMultiLayerMapSource"},
		{"noname.xml", SkipMissingName, "<name>"},
		{"nourl.xml", SkipMissingURL, "<url>"},
		{"wms.xml", SkipParseError, "customWmsMapSource"},
	}
	if len(skipped) != len(tests) {
		t.Fatalf("SkippedSources() = %+v, want %d entries", skipped, len(tests))
	}
	for i, tt := range tests {
		got := skipped[i]
		want := filepath.Join(dir, tt.file)
		if got.Path != want || got.Reason != tt.reason || !strings.Contains(got.Detail, tt.detail) {
			t.Errorf("SkippedSources()[%d] = %+v, want path %s, reason %s and a detail with %q", i, got, want, tt.reason, tt.detail)
		}
	}
}

func TestDiscoverSourcesLoadsADuplicateNameInAnotherDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	createTestXML(t, dir, "alpha.xml", sourceXML("Alpha", "https://user.test/top/{$z}/{$x}/{$y}.png"))
	createTestXML(t, dir, "sub/alpha.xml", sourceXML("Alpha", "https://user.test/sub/{$z}/{$x}/{$y}.png"))

	parser := NewWithSnapshot(testSnapshot(), dir)
	sources := parser.DiscoverSources()

	if got := parser.SkippedSources(); len(got) != 0 {
		t.Errorf("SkippedSources() = %+v, want none", got)
	}
	if s := sources["Alpha"]; s == nil || s.Origin != OriginUser {
		t.Errorf("Alpha = %+v, want the user map source", s)
	}
}

func TestDiscoverSourcesReadsASourceFileThatChangedAfterTheLastScan(t *testing.T) {
	dir := t.TempDir()
	createTestXML(t, dir, "alpha.xml", sourceXML("Alpha", "https://user.test/old/{$z}/{$x}/{$y}.png"))
	parser := New(dir)
	if s := parser.DiscoverSources()["Alpha"]; s == nil {
		t.Fatal("first DiscoverSources() has no Alpha")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}

	createTestXML(t, dir, "alpha.xml", sourceXML("Alpha", "https://user.test/new/{$z}/{$x}/{$y}.png"))
	// An edit in place can leave the directory time unchanged, so the test sets it back.
	if err := os.Chtimes(dir, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	if s := parser.DiscoverSources()["Alpha"]; s == nil || s.URL != "https://user.test/new/{$z}/{$x}/{$y}.png" {
		t.Errorf("second DiscoverSources() Alpha = %+v, want the new URL", s)
	}
}

func TestDiscoverSourcesLetsAUserFileReplaceASyncedFileOfTheSameName(t *testing.T) {
	// fs.WalkDir reads in lexical order, so the user file names sort before and after the synced directory.
	tests := []struct {
		name   string
		synced string
		user   string
	}{
		{"a user file that sorts before atak-maps", "atak-maps/maps/bravo.xml", "Mine.xml"},
		{"a user file that sorts after atak-maps", "atak-maps/maps/bravo.xml", "zulu.xml"},
		{"a user file in a sub-directory", "atak-maps/maps/bravo.xml", "mine/bravo.xml"},
		{"a user file that sorts before a kept sync directory", ".atak-maps-kept-1/atak-maps/maps/bravo.xml", "-mine.xml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, rel := range []string{tt.synced, tt.user} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			createTestXML(t, dir, tt.synced, sourceXML("Bravo", "https://synced.test/b/{$z}/{$x}/{$y}.png"))
			createTestXML(t, dir, tt.user, sourceXML("Bravo", "https://user.test/b/{$z}/{$x}/{$y}.png"))

			parser := NewWithSnapshot(testSnapshot(), dir)
			if s := parser.DiscoverSources()["Bravo"]; s == nil || s.URL != "https://user.test/b/{$z}/{$x}/{$y}.png" {
				t.Errorf("Bravo = %+v, want the map source of %s", s, tt.user)
			}
			if got := parser.SkippedSources(); len(got) != 0 {
				t.Errorf("SkippedSources() = %+v, want none", got)
			}
		})
	}
}

func TestDiscoverSourcesLetsASyncedFileReplaceTheSnapshotFileOfTheSameName(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "atak-maps", "maps"), 0o750); err != nil {
		t.Fatal(err)
	}
	createTestXML(t, dir, "atak-maps/maps/alpha.xml", sourceXML("Alpha", "https://synced.test/a/{$z}/{$x}/{$y}.png"))

	s := NewWithSnapshot(testSnapshot(), dir).DiscoverSources()["Alpha"]
	if s == nil || s.URL != "https://synced.test/a/{$z}/{$x}/{$y}.png" {
		t.Errorf("Alpha = %+v, want the synced map source", s)
	}
}
