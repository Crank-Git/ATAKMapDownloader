package snapshot

import (
	"encoding/xml"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// v01Names are the map source names of the v0.1 mapsources/ folder, from the 33 files at origin/develop.
var v01Names = []string{
	"Bing - Hybrid",
	"Bing - Maps",
	"Canada - Toporama",
	"Canada Base Map – Transportation",
	"CycleOSM - OSM Cycle",
	"Esri - Clarity",
	"Esri - Nat Geo World",
	"Esri - USA Topo Maps",
	"Esri - World Topo",
	"FEMA NFHL - Flood Hazard Zones (WMS)",
	"GRG - Google Road Only Overlay",
	"GRG - Google Terrain Shading Overlay",
	"GRG - USDA Fstopo Overlay",
	"GRG - WaymarkedTrails Cycle Routes Overlay",
	"Google - Hybrid",
	"Google - Roadmap Alt",
	"Google - Roadmap No Poi",
	"Google - Roadmap Standard",
	"Google - Satellite Only",
	"Google - Terrain",
	"MTBMap.cz - MTB Map Europe",
	"Michelin - OSM Michelin",
	"NAIP – USDA CONUS Prime",
	"NAIP – USGS National Map",
	"National Land Survey of Finland - MML",
	"OpenSeaMap – Base Chart",
	"OpenSeaMap – Seamarks",
	"OpenTopo - Opentopomap",
	"PL Ortofoto Std (WMTS EPSG3857)",
	"USGS - Usgsbasemap",
	"USGS - Usgsimageryonly",
	"USGS - Usgsimagerytopo",
	"USGS - Usgsshadedrelief",
}

// snapshotNames returns the name of every source file in the snapshot, whatever its root element.
func snapshotNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	err := fs.WalkDir(FS(), "maps", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".xml") {
			return err
		}
		data, err := fs.ReadFile(FS(), path)
		if err != nil {
			return err
		}
		var file struct {
			Name string `xml:"name"`
		}
		if err := xml.Unmarshal(data, &file); err != nil {
			return err
		}
		names[file.Name] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestAliasReturnsTheATAKMapsV2Name(t *testing.T) {
	tests := []struct {
		old  string
		want string
	}{
		{"Google - Satellite Only", "Google Satellite"},
		{"Google - Roadmap Standard", "Google Road"},
		{"Bing - Maps", "Bing Road"},
		{"GRG - Google Terrain Shading Overlay", "Google Relief"},
		{"OpenSeaMap – Seamarks", "+ OpenSeaMap"},
		{"USGS - Usgsbasemap", "USGS Topo"},
		{"Google Satellite", ""},
		{"No Such Source", ""},
	}
	for _, tt := range tests {
		t.Run(tt.old, func(t *testing.T) {
			got, err := Alias(tt.old)
			if err != nil || got != tt.want {
				t.Errorf("Alias(%q) = %q, %v, want %q, nil", tt.old, got, err, tt.want)
			}
		})
	}
}

func TestAliasOfARemovedSourceNamesTheUpstreamVersion(t *testing.T) {
	tests := []struct {
		old  string
		want string
	}{
		{"Michelin - OSM Michelin", "map source removed upstream in ATAK-Maps v1.10.1"},
		{"GRG - USDA Fstopo Overlay", "map source removed upstream in ATAK-Maps v1.7.0"},
		{"NAIP – USGS National Map", "map source removed upstream in ATAK-Maps v1.7.0"},
		{"National Land Survey of Finland - MML", "map source removed upstream in ATAK-Maps v1.7.0"},
	}
	for _, tt := range tests {
		t.Run(tt.old, func(t *testing.T) {
			got, err := Alias(tt.old)
			var removed *RemovedError
			if got != "" || !errors.As(err, &removed) || err.Error() != tt.want {
				t.Errorf("Alias(%q) = %q, %v, want \"\" and a *RemovedError %q", tt.old, got, err, tt.want)
			}
		})
	}
}

func TestEveryAliasTargetExistsInTheSnapshot(t *testing.T) {
	names := snapshotNames(t)
	for old, target := range aliases().Aliases {
		if !names[target] {
			t.Errorf("alias %q -> %q: the snapshot has no source file with that name", old, target)
		}
	}
}

// FR-source-sync-17 keeps the aliases for at least one minor release after v0.5.0.
// Thus this test fails when a v0.1 name leaves the table before v0.6.0 ships.
func TestTheAliasTableCoversEveryV01NameUntilV060(t *testing.T) {
	table := aliases()
	if n := len(table.Aliases) + len(table.Removed); n != len(v01Names) {
		t.Errorf("the table has %d entries, want %d, one for each v0.1 name", n, len(v01Names))
	}
	for _, name := range v01Names {
		got, err := Alias(name)
		var removed *RemovedError
		if got == "" && !errors.As(err, &removed) {
			t.Errorf("Alias(%q) = %q, %v, want a v2 name or a *RemovedError", name, got, err)
		}
	}
}
