package xmlparser

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/snapshot"
)

func TestParseSplitsServerPartsOnWhitespaceAndCommas(t *testing.T) {
	tests := []struct {
		name  string
		parts string
		want  []string
	}{
		{"splits on spaces", "a b c", []string{"a", "b", "c"}},
		{"splits on commas", "a,b,c", []string{"a", "b", "c"}},
		{"splits on commas with spaces", "a, b ,c", []string{"a", "b", "c"}},
		{"splits on tabs and line ends", "a\tb\nc", []string{"a", "b", "c"}},
		{"gives no part for an empty element", " , ", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := `<customMapSource><name>S</name><serverParts>` + tt.parts +
				`</serverParts><url>https://{$serverpart}.s.test/{$z}/{$x}/{$y}.png</url></customMapSource>`
			source, err := parse([]byte(data), OriginUser)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !slices.Equal(source.ServerParts, tt.want) {
				t.Errorf("ServerParts = %q, want %q", source.ServerParts, tt.want)
			}
		})
	}
}

func TestParseReadsInvertYCoordinate(t *testing.T) {
	tests := []struct {
		name    string
		element string
		want    bool
	}{
		{"reads true", "<invertYCoordinate>true</invertYCoordinate>", true},
		{"reads false", "<invertYCoordinate>false</invertYCoordinate>", false},
		{"reads a missing element as false", "", false},
		// The XML reference says that only the exact string true turns the inversion on.
		{"reads TRUE as false", "<invertYCoordinate>TRUE</invertYCoordinate>", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := `<customMapSource><name>S</name>` + tt.element + `<url>https://s.test/{$z}/{$x}/{$y}.png</url></customMapSource>`
			source, err := parse([]byte(data), OriginUser)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if source.InvertYCoordinate != tt.want {
				t.Errorf("InvertYCoordinate = %v, want %v", source.InvertYCoordinate, tt.want)
			}
		})
	}
}

func TestParseReadsAMissingTileUpdateAsNone(t *testing.T) {
	source, err := parse([]byte(sourceXML("S", "https://s.test/{$z}/{$x}/{$y}.png")), OriginUser)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if source.TileUpdate != "None" {
		t.Errorf("TileUpdate = %q, want None", source.TileUpdate)
	}
}

func TestParseChecksThePlaceholdersOfTheURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr string
	}{
		{"accepts the XYZ placeholders", "https://s.test/{$z}/{$x}/{$y}.png", ""},
		{"accepts the quadkey placeholder", "https://s.test/q/{$q}.png", ""},
		{"accepts the server part placeholder", "https://{$serverpart}.s.test/{$z}/{$x}/{$y}.png", ""},
		{"skips an unknown placeholder", "https://s.test/{$z}/{$x}/{$y}.png?key={$apikey}", "{$apikey}"},
		{"skips a placeholder in another letter case", "https://s.test/{$Z}/{$x}/{$y}.png", "{$Z}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(sourceXML("S", tt.url)), OriginUser)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("parse: %v, want no error", err)
				}
				return
			}
			se, ok := err.(*SkipError)
			if !ok || se.Reason != SkipUnknownPlaceholder || !strings.Contains(se.Detail, tt.wantErr) {
				t.Errorf("parse error = %v, want %s with %s in the detail", err, SkipUnknownPlaceholder, tt.wantErr)
			}
		})
	}
}

func TestDiscoverSourcesRecordsAnUnknownPlaceholderAsASkippedSourceFile(t *testing.T) {
	dir := t.TempDir()
	createTestXML(t, dir, "key.xml", sourceXML("Key", "https://s.test/{$z}/{$x}/{$y}.png?key={$apikey}"))

	parser := New(dir)
	if sources := parser.DiscoverSources(); len(sources) != 0 {
		t.Errorf("DiscoverSources() = %v, want no map source", sources)
	}
	skipped := parser.SkippedSources()
	if len(skipped) != 1 || skipped[0].Reason != SkipUnknownPlaceholder || !strings.Contains(skipped[0].Detail, "{$apikey}") {
		t.Errorf("SkippedSources() = %+v, want key.xml with reason %s", skipped, SkipUnknownPlaceholder)
	}
}

func TestDiscoverSourcesLoadsOrSkipsEverySnapshotSourceFile(t *testing.T) {
	fsys := snapshot.FS()
	files := 0
	if err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".xml") {
			files++
		}
		return err
	}); err != nil {
		t.Fatalf("walk the snapshot: %v", err)
	}

	parser := NewWithSnapshot(fsys, "")
	sources := parser.DiscoverSources()
	skipped := parser.SkippedSources()
	if len(sources)+len(skipped) != files {
		t.Errorf("snapshot gives %d map sources and %d skipped files, want %d source files in total", len(sources), len(skipped), files)
	}
	// Every snapshot placeholder is in the XML reference, so no snapshot file has an unknown placeholder.
	for _, s := range skipped {
		if s.Reason == SkipUnknownPlaceholder {
			t.Errorf("snapshot file %s is skipped with %s: %s", s.Path, s.Reason, s.Detail)
		}
	}
}

func TestMapSourceFormatURL(t *testing.T) {
	tests := []struct {
		name   string
		source MapSource
		x, y   int
		z      int
		want   string
	}{
		{"sends the XYZ row", MapSource{URL: "https://s.test/{$z}/{$x}/{$y}.png"}, 3, 1, 2, "https://s.test/2/3/1.png"},
		{"sends the TMS row for an inverted map source", MapSource{URL: "https://s.test/{$z}/{$x}/{$y}.png", InvertYCoordinate: true}, 3, 1, 2, "https://s.test/2/3/2.png"},
		{"keeps the quadkey of the XYZ row for an inverted map source", MapSource{URL: "https://s.test/{$q}.png", InvertYCoordinate: true}, 6, 2, 3, "https://s.test/130.png"},
		{"uses the server part of the tile key", MapSource{URL: "https://{$serverpart}.s.test/{$z}/{$x}/{$y}.png", ServerParts: []string{"a", "b", "c"}}, 3, 1, 2, "https://b.s.test/2/3/1.png"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.source.FormatURL(tt.x, tt.y, tt.z, ""); got != tt.want {
				t.Errorf("FormatURL(%d, %d, %d) = %q, want %q", tt.x, tt.y, tt.z, got, tt.want)
			}
		})
	}
}

func TestMapSourceGetServerPartUsesTheTileKey(t *testing.T) {
	source := &MapSource{ServerParts: []string{"a", "b", "c"}}
	tests := []struct {
		x, y int
		want string
	}{
		{0, 0, "a"},
		{1, 0, "b"},
		{0, 1, "b"},
		{1, 1, "c"},
		{2, 1, "a"},
	}
	for _, tt := range tests {
		for range 2 {
			if got := source.GetServerPart(tt.x, tt.y); got != tt.want {
				t.Errorf("GetServerPart(%d, %d) = %q, want %q", tt.x, tt.y, got, tt.want)
			}
		}
	}
	if got := (&MapSource{}).GetServerPart(1, 1); got != "" {
		t.Errorf("GetServerPart with no server parts = %q, want an empty string", got)
	}
}
