package xmlparser

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
)

// snapshotDir is the embedded snapshot of internal/snapshot, read from the disk so that this package needs no import of it.
var snapshotDir = os.DirFS("../snapshot")

func TestParseFSLoadsTheWMSSourceFilesOfTheSnapshot(t *testing.T) {
	tests := []struct {
		file    string
		version string
		layers  string
		styles  string
		crs     string
		params  string
	}{
		{"maps/overlays/grg_fema_flood.xml", "1.3.0", "12", "", "EPSG:4326", "&TRANSPARENT=TRUE"},
		{"maps/overlays/grg_ca_bc_wildfire.xml", "1.3.0", "pub:WHSE_LAND_AND_NATURAL_RESOURCE.PROT_CURRENT_FIRE_POLYS_SP", "1751_1752", "EPSG:3857", "&transparent=true"},
		{"maps/base/ca-nrcan/ca_base_map.xml", "1.1.1", "CBMT", "", "EPSG:3857", ""},
		{"maps/base/ca-nrcan/ca_toporama.xml", "1.1.1", "WMS-Toporama", "", "EPSG:3857", ""},
		{"maps/base/naip/naip_imagery.xml", "1.3.0", "USGSNAIPImagery", "", "EPSG:3857", ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			s, err := ParseFS(snapshotDir, tt.file)
			if err != nil {
				t.Fatalf("ParseFS(%s) returned %v", tt.file, err)
			}
			if s.Kind != KindWMS || s.WMSVersion != tt.version || s.WMSLayers != tt.layers ||
				s.WMSStyles != tt.styles || s.WMSCRS != tt.crs || s.WMSParams != tt.params {
				t.Errorf("ParseFS(%s) = {Kind %q, version %q, layers %q, styles %q, CRS %q, params %q}, want {%q, %q, %q, %q, %q, %q}",
					tt.file, s.Kind, s.WMSVersion, s.WMSLayers, s.WMSStyles, s.WMSCRS, s.WMSParams,
					KindWMS, tt.version, tt.layers, tt.styles, tt.crs, tt.params)
			}
			if errs := New("").ValidateSource(s); len(errs) != 0 {
				t.Errorf("ValidateSource: %v", errs)
			}
		})
	}
}

func TestParseAppliesTheWMSDefaultsOfTheXMLReference(t *testing.T) {
	s, err := parse([]byte(`<customWmsMapSource><name>W</name><url>https://wms.test/wms?</url><layers>a</layers>
<additionalparameters>&amp;x=1</additionalparameters></customWmsMapSource>`), OriginUser)
	if err != nil {
		t.Fatal(err)
	}
	// The ATAK-Maps XML reference v2.3.1 gives version 1.1.1 and CRS EPSG:4326 as the defaults.
	if s.WMSVersion != "1.1.1" || s.WMSCRS != "EPSG:4326" || s.WMSParams != "&x=1" {
		t.Errorf("parse = {version %q, CRS %q, params %q}, want {1.1.1, EPSG:4326, &x=1}", s.WMSVersion, s.WMSCRS, s.WMSParams)
	}
}

func TestParseSkipsAWMSSourceFileWithNoLayers(t *testing.T) {
	_, err := parse([]byte(`<customWmsMapSource><name>W</name><url>https://wms.test/wms?</url></customWmsMapSource>`), OriginUser)
	var se *SkipError
	if !errors.As(err, &se) || se.Reason != SkipParseError || !strings.Contains(se.Detail, "<layers>") {
		t.Errorf("parse = %v, want a parse_error that names <layers>", err)
	}
}

// wmsSource returns a WMS map source with the base URL base and the version version.
func wmsSource(base, version string) *MapSource {
	return &MapSource{Kind: KindWMS, URL: base, TileType: "png", WMSVersion: version, WMSLayers: "a,b", WMSCRS: "EPSG:4326"}
}

func TestFormatURLBuildsAWMSGetMapRequest(t *testing.T) {
	// half is the distance from the origin of EPSG:3857 to the edge of the world.
	const half = "20037508.342789244"
	tests := []struct {
		name   string
		source *MapSource
		x, y   int
		z      int
		want   map[string]string
	}{
		{
			"sends CRS for version 1.3.0", wmsSource("https://wms.test/wms?", "1.3.0"), 1, 0, 1,
			map[string]string{
				"SERVICE": "WMS", "REQUEST": "GetMap", "VERSION": "1.3.0", "LAYERS": "a,b", "STYLES": "",
				"CRS": "EPSG:3857", "BBOX": "0,0," + half + "," + half, "WIDTH": "256", "HEIGHT": "256", "FORMAT": "image/png",
			},
		},
		{
			"sends SRS for version 1.1.1", wmsSource("https://wms.test/wms", "1.1.1"), 0, 0, 0,
			map[string]string{"SRS": "EPSG:3857", "BBOX": "-" + half + ",-" + half + "," + half + "," + half},
		},
		{
			"sends image/jpeg for a JPG tile type", &MapSource{Kind: KindWMS, URL: "https://wms.test/wms?", TileType: "JPG", WMSVersion: "1.3.0", WMSLayers: "a"}, 0, 0, 0,
			map[string]string{"FORMAT": "image/jpeg"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := tt.source.FormatURL(tt.x, tt.y, tt.z, "")
			u, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("FormatURL = %q, which does not parse: %v", raw, err)
			}
			q := u.Query()
			for k, v := range tt.want {
				if got, ok := q[k]; !ok || len(got) != 1 || got[0] != v {
					t.Errorf("FormatURL = %q: %s = %v, want [%s]", raw, k, got, v)
				}
			}
			if _, ok := q["CRS"]; ok && tt.source.WMSVersion == "1.1.1" {
				t.Errorf("FormatURL = %q sends CRS for version 1.1.1", raw)
			}
		})
	}
}

func TestFormatURLKeepsTheWMSParametersOfTheSourceFile(t *testing.T) {
	s := wmsSource("https://wms.test/ows?SERVICE=WMS&", "1.3.0")
	s.WMSParams = "&STYLES=x&TRANSPARENT=TRUE"
	raw := s.FormatURL(0, 0, 0, "")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	// A second STYLES makes some servers return an XML exception, so the source file value replaces the default.
	for k, want := range map[string]string{"SERVICE": "WMS", "STYLES": "x", "TRANSPARENT": "TRUE"} {
		if got := q[k]; len(got) != 1 || got[0] != want {
			t.Errorf("FormatURL = %q: %s = %v, want [%s]", raw, k, got, want)
		}
	}
}

func TestCapabilitiesURLAsksForTheWMSServiceMetadata(t *testing.T) {
	raw := wmsSource("https://wms.test/wms?map=x", "1.3.0").CapabilitiesURL()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{"map": "x", "SERVICE": "WMS", "REQUEST": "GetCapabilities", "VERSION": "1.3.0"} {
		if got := q[k]; len(got) != 1 || got[0] != want {
			t.Errorf("CapabilitiesURL = %q: %s = %v, want [%s]", raw, k, got, want)
		}
	}
}

func TestHostsReturnsTheHostOfAWMSBaseURL(t *testing.T) {
	s, err := ParseFS(snapshotDir, "maps/overlays/grg_fema_flood.xml")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Hosts(); len(got) != 1 || got[0] != "hazards.fema.gov" {
		t.Errorf("Hosts() = %v, want [hazards.fema.gov]", got)
	}
}
