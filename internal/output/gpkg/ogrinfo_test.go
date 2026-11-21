package gpkg_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/policy"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/paulmach/orb"
)

// requireGDAL is the environment variable that turns a missing GDAL into a test failure. CI sets it to 1.
const requireGDAL = "ATAKDL_REQUIRE_GDAL"

// gdalTool returns the path of the GDAL command name.
// Without the command, the test fails when requireGDAL is 1, and it skips otherwise.
func gdalTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err == nil {
		t.Logf("GDAL check runs with %s", path)
		return path
	}
	if os.Getenv(requireGDAL) == "1" {
		t.Fatalf("%s=1 and %s is not on PATH: install GDAL (apt-get install gdal-bin, brew install gdal): %v", requireGDAL, name, err)
	}
	t.Skipf("GDAL check skipped: %s is not on PATH. Install GDAL and set %s=1 to require the check.", name, requireGDAL)
	return ""
}

// run returns the combined output of the command name with args, and fails the test when the command fails.
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), name, args...).CombinedOutput() //nolint:gosec // The test names the GDAL command and its arguments.
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestGDALListsAGeoPackageJobAsARasterGeoPackageInEPSG3857(t *testing.T) {
	ogrinfo := gdalTool(t, "ogrinfo")
	gdalinfo := gdalTool(t, "gdalinfo")
	path := filepath.Join(t.TempDir(), "out.gpkg")
	writeJob(t, path)

	// ogrinfo shows no SRS for a GeoPackage without vector layers, so the SQL query and gdalinfo show the raster SRS.
	tests := []struct {
		name string
		tool string
		args []string
		want []string
	}{
		{"ogrinfo opens the file with the GeoPackage driver", ogrinfo, []string{"-so", path},
			[]string{"using driver `GPKG' successful"}},
		{"ogrinfo lists a tiles table in EPSG:3857", ogrinfo,
			[]string{path, "-sql", "SELECT c.data_type, s.organization, s.organization_coordsys_id FROM gpkg_contents c JOIN gpkg_spatial_ref_sys s ON s.srs_id = c.srs_id"},
			[]string{"data_type (String) = tiles", "organization (String) = EPSG", "organization_coordsys_id (Integer64) = 3857"}},
		{"gdalinfo reads a GeoPackage raster in EPSG:3857", gdalinfo, []string{path},
			[]string{"Driver: GPKG/GeoPackage", `ID["EPSG",3857]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := run(t, tt.tool, tt.args...)
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("%s output does not hold %q:\n%s", filepath.Base(tt.tool), w, out)
				}
			}
		})
	}
}

// writeJob runs a GeoPackage job at zoom levels 8 and 9 from a test tile server to path.
func writeJob(t *testing.T, path string) {
	t.Helper()
	srv := testtiles.New(t, nil)
	source := &xmlparser.MapSource{
		Name: "Test XYZ", URL: srv.URLTemplate(), MinZoom: 0, MaxZoom: 18, TileType: "png",
		Kind: xmlparser.KindTile, Origin: xmlparser.OriginSnapshot, File: "maps/base/test.xml",
	}
	cfg := download.DefaultConfig()
	cfg.Timeout = 5 * time.Second
	cfg.UserAgent = "atak-dl-test"
	cfg.Format = output.GeoPackage
	cfg.Policy = policy.Entry{HostPatterns: []string{"127.0.0.1"}, PolicyClass: policy.Open, RequestsPerSecond: 1e6}
	b := orb.Bound{Min: orb.Point{-122.7, 45.5}, Max: orb.Point{-122.6, 45.6}}
	areas := []tilecalc.JobArea{{Area: tilecalc.Area{Kind: tilecalc.AreaBounds, Geometry: b}, MinZoom: 8, MaxZoom: 9}}
	res, err := download.NewEngine(cfg).DownloadTiles(t.Context(), source, areas, path, nil)
	if err != nil || !res.OK {
		t.Fatalf("DownloadTiles = %+v, %v; want a job that succeeds", res, err)
	}
}
