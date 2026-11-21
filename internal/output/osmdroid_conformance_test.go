package output_test

import (
	"database/sql"
	"math"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"

	// The blank import registers the OSMDroid writer that the job uses.
	_ "github.com/crank-git/atak-map-downloader/internal/output/osmdroid"
)

// The ATAK rules in this file are a port of the OSMDroid branch of parseSQLiteDb in TilesetInfo.java of ATAK-CIV, archived 4.6.0.5:
// https://github.com/deptofdefense/AndroidTacticalAssaultKit-CIV/blob/main/takkernel/engine/src/main/java/com/atakmap/android/maps/tilesets/TilesetInfo.java
// The key functions are the OSMDroid SQLite functions of takkernel/engine/src/main/java/com/atakmap/map/layer/raster/osm/OSMUtils.java.

func TestOSMDroidOutputFileConformsToTheATAKReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.sqlite")
	edges := writeJob(t, path, output.OSMDroid)
	db := openReadOnly(t, path)

	// isOSMDroidSQLite compares the column set of the tiles table for equality, before any other rule.
	if got := atakDatasetType(t, db); got != "osmdroid" {
		t.Fatalf("ATAK dataset type = %q, want osmdroid", got)
	}

	// parseSQLiteDb returns null for a file without tiles, and it reads the provider of the tile with the lowest key.
	var minKey, maxKey int64
	var provider string
	if err := db.QueryRowContext(t.Context(), "SELECT key, provider FROM tiles ORDER BY key ASC LIMIT 1").Scan(&minKey, &provider); err != nil {
		t.Fatalf("read the lowest key: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "SELECT key FROM tiles ORDER BY key DESC LIMIT 1").Scan(&maxKey); err != nil {
		t.Fatalf("read the highest key: %v", err)
	}
	minLevel, maxLevel := osmZoomLevel(minKey), osmZoomLevel(maxKey)
	if minLevel != conformanceArea.Zoom || maxLevel != conformanceArea.Zoom {
		t.Errorf("ATAK zoom levels = %d to %d, want %d", minLevel, maxLevel, conformanceArea.Zoom)
	}
	if provider != "Test XYZ" {
		t.Errorf("provider = %q, want the map source name Test XYZ", provider)
	}

	// Integer.parseInt reads the srid value, and an exception there keeps the default 3857 without a warning.
	var srid string
	if err := db.QueryRowContext(t.Context(), "SELECT value FROM ATAK_metadata WHERE key = 'srid'").Scan(&srid); err != nil {
		t.Fatalf("read the srid row of ATAK_metadata: %v", err)
	}
	if n, err := strconv.Atoi(srid); err != nil || n != 3857 {
		t.Errorf("ATAK_metadata srid = %q, want the integer 3857", srid)
	}

	checkOSMDroidCorners(t, db, minLevel, edges)
}

// checkOSMDroidCorners applies the bounds discovery of parseSQLiteDb to the keys at zoom level minLevel.
// ATAK reads the row of the key as an XYZ row, so TMS rows in the file would give corners outside the area.
func checkOSMDroidCorners(t *testing.T, db *sql.DB, minLevel int, want *tilecalc.GeographicBounds) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT key FROM tiles WHERE key <= ?", osmMaxIndex(minLevel))
	if err != nil {
		t.Fatalf("read the keys of zoom level %d: %v", minLevel, err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, -1, -1
	for rows.Next() {
		var key int64
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("read the keys of zoom level %d: %v", minLevel, err)
		}
		x, y := osmTileX(key), osmTileY(key)
		minX, maxX = min(minX, x), max(maxX, x)
		minY, maxY = min(minY, y), max(maxY, y)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the keys of zoom level %d: %v", minLevel, err)
	}

	got := map[string]float64{
		"south": mapnikTileLat(minLevel, maxY+1),
		"west":  mapnikTileLng(minLevel, minX),
		"north": mapnikTileLat(minLevel, minY),
		"east":  mapnikTileLng(minLevel, maxX+1),
	}
	wantCorners := map[string]float64{"south": want.MinLat, "west": want.MinLon, "north": want.MaxLat, "east": want.MaxLon}
	for side, w := range wantCorners {
		if math.Abs(got[side]-w) > 1e-9 {
			t.Errorf("ATAK %s edge = %f, want %f", side, got[side], w)
		}
	}
}

// osmMinIndex is OSMUtils.getOSMDroidSQLiteMinIndex.
func osmMinIndex(z int) int64 {
	if z < 0 || z > 28 {
		return -1
	}
	return int64(z) << (z * 2)
}

// osmMaxIndex is OSMUtils.getOSMDroidSQLiteMaxIndex.
func osmMaxIndex(z int) int64 {
	return osmMinIndex(z+1) - 1
}

// osmZoomLevel is OSMUtils.getOSMDroidSQLiteZoomLevel.
func osmZoomLevel(index int64) int {
	for i := range 29 {
		if index <= osmMaxIndex(i) {
			return i
		}
	}
	return -1
}

// osmTileX is OSMUtils.getOSMDroidSQLiteTileX.
func osmTileX(index int64) int {
	z := osmZoomLevel(index)
	return int(index>>z - int64(z)<<z)
}

// osmTileY is OSMUtils.getOSMDroidSQLiteTileY.
func osmTileY(index int64) int {
	z := osmZoomLevel(index)
	return int(index & ^(-1 << z))
}
