package xmlparser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverSourcesMarksOnlySnapshotSourcesUnderMapsOverlaysAsOverlays(t *testing.T) {
	dir := t.TempDir()
	// A synced copy of the snapshot layout shows that a user source file is never an overlay.
	if err := os.MkdirAll(filepath.Join(dir, "atak-maps", "maps", "overlays"), 0o750); err != nil {
		t.Fatal(err)
	}
	createTestXML(t, dir, "atak-maps/maps/overlays/charlie.xml", sourceXML("Charlie", "https://user.test/c/{$z}/{$x}/{$y}.png"))

	sources := NewWithSnapshot(testSnapshot(), dir).DiscoverSources()

	tests := []struct {
		name    string
		overlay bool
		file    string
	}{
		{"Alpha", false, "maps/base/alpha.xml"},
		{"Bravo", true, "maps/overlays/bravo.xml"},
		{"Charlie", false, filepath.Join(dir, "atak-maps", "maps", "overlays", "charlie.xml")},
	}
	for _, tt := range tests {
		s := sources[tt.name]
		if s == nil {
			t.Errorf("DiscoverSources() has no source %q", tt.name)
			continue
		}
		if s.Overlay != tt.overlay || s.File != tt.file {
			t.Errorf("%s = {Overlay %v, File %q}, want {%v, %q}", tt.name, s.Overlay, s.File, tt.overlay, tt.file)
		}
	}
}
