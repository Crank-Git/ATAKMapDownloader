package policy

import (
	"encoding/xml"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/snapshot"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
)

func TestLookupMatchesTheEmbeddedTable(t *testing.T) {
	tests := []struct {
		name    string
		host    string
		pattern string
		class   Class
		rps     float64
	}{
		{"matches a Google subdomain", "mt1.google.com", "*.google.com", Restricted, 1},
		{"matches a host in any letter case", "MT1.Google.COM", "*.google.com", Restricted, 1},
		{"matches a Bing subdomain at any depth", "ecn.t2.tiles.virtualearth.net", "*.virtualearth.net", Restricted, 1},
		{"matches an Esri subdomain", "server.arcgisonline.com", "*.arcgisonline.com", Restricted, 1},
		{"matches a host of the second pattern of an entry", "maps.googleapis.com", "*.google.com", Restricted, 1},
		{"matches the exact OSM tile host", "tile.openstreetmap.org", "tile.openstreetmap.org", Restricted, 1},
		{"matches an OSM tile subdomain", "a.tile.openstreetmap.org", "tile.openstreetmap.org", Restricted, 1},
		{"matches an OpenTopoMap subdomain", "a.tile.opentopomap.org", "*.opentopomap.org", Conditional, 2},
		{"matches a CyclOSM subdomain", "a.tile-cyclosm.openstreetmap.fr", "*.tile-cyclosm.openstreetmap.fr", Restricted, 1},
		{"matches a USGS subdomain", "basemap.nationalmap.gov", "*.nationalmap.gov", Open, 8},
		{"matches a USDA subdomain", "gis.apfo.usda.gov", "*.usda.gov", Open, 8},
		{"matches the OpenRailwayMap tile host", "tiles.openrailwaymap.org", "*.openrailwaymap.org", Restricted, 1},
		{"matches a BLM subdomain", "gis.blm.gov", "*.blm.gov", Open, 8},
		{"matches the BKG basemap.de host", "sgx.geodatenzentrum.de", "sgx.geodatenzentrum.de", Open, 8},
		{"matches the Ordnance Survey API host", "api.os.uk", "api.os.uk", Conditional, 2},
		{"matches the BC Government WMS host", "openmaps.gov.bc.ca", "openmaps.gov.bc.ca", Open, 8},
		{"matches an FAA chart host on the Esri entry", "tiles.arcgis.com", "*.arcgisonline.com", Restricted, 1},
		{"keeps a subdomain pattern off the bare domain", "google.com", "google.com", Conditional, 4},
		{"keeps a subdomain pattern off a longer domain", "evilgoogle.com", "evilgoogle.com", Conditional, 4},
		{"keeps an exact pattern off a subdomain of a subdomain", "x.openstreetmap.org", "x.openstreetmap.org", Conditional, 4},
		{"gives a host with no entry the default", "example.com", "example.com", Conditional, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lookup(tt.host)
			if got.ID() != tt.pattern || got.PolicyClass != tt.class || got.RequestsPerSecond != tt.rps {
				t.Errorf("Lookup(%q) = {%q %q %v}, want {%q %q %v}",
					tt.host, got.ID(), got.PolicyClass, got.RequestsPerSecond, tt.pattern, tt.class, tt.rps)
			}
		})
	}
}

func TestLookupReturnsTheFirstEntryThatMatches(t *testing.T) {
	entries := []Entry{
		{HostPatterns: []string{"a.example.com"}, PolicyClass: Open, RequestsPerSecond: 8},
		{HostPatterns: []string{"*.example.com"}, PolicyClass: Restricted, RequestsPerSecond: 1},
	}
	if got := lookup(entries, "a.example.com"); got.PolicyClass != Open {
		t.Errorf("lookup(a.example.com).PolicyClass = %q, want open", got.PolicyClass)
	}
	if got := lookup(entries, "b.example.com"); got.PolicyClass != Restricted {
		t.Errorf("lookup(b.example.com).PolicyClass = %q, want restricted", got.PolicyClass)
	}
}

func TestParseRejectsAnInvalidTable(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"rejects an unknown policy class", `[{"host_patterns":["*.a.com"],"policy_class":"free","requests_per_second":1}]`, `policy_class "free"`},
		{"rejects an empty policy class", `[{"host_patterns":["*.a.com"],"requests_per_second":1}]`, `policy_class ""`},
		{"rejects a rate of zero", `[{"host_patterns":["*.a.com"],"policy_class":"open","requests_per_second":0}]`, "requests_per_second"},
		{"rejects an entry with no host patterns", `[{"policy_class":"open","requests_per_second":1}]`, "host_patterns"},
		{"rejects an empty host pattern", `[{"host_patterns":["*.a.com",""],"policy_class":"open","requests_per_second":1}]`, "host_patterns"},
		{"rejects a body that is not JSON", `{`, "parse policy table"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse([]byte(tt.json))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("parse() error = %v, want an error that contains %q", err, tt.want)
			}
		})
	}
}

func TestEmbeddedTableHasEveryField(t *testing.T) {
	entries, err := parse(tableJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Reason == "" || e.Retrieved == "" {
			t.Errorf("entry %q has no reason or no retrieval date", e.ID())
		}
		if e.PolicyClass == Restricted && e.ReferenceURL == "" {
			t.Errorf("restricted entry %q has no reference_url", e.ID())
		}
	}
}

func TestUnverifiedLicenseSourcesAreConditionalAtTwo(t *testing.T) {
	files, err := fs.Glob(snapshot.FS(), "maps/base/ca-nrcan/*.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Error("found no ca-nrcan source files in the snapshot")
	}
	for _, file := range files {
		t.Run(path.Base(file), func(t *testing.T) {
			got := entryForFile(t, file)
			if got.PolicyClass != Conditional || got.RequestsPerSecond != 2 {
				t.Errorf("entry = {%q %q %v}, want conditional at 2", got.ID(), got.PolicyClass, got.RequestsPerSecond)
			}
		})
	}
}

// entryForFile returns the policy entry of a snapshot source file.
func entryForFile(t *testing.T, file string) Entry {
	t.Helper()
	src, err := xmlparser.ParseFS(snapshot.FS(), file)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return ForSource(src)
}

// unmatchedSnapshotHosts holds the snapshot hosts that may get the default entry, each with the reason.
// Keep it short: a host here gets 4 requests per second without a person having read its terms.
var unmatchedSnapshotHosts = map[string]string{}

func TestEverySnapshotHostMatchesATableEntry(t *testing.T) {
	var files []string
	err := fs.WalkDir(snapshot.FS(), "maps", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && path.Ext(name) == ".xml" {
			files = append(files, name)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("found no source files in the snapshot")
	}
	for _, file := range files {
		for _, host := range snapshotFileHosts(t, file) {
			if _, ok := unmatchedSnapshotHosts[host]; ok {
				continue
			}
			// lookup with no entries returns the default entry, so an equal reason means no entry matched.
			if got := Lookup(host); got.Reason == lookup(nil, host).Reason {
				t.Errorf("%s: host %q matches no policy entry", file, host)
			}
		}
	}
}

// snapshotFileHosts returns the hosts of every <url> element in a snapshot source file.
// It reads the XML directly, because the parser skips multi-layer files.
func snapshotFileHosts(t *testing.T, file string) []string {
	t.Helper()
	data, err := fs.ReadFile(snapshot.FS(), file)
	if err != nil {
		t.Fatal(err)
	}
	// A multi-layer file holds its <url> and <serverParts> elements in each layer.
	type layer struct {
		URL         string `xml:"url"`
		ServerParts string `xml:"serverParts"`
	}
	var raw struct {
		layer
		Layers []layer `xml:"layers>customMapSource"`
	}
	if err := xml.Unmarshal(data, &raw); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var urls, parts []string
	for _, l := range append(raw.Layers, raw.layer) {
		if u := strings.TrimSpace(l.URL); u != "" {
			urls = append(urls, u)
		}
		parts = append(parts, strings.Fields(l.ServerParts)...)
	}
	if len(urls) == 0 {
		t.Errorf("%s has no <url> element", file)
	}
	if len(parts) == 0 {
		parts = []string{""}
	}
	var hosts []string
	for _, raw := range urls {
		for _, part := range parts {
			u, err := url.Parse(strings.ReplaceAll(raw, "{$serverpart}", part))
			if err != nil || u.Hostname() == "" {
				t.Errorf("%s: cannot read the host of %q", file, raw)
				continue
			}
			hosts = append(hosts, strings.ToLower(u.Hostname()))
		}
	}
	return hosts
}

func TestForSourceReturnsTheStrictestEntry(t *testing.T) {
	tests := []struct {
		name    string
		src     xmlparser.MapSource
		pattern string
		class   Class
	}{
		{
			name:    "chooses restricted over open",
			src:     xmlparser.MapSource{URL: "https://{$serverpart}/{$z}/{$x}/{$y}.png", ServerParts: []string{"basemap.nationalmap.gov", "mt1.google.com"}},
			pattern: "*.google.com",
			class:   Restricted,
		},
		{
			name:    "chooses the lower rate in one class",
			src:     xmlparser.MapSource{URL: "https://{$serverpart}/{$z}/{$x}/{$y}.png", ServerParts: []string{"example.com", "a.tile.opentopomap.org"}},
			pattern: "*.opentopomap.org",
			class:   Conditional,
		},
		{
			name:    "returns the default for a host with no entry",
			src:     xmlparser.MapSource{URL: "https://example.com/{$z}/{$x}/{$y}.png"},
			pattern: "example.com",
			class:   Conditional,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ForSource(&tt.src)
			if got.ID() != tt.pattern || got.PolicyClass != tt.class {
				t.Errorf("ForSource() = {%q %q}, want {%q %q}", got.ID(), got.PolicyClass, tt.pattern, tt.class)
			}
		})
	}
}
