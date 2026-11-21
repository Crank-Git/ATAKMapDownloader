package download

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

// metadataBounds are bounds with a center of -121.5, 45.25.
var metadataBounds = tilecalc.GeographicBounds{MinLat: 45, MaxLat: 45.5, MinLon: -122, MaxLon: -121}

func TestOutputMetadataWritesEveryMBTilesKey(t *testing.T) {
	source := &xmlparser.MapSource{Name: "Test XYZ", TileType: "png", Origin: xmlparser.OriginSnapshot}
	meta := outputMetadata(source, &metadataBounds, []int{8, 9, 10})

	want := map[string]string{
		"name":          "Test XYZ - 45.0000,-122.0000 to 45.5000,-121.0000",
		"format":        "png",
		"bounds":        "-122.0000,45.0000,-121.0000,45.5000",
		"center":        "-121.5000,45.2500,8",
		"minzoom":       "8",
		"maxzoom":       "10",
		"type":          "baselayer",
		"version":       "1.0",
		"description":   "Downloaded from Test XYZ",
		"attribution":   "Test XYZ via ATAK-Maps",
		"atakdl_source": "Test XYZ",
	}
	if len(meta) != len(want) {
		t.Errorf("outputMetadata has %d keys, want %d: %v", len(meta), len(want), meta)
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("metadata %s = %q, want %q", k, meta[k], v)
		}
	}
}

func TestOutputMetadataFormatIsTheLowerCaseTileType(t *testing.T) {
	tests := []struct {
		tileType string
		want     string
	}{
		{"png", "png"},
		{"PNG", "png"},
		{"jpg", "jpg"},
		{"jpeg", "jpg"},
		{"JPEG", "jpg"},
		{"webp", "webp"},
	}
	for _, tt := range tests {
		t.Run(tt.tileType, func(t *testing.T) {
			source := &xmlparser.MapSource{Name: "Test", TileType: tt.tileType, Origin: xmlparser.OriginUser}
			if got := outputMetadata(source, &metadataBounds, []int{10})["format"]; got != tt.want {
				t.Errorf("format = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOutputMetadataTypeAndAttributionFollowTheMapSource(t *testing.T) {
	tests := []struct {
		name            string
		source          xmlparser.MapSource
		wantType        string
		wantAttribution string
	}{
		{
			"gives a snapshot overlay the type overlay",
			xmlparser.MapSource{Name: "GRG - FEMA Flood", Origin: xmlparser.OriginSnapshot, Overlay: true, File: "maps/overlays/grg_fema_flood.xml"},
			"overlay", "GRG - FEMA Flood via ATAK-Maps",
		},
		{
			"gives a snapshot base map source the type baselayer",
			xmlparser.MapSource{Name: "OSM", Origin: xmlparser.OriginSnapshot, File: "maps/base/osm/osm.xml"},
			"baselayer", "OSM via ATAK-Maps",
		},
		{
			"gives a user map source the type baselayer and its file name as attribution",
			xmlparser.MapSource{Name: "Mine", Origin: xmlparser.OriginUser, File: filepath.Join("mapsources", "mine.xml")},
			"baselayer", "mine.xml",
		},
		{
			"escapes the HTML characters of a snapshot map source name",
			xmlparser.MapSource{Name: `Roads & <Rails> "Test"`, Origin: xmlparser.OriginSnapshot, File: "maps/base/roads.xml"},
			"baselayer", "Roads &amp; &lt;Rails&gt; &#34;Test&#34; via ATAK-Maps",
		},
		{
			"escapes the HTML characters of a user file name",
			xmlparser.MapSource{Name: "Mine", Origin: xmlparser.OriginUser, File: filepath.Join("mapsources", "a&b<c>.xml")},
			"baselayer", "a&amp;b&lt;c&gt;.xml",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.source.TileType = "png"
			meta := outputMetadata(&tt.source, &metadataBounds, []int{10})
			if meta["type"] != tt.wantType || meta["attribution"] != tt.wantAttribution {
				t.Errorf("type, attribution = %q, %q; want %q, %q", meta["type"], meta["attribution"], tt.wantType, tt.wantAttribution)
			}
		})
	}
}

func TestOutputMetadataKeepsTheUserSourceDirectoryOutOfTheAttribution(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	file := filepath.Join(home, "atak", "mapsources", "atak-maps", "maps", "overlays", "roads.xml")
	source := &xmlparser.MapSource{Name: "Roads", TileType: "png", Origin: xmlparser.OriginUser, File: file}

	got := outputMetadata(source, &metadataBounds, []int{10})["attribution"]
	if got != "roads.xml" {
		t.Errorf("attribution = %q, want the bare file name %q", got, "roads.xml")
	}
	if filepath.IsAbs(got) || strings.Contains(got, home) || strings.ContainsRune(got, filepath.Separator) {
		t.Errorf("attribution = %q holds a path, and the output file can go to other devices", got)
	}
}
