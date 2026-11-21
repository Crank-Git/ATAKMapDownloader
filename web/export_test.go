package web

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// exportScenario is the input of testdata/export_panel.js.
type exportScenario struct {
	Area []float64 `json:"area,omitempty"`
	// Zoom is the zoom range of the area. It is [1, 15] when it is empty.
	Zoom    []int            `json:"zoom,omitempty"`
	Sources []map[string]any `json:"sources,omitempty"`
	Files   []map[string]any `json:"files,omitempty"`
	// Set holds [id, value] pairs in the order that the user changes them. A bool value sets checked.
	Set   [][]any `json:"set,omitempty"`
	Start bool    `json:"start,omitempty"`
}

// exportPanel is the state of the export panel that export_panel.js prints.
type exportPanel struct {
	Preview                string         `json:"preview"`
	Message                string         `json:"message"`
	StartDisabled          bool           `json:"startDisabled"`
	Format                 string         `json:"format"`
	DisabledFormats        []string       `json:"disabledFormats"`
	FormatLock             string         `json:"formatLock"`
	PackageOptionsDisabled bool           `json:"packageOptionsDisabled"`
	Request                map[string]any `json:"request"`
	FileRows               []string       `json:"fileRows"`
	BaseMaps               []string       `json:"baseMaps"`
}

// runExportPanel runs app.js in Node with a fake DOM from index.html, applies s, and returns the export panel.
func runExportPanel(t *testing.T, s exportScenario) exportPanel {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so app.js cannot run")
	}
	in, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	// #nosec G204 -- the arguments are test fixture paths and a JSON scenario of the test.
	cmd := exec.CommandContext(t.Context(), node, filepath.Join("testdata", "export_panel.js"),
		"index.html", filepath.Join("static", "js", "app.js"), string(in))
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node export_panel.js: %v\n%s", err, stderr.String())
	}
	var got exportPanel
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	return got
}

// campArea is [west, south, east, north].
var campArea = []float64{-122, 45, -121.5, 45.5}

var campSources = []map[string]any{{"name": "Camp Source", "policy_class": "open", "min_zoom": 0, "max_zoom": 18}}

func TestExportPreviewFollowsTheOutputFormatAndThePackageName(t *testing.T) {
	named := []any{"customName", "Camp"}
	pkg := []any{"dataPackage", true}
	tests := []struct {
		name string
		set  [][]any
		zoom []int
		want string
	}{
		{"shows the MBTiles extension by default", [][]any{named}, nil, "Camp_z1-15.mbtiles"},
		{"shows the OSMDroid extension", [][]any{named, {"format-osmdroid", true}}, nil, "Camp_z1-15.sqlite"},
		{"shows the GeoPackage extension", [][]any{named, {"format-gpkg", true}}, nil, "Camp_z1-15.gpkg"},
		{"names the map source and the area with no custom name", [][]any{{"mapSourceSelect", "Camp Source"}},
			nil, "Camp_Source_45.0000_-122.0000_to_45.5000_-121.5000_z1-15.mbtiles"},
		{"follows the zoom range of the area", [][]any{named}, []int{1, 12}, "Camp_z1-12.mbtiles"},
		{"shows the zip of the package name", [][]any{named, pkg, {"packageName", "Mt Hood SAR"}}, nil, "Mt_Hood_SAR.zip"},
		{"follows a package name change", [][]any{named, pkg, {"packageName", "Mt Hood SAR"}, {"packageName", "Base Camp"}}, nil, "Base_Camp.zip"},
		{"keeps the zip name for every output format", [][]any{named, pkg, {"packageName", "Mt Hood SAR"}, {"format-gpkg", true}}, nil, "Mt_Hood_SAR.zip"},
		{"goes back to the output file without the data package", [][]any{named, pkg, {"packageName", "Mt Hood SAR"}, {"dataPackage", false}}, nil, "Camp_z1-15.mbtiles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runExportPanel(t, exportScenario{Area: campArea, Zoom: tt.zoom, Sources: campSources, Set: tt.set})
			if got.Preview != tt.want {
				t.Errorf("preview = %q, want %q", got.Preview, tt.want)
			}
		})
	}
}

func TestExportPanelStartsWithMBTilesAndNoDataPackage(t *testing.T) {
	got := runExportPanel(t, exportScenario{})
	if got.Format != "mbtiles" || !got.PackageOptionsDisabled || got.Message != "" || len(got.DisabledFormats) != 0 {
		t.Errorf("default panel = %+v, want MBTiles, disabled package options and no message", got)
	}
}

func TestExportPanelDisablesStartForAnInvalidPackageName(t *testing.T) {
	tests := []struct {
		name        string
		packageName string
		wantMessage string
	}{
		{"accepts a valid name", "Mt Hood SAR", ""},
		{"rejects an empty name", "", "Enter a package name"},
		{"rejects a name of spaces only", "   ", "Enter a package name"},
		{"rejects a name with a slash", "a/b", "Use 1 to 64 letters, digits, spaces, _ or -"},
		{"rejects a name of 65 characters", strings.Repeat("a", 65), "Use 1 to 64 letters, digits, spaces, _ or -"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runExportPanel(t, exportScenario{Area: campArea, Sources: campSources, Set: [][]any{
				{"mapSourceSelect", "Camp Source"}, {"dataPackage", true}, {"packageName", tt.packageName},
			}})
			if got.Message != tt.wantMessage {
				t.Errorf("message = %q, want %q", got.Message, tt.wantMessage)
			}
			if got.StartDisabled != (tt.wantMessage != "") {
				t.Errorf("Start disabled = %v, want %v", got.StartDisabled, tt.wantMessage != "")
			}
			if got.PackageOptionsDisabled {
				t.Error("the package options stay disabled with the data package checked")
			}
		})
	}
}

func TestExportPanelLocksTheOutputFormatOfAResumedJob(t *testing.T) {
	existing := []map[string]any{
		{"name": "Camp_z1-15.gpkg", "size": 4096, "modified": "2026-11-21T10:00:00Z", "format": "png", "output_format": "gpkg",
			"source": "Camp Source", "min_zoom": 1, "max_zoom": 15, "bounds": nil, "in_progress": false},
	}
	tests := []struct {
		name         string
		set          [][]any
		wantFormat   string
		wantDisabled []string
	}{
		{"locks a resumed job to the format of the existing file", [][]any{{"customName", "Camp"}, {"ifExists", "resume"}},
			"gpkg", []string{"mbtiles", "osmdroid"}},
		{"keeps the format free when no file has the name", [][]any{{"customName", "Other"}, {"ifExists", "resume"}},
			"mbtiles", []string{}},
		{"keeps the format free for an overwrite", [][]any{{"customName", "Camp"}, {"ifExists", "overwrite"}},
			"mbtiles", []string{}},
		{"frees the format when the user leaves resume", [][]any{{"customName", "Camp"}, {"ifExists", "resume"}, {"ifExists", "fail"}},
			"gpkg", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runExportPanel(t, exportScenario{Area: campArea, Sources: campSources, Files: existing, Set: tt.set})
			if got.Format != tt.wantFormat || !slices.Equal(got.DisabledFormats, tt.wantDisabled) {
				t.Errorf("format = %q, disabled %q; want %q, disabled %q", got.Format, got.DisabledFormats, tt.wantFormat, tt.wantDisabled)
			}
			if locked := len(tt.wantDisabled) > 0; locked != strings.Contains(got.FormatLock, "Camp_z1-15.gpkg") {
				t.Errorf("format lock message = %q, want it shown %v", got.FormatLock, locked)
			}
		})
	}
}

func TestStartSendsTheExportSettings(t *testing.T) {
	got := runExportPanel(t, exportScenario{Area: campArea, Sources: campSources, Start: true, Set: [][]any{
		{"mapSourceSelect", "Camp Source"}, {"format-gpkg", true}, {"dataPackage", true},
		{"includeSource", true}, {"packageName", " Mt Hood SAR "},
	}})
	if got.Request == nil {
		t.Fatal("Start sent no POST /api/jobs")
	}
	for field, want := range map[string]any{
		"output_format": "gpkg", "data_package": true, "include_source": true, "package_name": "Mt Hood SAR",
	} {
		if got.Request[field] != want {
			t.Errorf("request %s = %v, want %v", field, got.Request[field], want)
		}
	}
}

func TestStartSendsNoJobForAnEmptyPackageName(t *testing.T) {
	got := runExportPanel(t, exportScenario{Area: campArea, Sources: campSources, Start: true, Set: [][]any{
		{"mapSourceSelect", "Camp Source"}, {"dataPackage", true},
	}})
	if got.Request != nil {
		t.Errorf("Start sent %v with an empty package name", got.Request)
	}
}

func TestOutputFileListNamesTheFormatAndOmitsTheDataPackageFromTheBaseMaps(t *testing.T) {
	got := runExportPanel(t, exportScenario{Files: []map[string]any{
		{"name": "Mt_Hood_SAR.zip", "size": 8192, "modified": "2026-11-21T11:00:00Z", "format": "data_package", "output_format": "",
			"source": "", "min_zoom": 0, "max_zoom": 0, "bounds": nil, "in_progress": false},
		{"name": "Camp_z1-15.gpkg", "size": 4096, "modified": "2026-11-21T10:00:00Z", "format": "png", "output_format": "gpkg",
			"source": "Camp Source", "min_zoom": 1, "max_zoom": 15, "bounds": nil, "in_progress": false},
	}})
	if len(got.FileRows) != 2 {
		t.Fatalf("the output file list has %d rows, want 2", len(got.FileRows))
	}
	if row := got.FileRows[0]; !strings.Contains(row, "Data package") || strings.Contains(row, "Zoom") {
		t.Errorf("data package row = %q, want the format Data package and no zoom range", row)
	}
	if row := got.FileRows[1]; !strings.Contains(row, "GeoPackage") || !strings.Contains(row, "Zoom 1-15") {
		t.Errorf("GeoPackage row = %q, want the format GeoPackage and the zoom range", row)
	}
	// A data package has no tile route, so the base map switcher omits it (feature 08, edge cases).
	if !slices.Equal(got.BaseMaps, []string{"Camp_z1-15.gpkg"}) {
		t.Errorf("base maps = %q, want only Camp_z1-15.gpkg", got.BaseMaps)
	}
}

func TestIndexHoldsTheExportPanel(t *testing.T) {
	index := string(IndexHTML())
	for _, want := range []string{
		`id="format-mbtiles" value="mbtiles" checked`,
		`id="format-osmdroid" value="osmdroid"`,
		`id="format-gpkg" value="gpkg"`,
		`<fieldset id="outputFormat"`,
		`<legend>Output format</legend>`,
		`id="dataPackage"`,
		`<fieldset id="packageOptions"`,
		`id="includeSource"`,
		`id="packageName"`,
		`id="exportPreview"`,
		`<code>atak/imagery/</code>`,
		"Import Manager",
	} {
		if !strings.Contains(index, want) {
			t.Errorf("index.html does not contain %q", want)
		}
	}
	// The message of an invalid package name must reach a screen reader as it appears.
	if !regexp.MustCompile(`<p id="packageNameMessage"[^>]*\baria-live="polite"`).MatchString(index) {
		t.Error("the package name message is not a live region")
	}
	app := appJS(t)
	for _, id := range []string{"outputFormat", "dataPackage", "includeSource", "packageName"} {
		if !regexp.MustCompile(`JOB_SETTINGS = \[[^\]]*'` + id + `'`).MatchString(app) {
			t.Errorf("JOB_SETTINGS does not name the export setting %q, so a running job does not lock it", id)
		}
	}
}
