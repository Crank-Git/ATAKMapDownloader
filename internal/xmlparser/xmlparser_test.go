package xmlparser

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMapSource_FormatURL(t *testing.T) {
	source := &MapSource{
		Name:     "Test",
		URL:      "https://example.com/{$z}/{$x}/{$y}.png",
		MinZoom:  0,
		MaxZoom:  18,
		TileType: "png",
	}
	url := source.FormatURL(123, 456, 10, "")
	if url != "https://example.com/10/123/456.png" {
		t.Errorf("expected https://example.com/10/123/456.png, got %s", url)
	}
}

func TestMapSource_FormatURL_BingQuadkey(t *testing.T) {
	source := &MapSource{
		Name:     "Bing",
		URL:      "https://example.com/tiles/h{$q}?g=761",
		MinZoom:  0,
		MaxZoom:  18,
		TileType: "png",
	}
	url := source.FormatURL(1, 1, 2, "")
	if url == "" || len(url) < 10 {
		t.Errorf("expected non-empty URL with quadkey, got %s", url)
	}
	if !contains(url, "h") {
		t.Errorf("expected quadkey in URL, got %s", url)
	}
}

func TestXMLParser_DiscoverSources(t *testing.T) {
	dir := t.TempDir()
	createTestXML(t, dir, "test.xml", `<?xml version="1.0"?>
<customMapSource>
    <name>Test Source</name>
    <minZoom>0</minZoom>
    <maxZoom>18</maxZoom>
    <tileType>png</tileType>
    <url>https://example.com/{$z}/{$x}/{$y}.png</url>
</customMapSource>`)

	parser := New(dir)
	sources := parser.DiscoverSources()
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
	s, ok := sources["Test Source"]
	if !ok {
		t.Fatal("expected Test Source")
	}
	if s.URL != "https://example.com/{$z}/{$x}/{$y}.png" {
		t.Errorf("unexpected URL: %s", s.URL)
	}
}

func TestXMLParser_ValidateSource(t *testing.T) {
	parser := New("")
	valid := &MapSource{
		Name:     "Valid",
		URL:      "https://a.com/{$z}/{$x}/{$y}.png",
		MinZoom:  0,
		MaxZoom:  18,
		TileType: "png",
	}
	if errs := parser.ValidateSource(valid); len(errs) != 0 {
		t.Errorf("expected no errors, got %v", errs)
	}

	invalid := &MapSource{
		Name:     "",
		URL:      "https://a.com/no-params",
		MinZoom:  20,
		MaxZoom:  10,
		TileType: "invalid",
	}
	errs := parser.ValidateSource(invalid)
	if len(errs) == 0 {
		t.Fatal("expected validation errors")
	}
}

func createTestXML(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMapSourceHostsReturnsTheDistinctHosts(t *testing.T) {
	tests := []struct {
		name string
		src  MapSource
		want []string
	}{
		{"returns the host of a URL with no server parts", MapSource{URL: "https://example.com/{$z}/{$x}/{$y}.png"}, []string{"example.com"}},
		{"returns one host per server part", MapSource{URL: "https://{$serverpart}.tile.opentopomap.org/{$z}/{$x}/{$y}.png", ServerParts: []string{"a", "b"}}, []string{"a.tile.opentopomap.org", "b.tile.opentopomap.org"}},
		{"returns a shared host once", MapSource{URL: "https://example.com/{$serverpart}/{$z}/{$x}/{$y}.png", ServerParts: []string{"a", "b"}}, []string{"example.com"}},
		{"drops the port and lowers the letter case", MapSource{URL: "https://MT1.Google.com:8443/{$z}/{$x}/{$y}.png"}, []string{"mt1.google.com"}},
		{"returns no host for a URL with no host", MapSource{URL: "/{$z}/{$x}/{$y}.png"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.src.Hosts()
			if len(got) != len(tt.want) {
				t.Fatalf("Hosts() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("Hosts() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
