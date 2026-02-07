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

func TestMapSource_GetServerPart(t *testing.T) {
	source := &MapSource{
		ServerParts: []string{"a", "b", "c"},
	}
	if source.GetServerPart(0) != "a" {
		t.Error("expected a")
	}
	if source.GetServerPart(1) != "b" {
		t.Error("expected b")
	}
	if source.GetServerPart(3) != "a" {
		t.Error("expected wrap to a")
	}
	noParts := &MapSource{ServerParts: nil}
	if noParts.GetServerPart(0) != "" {
		t.Error("expected empty for no server parts")
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
	sources := parser.DiscoverSources(false)
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
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
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
