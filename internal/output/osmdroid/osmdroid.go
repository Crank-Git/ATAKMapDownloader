// Package osmdroid writes OSMDroid SQLite output files that ATAK reads as an OSMDroid tile cache (FR-output-formats-11 to 14).
// The format rules come from OSMDroidTileContainer.java, OSMUtils.java and TilesetInfo.java of ATAK-CIV, archived 4.6.0.5.
// docs/adr/0011-osmdroid-output.md records the schema, the key and the zoom limit.
package osmdroid

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/output/sqlitequeue"
)

// MaxZoom is the highest zoom level that the writer accepts.
// OSMUtils.getOSMDroidSQLiteZoomLevel of ATAK decodes no zoom level from a key above zoom level 27.
// The key itself fits in int64 up to zoom level 29.
const MaxZoom = 27

// ErrZoomOutOfRange is the error of Open and Put for a zoom level above MaxZoom.
var ErrZoomOutOfRange = errors.New("zoom_out_of_range")

// srid is the value of the srid row of ATAK_metadata, which TilesetInfo.java reads with Integer.parseInt.
const srid = "3857"

// The tiles, ATAK_catalog and ATAK_metadata statements are those of OSMDroidTileContainer.createTables.
// ATAK reads a file with both ATAK tables the same as a tile cache that ATAK wrote, and an empty ATAK_catalog gives no expiration.
// TilesetInfo.java tests only the column set of tiles, so it ignores the atakdl_ tables.
// The statements use IF NOT EXISTS, so that Open also opens an existing output file.
const schema = `
	CREATE TABLE IF NOT EXISTS tiles (key INTEGER PRIMARY KEY, provider TEXT, tile BLOB);
	CREATE TABLE IF NOT EXISTS ATAK_catalog (key INTEGER PRIMARY KEY, access INTEGER, expiration INTEGER, size INTEGER);
	CREATE TABLE IF NOT EXISTS ATAK_metadata (key TEXT, value TEXT);
	CREATE TABLE IF NOT EXISTS atakdl_metadata (name TEXT NOT NULL PRIMARY KEY, value TEXT);
	CREATE TABLE IF NOT EXISTS atakdl_failed_tiles (zoom_level integer, tile_column integer, tile_row integer, reason text, attempts integer);
	CREATE UNIQUE INDEX IF NOT EXISTS atakdl_failed_tile_index ON atakdl_failed_tiles (zoom_level, tile_column, tile_row);
`

// insertTile is the statement that the writer goroutine runs for each Put.
const insertTile = "INSERT OR REPLACE INTO tiles (key, provider, tile) VALUES (?, ?, ?)"

// Writer writes the tiles of one job to an OSMDroid output file, and it satisfies output.Writer.
// The queue of sqlitequeue owns the database, and it commits one transaction per 500 operations.
// Has, Put, Fail and Failed are safe for concurrent use. Close must not run at the same time as any of them.
type Writer struct {
	*sqlitequeue.Queue[int64]
	provider string
}

func init() {
	output.Register(output.OSMDroid, readInfo, func(path string, metadata map[string]string) (output.Writer, error) {
		w, err := Open(path, metadata)
		if err != nil {
			// A nil *Writer in a non-nil interface would look like a writer to the caller.
			return nil, err
		}
		return w, nil
	}, readTile)
}

// tileKey returns the OSMDroid key of the tile z, x, y, the same as OSMUtils.getOSMDroidSQLiteIndex (FR-output-formats-12).
// The row y is the XYZ row.
func tileKey(z, x, y int) int64 {
	return (((int64(z) << z) + int64(x)) << z) + int64(y)
}

// Open returns a writer for the OSMDroid output file at path, and it creates the file when it does not exist.
// It reads these rows of metadata, and it stores every row in atakdl_metadata:
//   - atakdl_source is the provider of each tile (FR-output-formats-13).
//   - maxzoom is the highest zoom level of the job.
//
// An existing file keeps its tiles, and Open loads their keys, so that Has needs no query.
// It returns ErrZoomOutOfRange before it creates the file when maxzoom is above MaxZoom.
// It returns an error when the directory, the schema or a metadata write fails, and when an existing file is not in EPSG:3857.
func Open(path string, metadata map[string]string) (*Writer, error) {
	if maxZoom, err := strconv.Atoi(metadata["maxzoom"]); err == nil && maxZoom > MaxZoom {
		return nil, zoomError(maxZoom)
	}
	db, err := sqlitequeue.OpenDB(path)
	if err != nil {
		return nil, err
	}
	if err := setUp(db, metadata); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	keys, err := storedKeys(db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	w := &Writer{provider: metadata["atakdl_source"]}
	w.Queue = sqlitequeue.New(db, keys, sqlitequeue.Format[int64]{
		Key: tileKey,
		// atakdl_failed_tiles holds XYZ rows, the same rows as the key.
		FailedRow: func(_, y int) int { return y },
		Insert:    w.insert,
	})
	return w, nil
}

// zoomError returns ErrZoomOutOfRange with a message that names z.
func zoomError(z int) error {
	return fmt.Errorf("%w: zoom level %d is above %d, the highest zoom level that ATAK reads from an OSMDroid file", ErrZoomOutOfRange, z, MaxZoom)
}

// setUp creates the tables in db and writes the srid row and the rows of metadata in one transaction.
func setUp(db *sql.DB, metadata map[string]string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin the schema transaction: %w", err)
	}
	if err := writeSchema(tx, metadata); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit the schema: %w", err)
	}
	return nil
}

// writeSchema creates the tables in tx and writes the srid row and the rows of metadata.
func writeSchema(tx *sql.Tx, metadata map[string]string) error {
	if _, err := tx.Exec(schema); err != nil {
		return fmt.Errorf("create the OSMDroid tables: %w", err)
	}
	// ATAK_metadata has no key constraint, so the insert tests for the row itself.
	if _, err := tx.Exec("INSERT INTO ATAK_metadata (key, value) SELECT 'srid', ? WHERE NOT EXISTS (SELECT 1 FROM ATAK_metadata WHERE key = 'srid')", srid); err != nil {
		return fmt.Errorf("write the srid row of ATAK_metadata: %w", err)
	}
	if err := checkSRID(tx); err != nil {
		return err
	}
	for k, v := range metadata {
		if _, err := tx.Exec("INSERT OR REPLACE INTO atakdl_metadata (name, value) VALUES (?, ?)", k, v); err != nil {
			return fmt.Errorf("write atakdl_metadata %s: %w", k, err)
		}
	}
	return nil
}

// checkSRID returns an error when the srid row of ATAK_metadata in tx is not EPSG:3857.
// An OSMDroid file that ATAK wrote can be in EPSG:4326, and an XYZ tile in it would show at the wrong place.
// OSMDroidTileContainer reads 900913 as 3857.
func checkSRID(tx *sql.Tx) error {
	var value string
	if err := tx.QueryRow("SELECT value FROM ATAK_metadata WHERE key = 'srid' LIMIT 1").Scan(&value); err != nil {
		return fmt.Errorf("read the srid row of ATAK_metadata: %w", err)
	}
	if v := strings.TrimSpace(value); v != srid && v != "900913" {
		return fmt.Errorf("output file has srid %s in ATAK_metadata, and the OSMDroid writer writes only EPSG:3857", value)
	}
	return nil
}

// storedKeys returns the keys of the tiles in db.
func storedKeys(db *sql.DB) (map[int64]struct{}, error) {
	rows, err := db.Query("SELECT key FROM tiles")
	if err != nil {
		return nil, fmt.Errorf("read the stored tile keys: %w", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	keys := make(map[int64]struct{})
	for rows.Next() {
		var k int64
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("read the stored tile keys: %w", err)
		}
		keys[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the stored tile keys: %w", err)
	}
	return keys, nil
}

// readInfo returns the Info of the OSMDroid file at path, and false when the file is not an OSMDroid file.
// The tiles table of an OSMDroid file has exactly the columns key, provider and tile, the rule of OSMUtils.isOSMDroidSQLite.
// It never writes to the file. A metadata row that is missing or not valid gives the zero value of its field.
// It returns an error when the file is not an SQLite database.
func readInfo(path string) (output.Info, bool, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return output.Info{}, false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	var cols string
	if err := db.QueryRow("SELECT coalesce(group_concat(name, ','), '') FROM (SELECT name FROM pragma_table_info('tiles') ORDER BY name)").Scan(&cols); err != nil {
		return output.Info{}, false, fmt.Errorf("read the schema: %w", err)
	}
	if cols != "key,provider,tile" {
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
	// An OSMDroid file that ATAK wrote has no zoom rows, so a missing or bad row gives 0.
	info.MinZoom, _ = strconv.Atoi(meta["minzoom"])
	info.MaxZoom, _ = strconv.Atoi(meta["maxzoom"])
	return info, true, nil
}

// readTile returns the data of the tile z, x, y in the OSMDroid file at path, and true when the file holds the tile.
// The row y is the XYZ row, and the key of the tile holds the same row.
// It never writes to the file. It returns an error when the file has no tiles table with a key column.
func readTile(path string, z, x, y int) ([]byte, bool, error) {
	// The writer stores no tile above MaxZoom, and the key of zoom level 30 overflows int64.
	if z > MaxZoom {
		return nil, false, nil
	}
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	var data []byte
	err = db.QueryRow("SELECT tile FROM tiles WHERE key = ?", tileKey(z, x, y)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read tile %d/%d/%d: %w", z, x, y, err)
	}
	return data, true, nil
}

// readMetadata returns the rows of atakdl_metadata in db. An OSMDroid file that ATAK wrote has no such table and gives no rows.
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

// Put queues the tile z, x, y with data for the writer goroutine. The row y is the XYZ row.
// It returns ErrZoomOutOfRange for z above MaxZoom and queues nothing.
// Otherwise it returns the error of sqlitequeue.Queue.Put.
func (w *Writer) Put(z, x, y int, data []byte) error {
	if z > MaxZoom {
		return zoomError(z)
	}
	return w.Queue.Put(z, x, y, data)
}

// insert writes tiles in tx with the OSMDroid key and the provider of the writer, and it replaces a stored tile with the same key.
func (w *Writer) insert(tx *sql.Tx, tiles []sqlitequeue.Tile) (func(), error) {
	put, err := tx.Prepare(insertTile)
	if err != nil {
		return nil, fmt.Errorf("prepare %q: %w", insertTile, err)
	}
	for _, t := range tiles {
		if _, err := put.Exec(tileKey(t.Z, t.X, t.Y), w.provider, t.Data); err != nil {
			return nil, errors.Join(fmt.Errorf("write tile %d/%d/%d: %w", t.Z, t.X, t.Y, err), sqlitequeue.CloseAll(put))
		}
	}
	return nil, sqlitequeue.CloseAll(put)
}
