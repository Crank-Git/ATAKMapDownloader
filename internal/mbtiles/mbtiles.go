// Package mbtiles provides MBTiles database creation for ATAK compatibility.
package mbtiles

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/output"
	"github.com/crank-git/atak-map-downloader/internal/output/sqlitequeue"
)

// The schema statements use IF NOT EXISTS, so that Open also opens an existing output file.
// SQLite stores each statement in sqlite_master without the IF NOT EXISTS clause (FR-foundation-4).
// The ATAK reader reads only the tiles table, so it ignores atakdl_failed_tiles (FR-robust-downloads-21).
const schema = `
	CREATE TABLE IF NOT EXISTS metadata (name text, value text);
	CREATE TABLE IF NOT EXISTS tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob);
	CREATE UNIQUE INDEX IF NOT EXISTS name ON metadata (name);
	CREATE UNIQUE INDEX IF NOT EXISTS tile_index ON tiles (zoom_level, tile_column, tile_row);
	CREATE TABLE IF NOT EXISTS atakdl_failed_tiles (zoom_level integer, tile_column integer, tile_row integer, reason text, attempts integer);
	CREATE UNIQUE INDEX IF NOT EXISTS atakdl_failed_tile_index ON atakdl_failed_tiles (zoom_level, tile_column, tile_row);
`

// insertTile is the statement that the writer goroutine runs for each Put. The row is the TMS row.
const insertTile = "INSERT OR REPLACE INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)"

// Writer writes the tiles of one job to an MBTiles output file, and it satisfies output.Writer.
// The queue of sqlitequeue owns the database, and it commits one transaction per 500 operations.
// Has, Put, Fail and Failed are safe for concurrent use. Close must not run at the same time as any of them.
type Writer struct {
	*sqlitequeue.Queue[sqlitequeue.Key]
	db *sql.DB

	// commits counts the committed tile transactions. Only the writer goroutine writes it, and a test reads it after Close.
	commits int
}

// Open returns a writer for the output file at path, and it creates the file when it does not exist.
// It stores the rows in metadata, and it replaces a stored row with the same name.
// It loads the keys of the stored tiles, so that Has needs no query.
// It returns an error when the directory, the schema or the metadata write fails.
func Open(path string, metadata map[string]string) (*Writer, error) {
	db, err := sqlitequeue.OpenDB(path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	if err := writeMetadata(db, metadata); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	keys, err := storedKeys(db)
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}

	w := &Writer{db: db}
	w.Queue = sqlitequeue.New(db, keys, sqlitequeue.Format[sqlitequeue.Key]{
		Key:       func(z, x, y int) sqlitequeue.Key { return sqlitequeue.Key{Z: z, X: x, Y: y} },
		FailedRow: flip,
		Insert:    w.insert,
	})
	return w, nil
}

func init() {
	output.Register(output.MBTiles, readInfo, func(path string, metadata map[string]string) (output.Writer, error) {
		w, err := Open(path, metadata)
		if err != nil {
			// A nil *Writer in a non-nil interface would look like a writer to the caller.
			return nil, err
		}
		return w, nil
	}, ReadTile)
}

// tileColumns are the columns of the tiles table that mark an MBTiles file.
// An OSMDroid file also has a tiles table, so the detector checks the columns and not the table name.
var tileColumns = []string{"tile_column", "tile_data", "tile_row", "zoom_level"}

// readInfo returns the Info of the MBTiles file at path, and false when the file has no tiles table with tileColumns.
// It never writes to the file. A metadata row that is missing or not valid gives the zero value of its field.
// It returns an error when the file is not an SQLite database or has no metadata table.
func readInfo(path string) (output.Info, bool, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return output.Info{}, false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	columns, err := tableColumns(db, "tiles")
	if err != nil {
		return output.Info{}, false, err
	}
	for _, c := range tileColumns {
		if !columns[c] {
			return output.Info{}, false, nil
		}
	}
	meta, err := readMetadata(db, "format", "atakdl_source", "minzoom", "maxzoom", "bounds", "atakdl_areas")
	if err != nil {
		return output.Info{}, false, err
	}
	info := output.Info{TileFormat: meta["format"], Source: meta["atakdl_source"], Bounds: parseBounds(meta["bounds"]), Areas: meta["atakdl_areas"]}
	// A v0.1 output file can lack a zoom row, so a missing or bad row gives 0.
	info.MinZoom, _ = strconv.Atoi(meta["minzoom"])
	info.MaxZoom, _ = strconv.Atoi(meta["maxzoom"])
	return info, true, nil
}

// tableColumns returns the set of the column names of table in db. A missing table gives an empty set.
func tableColumns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.
	columns := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("read the columns of %s: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	return columns, nil
}

// readMetadata returns the value of each metadata row in names that db holds.
func readMetadata(db *sql.DB, names ...string) (map[string]string, error) {
	meta := map[string]string{}
	for _, name := range names {
		var value string
		err := db.QueryRow("SELECT value FROM metadata WHERE name = ?", name).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read metadata %s: %w", name, err)
		}
		meta[name] = value
	}
	return meta, nil
}

// parseBounds returns the four numbers of an MBTiles bounds row "west,south,east,north".
// It returns nil when the row does not hold four numbers.
func parseBounds(row string) []float64 {
	parts := strings.Split(row, ",")
	if len(parts) != 4 {
		return nil
	}
	bounds := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil
		}
		bounds[i] = v
	}
	return bounds
}

// ReadTile returns the tile data of the tile z, x, y in the output file at path, and true when the file holds the tile.
// The row y is the XYZ row. ReadTile never writes to the file.
// It reads only committed rows, so it is safe while a job writes the file in WAL mode.
// Call it only for a file that exists, because the driver creates a missing file.
// It returns an error when the file is not an SQLite database or has no tiles table.
func ReadTile(path string, z, x, y int) ([]byte, bool, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	var data []byte
	err = db.QueryRow("SELECT tile_data FROM tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?", z, x, flip(z, y)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read tile %d/%d/%d: %w", z, x, y, err)
	}
	return data, true, nil
}

// writeMetadata stores the rows in metadata in one transaction.
func writeMetadata(db *sql.DB, metadata map[string]string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for k, v := range metadata {
		if _, err := tx.Exec("INSERT OR REPLACE INTO metadata (name, value) VALUES (?, ?)", k, v); err != nil {
			return errors.Join(fmt.Errorf("insert metadata %s: %w", k, err), tx.Rollback())
		}
	}
	return tx.Commit()
}

// storedKeys returns the keys of the tiles in db, with XYZ rows.
func storedKeys(db *sql.DB) (map[sqlitequeue.Key]struct{}, error) {
	rows, err := db.Query("SELECT zoom_level, tile_column, tile_row FROM tiles")
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
		k.Y = flip(k.Z, k.Y)
		keys[k] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the stored tile keys: %w", err)
	}
	return keys, nil
}

// flip converts between an XYZ row and a TMS row at zoom level z. The conversion is its own inverse.
// ATAK reads tile_row as a TMS row, so the writer stores TMS rows.
func flip(z, y int) int {
	return (1 << z) - 1 - y
}

// insert writes tiles in tx with TMS rows, and it replaces a stored tile with the same key.
// It returns the function that counts the commit.
func (w *Writer) insert(tx *sql.Tx, tiles []sqlitequeue.Tile) (func(), error) {
	put, err := tx.Prepare(insertTile)
	if err != nil {
		return nil, fmt.Errorf("prepare %q: %w", insertTile, err)
	}
	for _, t := range tiles {
		if _, err := put.Exec(t.Z, t.X, flip(t.Z, t.Y), t.Data); err != nil {
			return nil, errors.Join(fmt.Errorf("write tile %d/%d/%d: %w", t.Z, t.X, t.Y, err), sqlitequeue.CloseAll(put))
		}
	}
	return func() { w.commits++ }, sqlitequeue.CloseAll(put)
}
