// Package gpkg writes GeoPackage 1.3 output files with one tile pyramid table (FR-output-formats-15 to 18).
// The format rules come from OGC GeoPackage 1.3.1, https://www.geopackage.org/spec131/.
// The reader rules come from GeoPackageLayerInfoSpi.java of ATAK-CIV, archived 4.6.0.5.
package gpkg

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/output/sqlitequeue"
)

// The header values of GeoPackage requirement 2 and of GeoPackage version 1.3 (FR-output-formats-18).
const (
	applicationID = 0x47504B47
	userVersion   = 10300
)

// srsID is the srs_id of EPSG:3857 in gpkg_spatial_ref_sys (FR-output-formats-16).
const srsID = 3857

// tileSize is the width and the height of a tile in pixels in the GoogleMapsCompatible tile matrix.
const tileSize = 256

// webMercatorRadius is the sphere radius of EPSG:3857 in meters.
const webMercatorRadius = 6378137.0

// worldExtent is the half width of the EPSG:3857 world in meters.
// The GoogleMapsCompatible tile matrix set covers the square from -worldExtent to worldExtent on both axes.
const worldExtent = math.Pi * webMercatorRadius

// The schema statements are the table definitions of GeoPackage 1.3.1 Annex C.
// They use IF NOT EXISTS, so that Open also opens an existing output file.
// ATAK and GDAL read only the gpkg_ tables and the tile pyramid table, so they ignore the atakdl_ tables.
const schema = `
	CREATE TABLE IF NOT EXISTS gpkg_spatial_ref_sys (
		srs_name TEXT NOT NULL,
		srs_id INTEGER NOT NULL PRIMARY KEY,
		organization TEXT NOT NULL,
		organization_coordsys_id INTEGER NOT NULL,
		definition TEXT NOT NULL,
		description TEXT
	);
	CREATE TABLE IF NOT EXISTS gpkg_contents (
		table_name TEXT NOT NULL PRIMARY KEY,
		data_type TEXT NOT NULL,
		identifier TEXT UNIQUE,
		description TEXT DEFAULT '',
		last_change DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
		min_x DOUBLE,
		min_y DOUBLE,
		max_x DOUBLE,
		max_y DOUBLE,
		srs_id INTEGER,
		CONSTRAINT fk_gc_r_srs_id FOREIGN KEY (srs_id) REFERENCES gpkg_spatial_ref_sys(srs_id)
	);
	CREATE TABLE IF NOT EXISTS gpkg_tile_matrix_set (
		table_name TEXT NOT NULL PRIMARY KEY,
		srs_id INTEGER NOT NULL,
		min_x DOUBLE NOT NULL,
		min_y DOUBLE NOT NULL,
		max_x DOUBLE NOT NULL,
		max_y DOUBLE NOT NULL,
		CONSTRAINT fk_gtms_table_name FOREIGN KEY (table_name) REFERENCES gpkg_contents(table_name),
		CONSTRAINT fk_gtms_srs FOREIGN KEY (srs_id) REFERENCES gpkg_spatial_ref_sys (srs_id)
	);
	CREATE TABLE IF NOT EXISTS gpkg_tile_matrix (
		table_name TEXT NOT NULL,
		zoom_level INTEGER NOT NULL,
		matrix_width INTEGER NOT NULL,
		matrix_height INTEGER NOT NULL,
		tile_width INTEGER NOT NULL,
		tile_height INTEGER NOT NULL,
		pixel_x_size DOUBLE NOT NULL,
		pixel_y_size DOUBLE NOT NULL,
		CONSTRAINT pk_ttm PRIMARY KEY (table_name, zoom_level),
		CONSTRAINT fk_tmm_table_name FOREIGN KEY (table_name) REFERENCES gpkg_contents(table_name)
	);
	CREATE TABLE IF NOT EXISTS atakdl_metadata (name TEXT NOT NULL PRIMARY KEY, value TEXT);
	CREATE TABLE IF NOT EXISTS atakdl_failed_tiles (zoom_level integer, tile_column integer, tile_row integer, reason text, attempts integer);
	CREATE UNIQUE INDEX IF NOT EXISTS atakdl_failed_tile_index ON atakdl_failed_tiles (zoom_level, tile_column, tile_row);
`

// The definitions are the OGC WKT of the EPSG registry, the same text that GDAL writes for these codes.
const (
	wkt4326 = `GEOGCS["WGS 84",DATUM["WGS_1984",SPHEROID["WGS 84",6378137,298.257223563,AUTHORITY["EPSG","7030"]],` +
		`AUTHORITY["EPSG","6326"]],PRIMEM["Greenwich",0,AUTHORITY["EPSG","8901"]],` +
		`UNIT["degree",0.0174532925199433,AUTHORITY["EPSG","9122"]],AXIS["Latitude",NORTH],AXIS["Longitude",EAST],` +
		`AUTHORITY["EPSG","4326"]]`
	wkt3857 = `PROJCS["WGS 84 / Pseudo-Mercator",GEOGCS["WGS 84",DATUM["WGS_1984",` +
		`SPHEROID["WGS 84",6378137,298.257223563,AUTHORITY["EPSG","7030"]],AUTHORITY["EPSG","6326"]],` +
		`PRIMEM["Greenwich",0,AUTHORITY["EPSG","8901"]],UNIT["degree",0.0174532925199433,AUTHORITY["EPSG","9122"]],` +
		`AUTHORITY["EPSG","4326"]],PROJECTION["Mercator_1SP"],PARAMETER["central_meridian",0],` +
		`PARAMETER["scale_factor",1],PARAMETER["false_easting",0],PARAMETER["false_northing",0],` +
		`UNIT["metre",1,AUTHORITY["EPSG","9001"]],AXIS["Easting",EAST],AXIS["Northing",NORTH],` +
		`EXTENSION["PROJ4","+proj=merc +a=6378137 +b=6378137 +lat_ts=0 +lon_0=0 +x_0=0 +y_0=0 +k=1 +units=m +nadgrids=@null +wktext +no_defs"],` +
		`AUTHORITY["EPSG","3857"]]`
)

// insertSRS writes the rows of GeoPackage requirement 11 and the EPSG:3857 row.
// INSERT OR IGNORE keeps the rows of an existing output file.
const insertSRS = `INSERT OR IGNORE INTO gpkg_spatial_ref_sys
	(srs_name, srs_id, organization, organization_coordsys_id, definition, description) VALUES
	('WGS 84 geodetic', 4326, 'EPSG', 4326, '` + wkt4326 + `', 'longitude/latitude coordinates in decimal degrees on the WGS 84 spheroid'),
	('Undefined cartesian SRS', -1, 'NONE', -1, 'undefined', 'undefined cartesian coordinate reference system'),
	('Undefined geographic SRS', 0, 'NONE', 0, 'undefined', 'undefined geographic coordinate reference system'),
	('WGS 84 / Pseudo-Mercator', 3857, 'EPSG', 3857, '` + wkt3857 + `', 'Web Mercator coordinates in meters')`

// upsertContents writes the tiles row of gpkg_contents.
// For an existing row, the bounds grow to hold the bounds of both jobs, so that the stored tiles stay inside them.
// SQLite min and max with a NULL argument return NULL, so coalesce gives each side the other value.
const upsertContents = `INSERT INTO gpkg_contents (table_name, data_type, identifier, description, min_x, min_y, max_x, max_y, srs_id)
	VALUES (?, 'tiles', ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (table_name) DO UPDATE SET
		last_change = strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		min_x = min(coalesce(min_x, excluded.min_x), coalesce(excluded.min_x, min_x)),
		min_y = min(coalesce(min_y, excluded.min_y), coalesce(excluded.min_y, min_y)),
		max_x = max(coalesce(max_x, excluded.max_x), coalesce(excluded.max_x, max_x)),
		max_y = max(coalesce(max_y, excluded.max_y), coalesce(excluded.max_y, max_y))`

// The statements that use the name of the tile pyramid table. quote gives the %s value.
const (
	createPyramid = `CREATE TABLE IF NOT EXISTS %s (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		zoom_level INTEGER NOT NULL,
		tile_column INTEGER NOT NULL,
		tile_row INTEGER NOT NULL,
		tile_data BLOB NOT NULL,
		UNIQUE (zoom_level, tile_column, tile_row))`
	// The upsert keeps the id of a replaced tile, which INSERT OR REPLACE would change.
	insertTile = `INSERT INTO %s (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)
		ON CONFLICT (zoom_level, tile_column, tile_row) DO UPDATE SET tile_data = excluded.tile_data`
	selectKeys = "SELECT zoom_level, tile_column, tile_row FROM %s"
)

// insertMatrix is the statement that the writer goroutine runs for each new zoom level of a batch.
const insertMatrix = `INSERT OR IGNORE INTO gpkg_tile_matrix
	(table_name, zoom_level, matrix_width, matrix_height, tile_width, tile_height, pixel_x_size, pixel_y_size)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

// Writer writes the tiles of one job to a GeoPackage output file, and it satisfies output.Writer.
// The queue of sqlitequeue owns the database, and it commits one transaction per 500 operations.
// Has, Put, Fail and Failed are safe for concurrent use. Close must not run at the same time as any of them.
type Writer struct {
	*sqlitequeue.Queue[sqlitequeue.Key]
	table string
	// zooms holds the zoom levels that gpkg_tile_matrix has a row for. Only the writer goroutine uses it after Open.
	zooms map[int]bool
}

func init() {
	output.Register(output.GeoPackage, readInfo, func(path string, metadata map[string]string) (output.Writer, error) {
		w, err := Open(path, metadata)
		if err != nil {
			// A nil *Writer in a non-nil interface would look like a writer to the caller.
			return nil, err
		}
		return w, nil
	}, readTile)
}

// Open returns a writer for the GeoPackage output file at path, and it creates the file when it does not exist.
// It reads these rows of metadata, and it stores every row in atakdl_metadata:
//   - atakdl_source names the tile pyramid table and is the identifier in gpkg_contents.
//   - bounds, as "west,south,east,north" in degrees, gives the gpkg_contents bounds in EPSG:3857.
//   - minzoom and maxzoom give the gpkg_tile_matrix rows.
//
// An existing file keeps its tile pyramid table, and Open loads the keys of the stored tiles, so that Has needs no query.
// It returns an error when the directory, the schema or a metadata write fails.
func Open(path string, metadata map[string]string) (*Writer, error) {
	db, err := sqlitequeue.OpenDB(path)
	if err != nil {
		return nil, err
	}
	table, err := setUp(db, metadata)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	zooms, err := matrixZooms(db, table)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	keys, err := storedKeys(db, table)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	w := &Writer{table: table, zooms: zooms}
	w.Queue = sqlitequeue.New(db, keys, sqlitequeue.Format[sqlitequeue.Key]{
		Key: func(z, x, y int) sqlitequeue.Key { return sqlitequeue.Key{Z: z, X: x, Y: y} },
		// The tile pyramid table and atakdl_failed_tiles hold XYZ rows.
		FailedRow: func(_, y int) int { return y },
		Insert:    w.insert,
	})
	return w, nil
}

// setUp writes the header, the schema and the rows of metadata in db, and it returns the name of the tile pyramid table.
func setUp(db *sql.DB, metadata map[string]string) (string, error) {
	// The header pragmas cannot run in a transaction that reads the schema, so they run first.
	for _, pragma := range []string{
		"PRAGMA application_id = " + strconv.Itoa(applicationID),
		"PRAGMA user_version = " + strconv.Itoa(userVersion),
	} {
		if _, err := db.Exec(pragma); err != nil {
			return "", fmt.Errorf("set the GeoPackage header: %w", err)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin the schema transaction: %w", err)
	}
	table, err := writeSchema(tx, metadata)
	if err != nil {
		return "", errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit the schema: %w", err)
	}
	return table, nil
}

// writeSchema creates the tables in tx and writes the rows of the job, and it returns the name of the tile pyramid table.
func writeSchema(tx *sql.Tx, metadata map[string]string) (string, error) {
	if _, err := tx.Exec(schema); err != nil {
		return "", fmt.Errorf("create the GeoPackage tables: %w", err)
	}
	if _, err := tx.Exec(insertSRS); err != nil {
		return "", fmt.Errorf("write gpkg_spatial_ref_sys: %w", err)
	}
	table, err := pyramidTable(tx, metadata["atakdl_source"])
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(fmt.Sprintf(createPyramid, quote(table))); err != nil {
		return "", fmt.Errorf("create the tile pyramid table %s: %w", table, err)
	}
	if err := writeTileRows(tx, table, metadata); err != nil {
		return "", err
	}
	for k, v := range metadata {
		if _, err := tx.Exec("INSERT OR REPLACE INTO atakdl_metadata (name, value) VALUES (?, ?)", k, v); err != nil {
			return "", fmt.Errorf("write atakdl_metadata %s: %w", k, err)
		}
	}
	return table, nil
}

// pyramidTable returns the name of the tile pyramid table in tx, or the name for the map source source in a new file.
// An existing output file keeps the table name in its gpkg_contents, so a resume writes to the same table.
func pyramidTable(tx *sql.Tx, source string) (string, error) {
	var table string
	err := tx.QueryRow("SELECT table_name FROM gpkg_contents WHERE data_type = 'tiles' ORDER BY table_name LIMIT 1").Scan(&table)
	if errors.Is(err, sql.ErrNoRows) {
		return tableName(source), nil
	}
	if err != nil {
		return "", fmt.Errorf("read gpkg_contents: %w", err)
	}
	return table, checkMatrixSet(tx, table)
}

// writeTileRows writes the gpkg_contents, gpkg_tile_matrix_set and gpkg_tile_matrix rows of table for the job.
func writeTileRows(tx *sql.Tx, table string, metadata map[string]string) error {
	var bounds [4]any
	if b, ok := parseBounds(metadata["bounds"]); ok {
		for i, v := range mercatorBounds(b) {
			bounds[i] = v
		}
	}
	if _, err := tx.Exec(upsertContents, table, metadata["atakdl_source"], metadata["description"],
		bounds[0], bounds[1], bounds[2], bounds[3], srsID); err != nil {
		return fmt.Errorf("write gpkg_contents: %w", err)
	}
	if _, err := tx.Exec("INSERT OR IGNORE INTO gpkg_tile_matrix_set (table_name, srs_id, min_x, min_y, max_x, max_y) VALUES (?, ?, ?, ?, ?, ?)",
		table, srsID, -worldExtent, -worldExtent, worldExtent, worldExtent); err != nil {
		return fmt.Errorf("write gpkg_tile_matrix_set: %w", err)
	}
	// A job without valid zoom rows gets its gpkg_tile_matrix rows from the tiles that Put stores.
	minZoom, minErr := strconv.Atoi(metadata["minzoom"])
	maxZoom, maxErr := strconv.Atoi(metadata["maxzoom"])
	if minErr != nil || maxErr != nil {
		return nil
	}
	for z := minZoom; z <= maxZoom; z++ {
		if _, err := tx.Exec(insertMatrix, matrixRow(table, z)...); err != nil {
			return fmt.Errorf("write gpkg_tile_matrix for zoom level %d: %w", z, err)
		}
	}
	return nil
}

// checkMatrixSet returns an error when the tile pyramid table of an existing file is not in the GoogleMapsCompatible tile matrix set.
// A GeoPackage from another tool can use another SRS, and an XYZ tile in it would show at the wrong place.
func checkMatrixSet(q queryer, table string) error {
	var srs int
	var minX, maxY float64
	err := q.QueryRow("SELECT srs_id, min_x, max_y FROM gpkg_tile_matrix_set WHERE table_name = ?", table).Scan(&srs, &minX, &maxY)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read gpkg_tile_matrix_set: %w", err)
	}
	if srs != srsID || minX != -worldExtent || maxY != worldExtent {
		return fmt.Errorf("tile pyramid table %s is not in the GoogleMapsCompatible tile matrix set of EPSG:3857", table)
	}
	return nil
}

// queryer is the query method that *sql.DB and *sql.Tx share, so that the writer and the tile reader use one check.
type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// quote returns name as an SQL identifier in double quotes.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// tableName returns the name of the tile pyramid table for the map source source.
// GeoPackage requirement 9 reserves the gpkg_ prefix, and SQLite reserves sqlite_.
// The writer owns the atakdl_ prefix, and a name that starts with a digit needs quotes in SQL.
func tableName(source string) string {
	if source == "" {
		return "tiles"
	}
	name := []byte(source)
	for i, c := range name {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' {
			name[i] = '_'
		}
	}
	s := string(name)
	lower := strings.ToLower(s)
	if s[0] >= '0' && s[0] <= '9' || strings.HasPrefix(lower, "gpkg_") || strings.HasPrefix(lower, "sqlite_") || strings.HasPrefix(lower, "atakdl_") {
		return "t_" + s
	}
	return s
}

// matrixRow returns the gpkg_tile_matrix values of table at zoom level z in the GoogleMapsCompatible tile matrix.
// GeoPackage requirement 45 holds, because 2^z tiles of tileSize pixels span the world width.
func matrixRow(table string, z int) []any {
	n := 1 << z
	pixel := 2 * worldExtent / float64(n*tileSize)
	return []any{table, z, n, n, tileSize, tileSize, pixel, pixel}
}

// parseBounds returns the four numbers of a bounds row "west,south,east,north", and false when the row does not hold four numbers.
func parseBounds(row string) ([4]float64, bool) {
	var b [4]float64
	parts := strings.Split(row, ",")
	if len(parts) != 4 {
		return b, false
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return b, false
		}
		b[i] = v
	}
	return b, true
}

// mercatorBounds returns the degree bounds b as min_x, min_y, max_x and max_y in EPSG:3857.
func mercatorBounds(b [4]float64) [4]float64 {
	x := func(lon float64) float64 { return webMercatorRadius * lon * math.Pi / 180 }
	y := func(lat float64) float64 { return webMercatorRadius * math.Log(math.Tan(math.Pi/4+lat*math.Pi/360)) }
	return [4]float64{x(b[0]), y(b[1]), x(b[2]), y(b[3])}
}

// matrixZooms returns the zoom levels that gpkg_tile_matrix has a row for in table.
func matrixZooms(db *sql.DB, table string) (map[int]bool, error) {
	rows, err := db.Query("SELECT zoom_level FROM gpkg_tile_matrix WHERE table_name = ?", table)
	if err != nil {
		return nil, fmt.Errorf("read gpkg_tile_matrix: %w", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	zooms := map[int]bool{}
	for rows.Next() {
		var z int
		if err := rows.Scan(&z); err != nil {
			return nil, fmt.Errorf("read gpkg_tile_matrix: %w", err)
		}
		zooms[z] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read gpkg_tile_matrix: %w", err)
	}
	return zooms, nil
}

// storedKeys returns the keys of the tiles in the tile pyramid table of db.
func storedKeys(db *sql.DB, table string) (map[sqlitequeue.Key]struct{}, error) {
	rows, err := db.Query(fmt.Sprintf(selectKeys, quote(table)))
	if err != nil {
		return nil, fmt.Errorf("read the stored tile keys: %w", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	keys := make(map[sqlitequeue.Key]struct{})
	for rows.Next() {
		var k sqlitequeue.Key
		if err := rows.Scan(&k.Z, &k.X, &k.Y); err != nil {
			return nil, fmt.Errorf("read the stored tile keys: %w", err)
		}
		keys[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the stored tile keys: %w", err)
	}
	return keys, nil
}

// readInfo returns the Info of the GeoPackage file at path, and false when the file is not a GeoPackage.
// A GeoPackage has the application_id of requirement 2 and a gpkg_contents table, the table that ATAK tests.
// It never writes to the file. A metadata row that is missing or not valid gives the zero value of its field.
// It returns an error when the file is not an SQLite database.
func readInfo(path string) (output.Info, bool, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return output.Info{}, false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	var id, contents int
	if err := db.QueryRow("PRAGMA application_id").Scan(&id); err != nil {
		return output.Info{}, false, fmt.Errorf("read application_id: %w", err)
	}
	if id != applicationID {
		return output.Info{}, false, nil
	}
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'gpkg_contents'").Scan(&contents); err != nil {
		return output.Info{}, false, fmt.Errorf("read the schema: %w", err)
	}
	if contents == 0 {
		return output.Info{}, false, nil
	}

	meta, err := readMetadata(db)
	if err != nil {
		return output.Info{}, false, err
	}
	info := output.Info{TileFormat: meta["format"], Source: meta["atakdl_source"], Areas: meta["atakdl_areas"]}
	if b, ok := parseBounds(meta["bounds"]); ok {
		info.Bounds = b[:]
	}
	// A GeoPackage from another tool has no zoom rows, so a missing or bad row gives 0.
	info.MinZoom, _ = strconv.Atoi(meta["minzoom"])
	info.MaxZoom, _ = strconv.Atoi(meta["maxzoom"])
	return info, true, nil
}

// readTile returns the data of the tile z, x, y in the GeoPackage file at path, and true when the file holds the tile.
// The row y is the XYZ row, and a GeoPackage stores the same row, so readTile does not flip it.
// It reads the tile pyramid table that gpkg_contents names, the same table that a resume writes to.
// It never writes to the file. It returns an error when the file is not a GeoPackage in EPSG:3857.
func readTile(path string, z, x, y int) ([]byte, bool, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	var table string
	err = db.QueryRow("SELECT table_name FROM gpkg_contents WHERE data_type = 'tiles' ORDER BY table_name LIMIT 1").Scan(&table)
	// The writer adds the gpkg_contents row before the first tile, so a file without the row holds no tile.
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read gpkg_contents: %w", err)
	}
	if err := checkMatrixSet(db, table); err != nil {
		return nil, false, err
	}
	var data []byte
	err = db.QueryRow(fmt.Sprintf("SELECT tile_data FROM %s WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?", quote(table)), z, x, y).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read tile %d/%d/%d: %w", z, x, y, err)
	}
	return data, true, nil
}

// readMetadata returns the rows of atakdl_metadata in db. A GeoPackage from another tool has no such table and gives no rows.
func readMetadata(db *sql.DB) (map[string]string, error) {
	meta := map[string]string{}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'atakdl_metadata'").Scan(&n); err != nil {
		return nil, fmt.Errorf("read the schema: %w", err)
	}
	if n == 0 {
		return meta, nil
	}
	rows, err := db.Query("SELECT name, value FROM atakdl_metadata WHERE value IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("read atakdl_metadata: %w", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, fmt.Errorf("read atakdl_metadata: %w", err)
		}
		meta[name] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read atakdl_metadata: %w", err)
	}
	return meta, nil
}

// insert writes tiles in tx, and it replaces a stored tile with the same key.
// It adds the gpkg_tile_matrix row of each new zoom level, because GeoPackage requirement 44 needs one for each zoom level that holds a tile.
// It returns the function that records the new zoom levels after the commit.
func (w *Writer) insert(tx *sql.Tx, tiles []sqlitequeue.Tile) (func(), error) {
	newZooms := w.newZooms(tiles)
	for _, z := range newZooms {
		if _, err := tx.Exec(insertMatrix, matrixRow(w.table, z)...); err != nil {
			return nil, fmt.Errorf("write gpkg_tile_matrix for zoom level %d: %w", z, err)
		}
	}
	// quote escapes the table name, so the statement holds no text from outside the writer.
	put, err := tx.Prepare(fmt.Sprintf(insertTile, quote(w.table)))
	if err != nil {
		return nil, fmt.Errorf("prepare the tile insert of %s: %w", w.table, err)
	}
	for _, t := range tiles {
		if _, err := put.Exec(t.Z, t.X, t.Y, t.Data); err != nil {
			return nil, errors.Join(fmt.Errorf("write tile %d/%d/%d: %w", t.Z, t.X, t.Y, err), sqlitequeue.CloseAll(put))
		}
	}
	// The zoom levels count only after the commit, so a rolled-back batch adds its rows again.
	return func() {
		for _, z := range newZooms {
			w.zooms[z] = true
		}
	}, sqlitequeue.CloseAll(put)
}

// newZooms returns the zoom levels of tiles that gpkg_tile_matrix has no row for.
func (w *Writer) newZooms(tiles []sqlitequeue.Tile) []int {
	var zooms []int
	for _, t := range tiles {
		if !w.zooms[t.Z] && !slices.Contains(zooms, t.Z) {
			zooms = append(zooms, t.Z)
		}
	}
	return zooms
}
