package policy

import (
	"encoding/xml"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		{"matches the exact OSM tile host", "tile.openstreetmap.org", "tile.openstreetmap.org", Restricted, 1},
		{"matches an OSM tile subdomain", "a.tile.openstreetmap.org", "*.tile.openstreetmap.org", Restricted, 1},
		{"matches an OpenTopoMap subdomain", "a.tile.opentopomap.org", "*.opentopomap.org", Conditional, 2},
		{"matches a CyclOSM subdomain", "a.tile-cyclosm.openstreetmap.fr", "*.tile-cyclosm.openstreetmap.fr", Conditional, 2},
		{"matches a USGS subdomain", "basemap.nationalmap.gov", "*.nationalmap.gov", Open, 8},
		{"matches a USDA subdomain", "gis.apfo.usda.gov", "*.usda.gov", Open, 8},
		{"keeps a subdomain pattern off the bare domain", "google.com", "google.com", Conditional, 4},
		{"keeps a subdomain pattern off a longer domain", "evilgoogle.com", "evilgoogle.com", Conditional, 4},
		{"keeps an exact pattern off a subdomain of a subdomain", "x.openstreetmap.org", "x.openstreetmap.org", Conditional, 4},
		{"gives a host with no entry the default", "example.com", "example.com", Conditional, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Lookup(tt.host)
			if got.HostPattern != tt.pattern || got.PolicyClass != tt.class || got.RequestsPerSecond != tt.rps {
				t.Errorf("Lookup(%q) = {%q %q %v}, want {%q %q %v}",
					tt.host, got.HostPattern, got.PolicyClass, got.RequestsPerSecond, tt.pattern, tt.class, tt.rps)
			}
		})
	}
}

func TestLookupReturnsTheFirstEntryThatMatches(t *testing.T) {
	entries := []Entry{
		{HostPattern: "a.example.com", PolicyClass: Open, RequestsPerSecond: 8},
		{HostPattern: "*.example.com", PolicyClass: Restricted, RequestsPerSecond: 1},
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
		{"rejects an unknown policy class", `[{"host_pattern":"*.a.com","policy_class":"free","requests_per_second":1}]`, `policy_class "free"`},
		{"rejects an empty policy class", `[{"host_pattern":"*.a.com","requests_per_second":1}]`, `policy_class ""`},
		{"rejects a rate of zero", `[{"host_pattern":"*.a.com","policy_class":"open","requests_per_second":0}]`, "requests_per_second"},
		{"rejects an empty host pattern", `[{"policy_class":"open","requests_per_second":1}]`, "host_pattern"},
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
			t.Errorf("entry %q has no reason or no retrieval date", e.HostPattern)
		}
		if e.PolicyClass == Restricted && e.ReferenceURL == "" {
			t.Errorf("restricted entry %q has no reference_url", e.HostPattern)
		}
	}
}

func TestUnverifiedLicenseSourcesAreConditionalAtTwo(t *testing.T) {
	const dir = "../../mapsources"
	parser := xmlparser.New(dir)
	for _, provider := range []string{"NationalLandSurveyOfFinland", "NaturalResourcesCanada"} {
		files, err := filepath.Glob(filepath.Join(dir, provider, "*.xml"))
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Errorf("found no source files in %s", provider)
		}
		for _, file := range files {
			t.Run(filepath.Base(file), func(t *testing.T) {
				got := entryForFile(t, parser, file)
				if got.PolicyClass != Conditional || got.RequestsPerSecond != 2 {
					t.Errorf("entry = {%q %q %v}, want conditional at 2", got.HostPattern, got.PolicyClass, got.RequestsPerSecond)
				}
			})
		}
	}
}

// entryForFile returns the policy entry of a source file.
// The parser skips customWmsMapSource files, so for those it looks up the host of the <url> element.
func entryForFile(t *testing.T, parser *xmlparser.XMLParser, file string) Entry {
	t.Helper()
	if src, err := parser.ParseFile(file); err == nil && src != nil {
		return ForSource(src)
	}
	data, err := os.ReadFile(file) //nolint:gosec // The test reads the bundled source files.
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		URL string `xml:"url"`
	}
	if err := xml.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(strings.TrimSpace(raw.URL))
	if err != nil {
		t.Fatal(err)
	}
	return Lookup(u.Hostname())
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
			if got.HostPattern != tt.pattern || got.PolicyClass != tt.class {
				t.Errorf("ForSource() = {%q %q}, want {%q %q}", got.HostPattern, got.PolicyClass, tt.pattern, tt.class)
			}
		})
	}
}
