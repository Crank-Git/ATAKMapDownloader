package output_test

import (
	"bytes"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"

	// The blank import registers the GeoPackage writer that the job uses.
	_ "github.com/crank-git/atak-map-downloader/internal/output/gpkg"
)

// The ATAK rules in this file are a port of the GeoPackage reader of ATAK-CIV, archived 4.6.0.5, in
// https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/tree/main/takkernel/engine/src/main/java/com/atakmap/:
//   - map/layer/raster/gpkg/GeoPackageLayerInfoSpi.java: create and getSRID.
//   - map/gpkg/GeoPackage.java: isGeoPackage.
//   - map/layer/raster/gpkg/GeoPackageMosaicDatabase.java: createTileTableSpec.
//
// The GeoPackage rules are OGC GeoPackage 1.3.1: https://www.geopackage.org/spec131/

// webMercatorRadius is the sphere radius of EPSG:3857 in meters.
const webMercatorRadius = 6378137.0

func TestGeoPackageOutputFileConformsToTheATAKReaderAndGeoPackage131(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.gpkg")
	edges := writeJob(t, path, output.GeoPackage)

	// isGeoPackage reads the SQLite header before it opens the file.
	header := make([]byte, 16)
	f, err := os.Open(path) //nolint:gosec // The path is in the temporary directory of the test.
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	_, err = f.Read(header)
	_ = f.Close() // The test only reads the file, so a close error loses no data.
	if err != nil || !bytes.Equal(header, []byte("SQLite format 3\x00")) {
		t.Fatalf("file header = %q, %v; want the SQLite header", header, err)
	}

	db := openReadOnly(t, path)
	checkGeoPackageHeader(t, db)
	for _, table := range []string{"gpkg_spatial_ref_sys", "gpkg_contents", "gpkg_tile_matrix_set", "gpkg_tile_matrix"} {
		if len(columns(t, db, table)) == 0 {
			t.Errorf("output file has no %s table, which GeoPackage 1.3.1 requires for tiles", table)
		}
	}
	checkGeoPackageSRS(t, db)
	checkATAKTileTable(t, db, edges)
}

// checkGeoPackageHeader checks GeoPackage requirement 2 and the user_version of GeoPackage 1.3 (FR-output-formats-18).
func checkGeoPackageHeader(t *testing.T, db *sql.DB) {
	t.Helper()
	for pragma, want := range map[string]int{"application_id": 0x47504B47, "user_version": 10300} {
		var got int
		if err := db.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", pragma, err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %#x, want %#x", pragma, got, want)
		}
	}
}

// checkGeoPackageSRS checks the rows that GeoPackage requirement 11 requires in gpkg_spatial_ref_sys.
func checkGeoPackageSRS(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, id := range []int{4326, -1, 0} {
		var n int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM gpkg_spatial_ref_sys WHERE srs_id = ?", id).Scan(&n); err != nil {
			t.Fatalf("read srs_id %d: %v", id, err)
		}
		if n != 1 {
			t.Errorf("gpkg_spatial_ref_sys holds %d rows for srs_id %d, want 1", n, id)
		}
	}
}

// checkATAKTileTable applies createTileTableSpec to the one tiles row of gpkg_contents.
// ATAK reads the tile grid from the upper-left corner of the tile matrix set, so tile_row counts down from the north edge.
func checkATAKTileTable(t *testing.T, db *sql.DB, want *tilecalc.GeographicBounds) {
	t.Helper()
	var table string
	var minX, minY, maxX, maxY sql.NullFloat64
	err := db.QueryRowContext(t.Context(), "SELECT table_name, min_x, min_y, max_x, max_y FROM gpkg_contents WHERE data_type = 'tiles'").
		Scan(&table, &minX, &minY, &maxX, &maxY)
	if err != nil {
		t.Fatalf("read the tiles row of gpkg_contents: %v", err)
	}
	if !minX.Valid || !minY.Valid || !maxX.Valid || !maxY.Valid {
		t.Errorf("gpkg_contents bounds = %v %v %v %v, want four values, which ATAK reads as the coverage", minX, minY, maxX, maxY)
	}

	// getSRID returns organization_coordsys_id for an EPSG row, and ProjectionFactory needs 3857 for Web Mercator.
	var organization string
	var srid int
	var originX, originY float64
	err = db.QueryRowContext(t.Context(), `SELECT s.organization, s.organization_coordsys_id, m.min_x, m.max_y
		FROM gpkg_tile_matrix_set m JOIN gpkg_spatial_ref_sys s ON s.srs_id = m.srs_id WHERE m.table_name = ?`, table).
		Scan(&organization, &srid, &originX, &originY)
	if err != nil {
		t.Fatalf("read the tile matrix set of %s: %v", table, err)
	}
	if (organization != "EPSG" && organization != "epsg") || srid != 3857 {
		t.Errorf("tile matrix set SRS = %s %d, want EPSG 3857", organization, srid)
	}

	// createTileTableSpec reads zoomLevels[0], so a table with no gpkg_tile_matrix row gives no coverage.
	z := conformanceArea.Zoom
	var tileWidth int
	var pixelX, pixelY float64
	err = db.QueryRowContext(t.Context(),
		"SELECT tile_width, pixel_x_size, pixel_y_size FROM gpkg_tile_matrix WHERE table_name = ? AND zoom_level = ?", table, z).
		Scan(&tileWidth, &pixelX, &pixelY)
	if err != nil {
		t.Fatalf("read the tile matrix of zoom level %d: %v", z, err)
	}

	var minCol, maxCol, minRow, maxRow int
	err = db.QueryRowContext(t.Context(), //nolint:gosec // The table name comes from the output file of the test.
		"SELECT min(tile_column), max(tile_column), min(tile_row), max(tile_row) FROM \""+table+"\" WHERE zoom_level = ?", z).
		Scan(&minCol, &maxCol, &minRow, &maxRow)
	if err != nil {
		t.Fatalf("read the tile grid of %s: %v", table, err)
	}
	tileSpanX := pixelX * float64(tileWidth)
	tileSpanY := pixelY * float64(tileWidth)
	got := map[string]float64{
		"west":  lonOf(originX + float64(minCol)*tileSpanX),
		"east":  lonOf(originX + float64(maxCol+1)*tileSpanX),
		"north": latOf(originY - float64(minRow)*tileSpanY),
		"south": latOf(originY - float64(maxRow+1)*tileSpanY),
	}
	wantEdges := map[string]float64{"south": want.MinLat, "west": want.MinLon, "north": want.MaxLat, "east": want.MaxLon}
	for side, w := range wantEdges {
		if math.Abs(got[side]-w) > 1e-9 {
			t.Errorf("ATAK %s edge = %f, want %f", side, got[side], w)
		}
	}
}

// lonOf returns the longitude of the EPSG:3857 x coordinate x.
func lonOf(x float64) float64 {
	return x / webMercatorRadius * 180 / math.Pi
}

// latOf returns the latitude of the EPSG:3857 y coordinate y.
func latOf(y float64) float64 {
	return (2*math.Atan(math.Exp(y/webMercatorRadius)) - math.Pi/2) * 180 / math.Pi
}
