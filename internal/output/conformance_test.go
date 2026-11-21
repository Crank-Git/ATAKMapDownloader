package output_test

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/policy"
	"github.com/crank-git/atak-map-downloader/internal/testtiles"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"

	// The blank import registers the MBTiles writer that the job uses.
	_ "github.com/crank-git/atak-map-downloader/internal/mbtiles"
)

// The ATAK rules in this file are a port of TilesetInfo.java in ATAK-CIV, archived 4.6.0.5:
// https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/android/maps/tilesets/TilesetInfo.java
// The OSMDroid rule is OSMUtils.isOSMDroidSQLite in takkernel/engine/src/main/java/com/atakmap/map/layer/raster/osm/OSMUtils.java.
// The metadata rules are the MBTiles 1.3 spec: https://github.com/mapbox/mbtiles-spec/blob/master/1.3/spec.md

// atakColumnSets are the tiles table column sets that parseSQLiteDb tests, by the dataset type that each gives.
var atakColumnSets = map[string][]string{
	// OSMDROID_SQLITE_TILES_TABLE_COLUMNS. OSMUtils tests it before any other set.
	"osmdroid": {"key", "provider", "tile"},
	// NETT_WARRIOR_TILES_TABLE_COLUMN_NAMES, which createMOMAPDataset reads.
	"momap": {"tile_column", "tile_data", "tile_row", "zoom_level"},
	// MBTILES_TILES_TABLE_COLUMN_NAMES, which createMBTilesDataset reads.
	"mbtiles": {"tile_alpha", "tile_column", "tile_data", "tile_row", "zoom_level"},
}

// conformanceArea is a 2×2 tile area at zoom level 10.
var conformanceArea = tilecalc.TileBounds{MinX: 300, MaxX: 301, MinY: 400, MaxY: 401, Zoom: 10}

func TestMBTilesOutputFileConformsToTheATAKReaderAndMBTiles13(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mbtiles")
	edges := writeJob(t, path)
	db := openReadOnly(t, path)

	// parseSQLiteDb returns null for a file with the .gpkg extension before it opens the file.
	if strings.HasSuffix(path, ".gpkg") {
		t.Fatalf("output file %s has the .gpkg extension", path)
	}
	if got := atakDatasetType(t, db); got != "momap" {
		t.Fatalf("ATAK dataset type = %q, want momap", got)
	}

	// createMOMAPDataset reads minzoom and maxzoom with Integer.parseInt, so a value that is not an integer throws.
	meta := metadataRows(t, db)
	minLevel, err := strconv.Atoi(meta["minzoom"])
	if err != nil || minLevel != conformanceArea.Zoom {
		t.Errorf("metadata minzoom = %q, want the integer %d", meta["minzoom"], conformanceArea.Zoom)
	}
	if maxLevel, err := strconv.Atoi(meta["maxzoom"]); err != nil || maxLevel != conformanceArea.Zoom {
		t.Errorf("metadata maxzoom = %q, want the integer %d", meta["maxzoom"], conformanceArea.Zoom)
	}
	// setImageExt takes the format value, and the MBTiles 1.3 spec lists the valid values.
	if f := meta["format"]; f != "png" && f != "jpg" && f != "webp" && f != "pbf" {
		t.Errorf("metadata format = %q, want one of png, jpg, webp, pbf", f)
	}

	checkATAKCorners(t, db, conformanceArea.Zoom, edges)
	checkMBTiles13Metadata(t, db, meta)
}

// writeJob downloads the tiles of conformanceArea from a test tile server to path, and returns the tile edges of the area.
func writeJob(t *testing.T, path string) *tilecalc.GeographicBounds {
	t.Helper()
	srv := testtiles.New(t, nil)
	source := &xmlparser.MapSource{
		Name: "Test XYZ", URL: srv.URLTemplate(), MinZoom: 0, MaxZoom: 18, TileType: "png",
		Kind: xmlparser.KindTile, Origin: xmlparser.OriginSnapshot, File: "maps/base/test.xml",
	}
	cfg := download.DefaultConfig()
	cfg.Timeout = 5 * time.Second
	cfg.UserAgent = "atak-dl-test"
	cfg.Policy = policy.Entry{HostPatterns: []string{"127.0.0.1"}, PolicyClass: policy.Open, RequestsPerSecond: 1e6}

	edges := tilecalc.NewTileCalculator().TileBoundsToGeographic(&conformanceArea)
	// The tile edges belong to the next tile, so the bounds move inside them.
	const inset = 1e-6
	bounds, err := tilecalc.NewGeographicBounds(edges.MinLat+inset, edges.MaxLat-inset, edges.MinLon+inset, edges.MaxLon-inset)
	if err != nil {
		t.Fatalf("make bounds: %v", err)
	}
	res, err := download.NewEngine(cfg).DownloadTiles(context.Background(), source, bounds, []int{conformanceArea.Zoom}, path, nil)
	if err != nil || !res.OK || res.Downloaded != int64(conformanceArea.TileCount()) {
		t.Fatalf("DownloadTiles = %+v, %v; want %d downloaded tiles", res, err, conformanceArea.TileCount())
	}
	return edges
}

func openReadOnly(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		t.Fatalf("open output file: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() }) // The test only reads the file, so a close error loses no data.
	return db
}

// atakDatasetType returns the dataset type that parseSQLiteDb gives the file, or "" when it rejects the file.
// Each rule compares the column set of the tiles table for equality, so one extra column changes the result.
func atakDatasetType(t *testing.T, db *sql.DB) string {
	t.Helper()
	got := strings.Join(columns(t, db, "tiles"), ",")
	for _, kind := range []string{"osmdroid", "momap", "mbtiles"} {
		if got == strings.Join(atakColumnSets[kind], ",") {
			return kind
		}
	}
	return ""
}

// columns returns the sorted column names of table.
func columns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT name FROM pragma_table_info(?) ORDER BY name", table)
	if err != nil {
		t.Fatalf("read the columns of %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read the columns of %s: %v", table, err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the columns of %s: %v", table, err)
	}
	return names
}

func metadataRows(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT name, value FROM metadata")
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	meta := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			t.Fatalf("read metadata: %v", err)
		}
		meta[name] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	return meta
}

// checkATAKCorners applies the corner math of createMOMAPDataset to the tile rows at zoom level z.
// ATAK reads tile_row as a TMS row, so XYZ rows in the file would give corners outside the area.
func checkATAKCorners(t *testing.T, db *sql.DB, z int, want *tilecalc.GeographicBounds) {
	t.Helper()
	var minX, maxX, minY, maxY int
	err := db.QueryRowContext(t.Context(),
		"SELECT min(tile_column), max(tile_column), min(tile_row), max(tile_row) FROM tiles WHERE zoom_level = ?", z).
		Scan(&minX, &maxX, &minY, &maxY)
	if err != nil {
		t.Fatalf("read the tile grid: %v", err)
	}
	n := 1 << z
	got := map[string]float64{
		"south": mapnikTileLat(z, n-minY),
		"west":  mapnikTileLng(z, minX),
		"north": mapnikTileLat(z, n-maxY-1),
		"east":  mapnikTileLng(z, maxX+1),
	}
	wantCorners := map[string]float64{"south": want.MinLat, "west": want.MinLon, "north": want.MaxLat, "east": want.MaxLon}
	for side, w := range wantCorners {
		if math.Abs(got[side]-w) > 1e-9 {
			t.Errorf("ATAK %s edge = %f, want %f", side, got[side], w)
		}
	}
}

// mapnikTileLat is OSMUtils.mapnikTileLat.
func mapnikTileLat(level, ytile int) float64 {
	n := float64(int(1) << level)
	return math.Atan(math.Sinh(math.Pi*(1-2*float64(ytile)/n))) * 180 / math.Pi
}

// mapnikTileLng is OSMUtils.mapnikTileLng.
func mapnikTileLng(level, xtile int) float64 {
	return float64(xtile)/float64(int(1)<<level)*360 - 180
}

// checkMBTiles13Metadata checks the MUST rules of the Metadata section of the MBTiles 1.3 spec.
func checkMBTiles13Metadata(t *testing.T, db *sql.DB, meta map[string]string) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT name, type FROM pragma_table_info('metadata') ORDER BY cid")
	if err != nil {
		t.Fatalf("read the metadata columns: %v", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	var got []string
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			t.Fatalf("read the metadata columns: %v", err)
		}
		got = append(got, name+" "+strings.ToLower(typ))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the metadata columns: %v", err)
	}
	if strings.Join(got, ", ") != "name text, value text" {
		t.Errorf("metadata columns = %v, want exactly name text, value text", got)
	}
	for _, key := range []string{"name", "format"} {
		if meta[key] == "" {
			t.Errorf("metadata has no %s row, which MBTiles 1.3 requires", key)
		}
	}
}
