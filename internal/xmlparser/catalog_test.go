package xmlparser

import (
	"fmt"
	"os"
	"path/filepath"
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

	sources := NewWithSnapshot(testSnapshot(), dir).DiscoverSources(false)

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
	// The second call reads the cache, so it must return the snapshot too.
	for i := range 2 {
		sources := parser.DiscoverSources(false)
		if len(sources) != 2 || sources["Alpha"] == nil || sources["Bravo"] == nil {
			t.Errorf("call %d: DiscoverSources() = %v, want Alpha and Bravo", i+1, sources)
		}
	}
}

func TestDiscoverSourcesWithoutASnapshotLoadsOnlyTheUserSourceDirectory(t *testing.T) {
	dir := t.TempDir()
	createTestXML(t, dir, "charlie.xml", sourceXML("Charlie", "https://user.test/c/{$z}/{$x}/{$y}.png"))

	sources := New(dir).DiscoverSources(false)

	if len(sources) != 1 || sources["Charlie"] == nil || sources["Charlie"].Origin != OriginUser {
		t.Errorf("DiscoverSources() = %v, want only Charlie with origin user", sources)
	}
}
