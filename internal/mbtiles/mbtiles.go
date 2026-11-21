// Package mbtiles provides MBTiles database creation for ATAK compatibility.
package mbtiles

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	// The blank import registers the "sqlite" driver with database/sql.
	_ "modernc.org/sqlite"
)

// batchSize is the number of tiles that the writer commits in one transaction (FR-robust-downloads-2).
const batchSize = 500

// queueSize is the number of tiles that Put can queue before it waits for the writer goroutine.
const queueSize = 64

// dsnPragmas sets the pragmas of FR-robust-downloads-3 on every connection.
// modernc.org/sqlite runs each _pragma value as "PRAGMA <value>", and it runs busy_timeout first.
const dsnPragmas = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"

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

// The statements that the writer goroutine runs for each queued operation. The row is the TMS row.
const (
	insertTile   = "INSERT OR REPLACE INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)"
	removeFailed = "DELETE FROM atakdl_failed_tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?"
	recordFailed = `INSERT INTO atakdl_failed_tiles (zoom_level, tile_column, tile_row, reason, attempts) VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (zoom_level, tile_column, tile_row) DO UPDATE SET reason = excluded.reason, attempts = attempts + 1`
)

// key identifies one tile by zoom level, column and XYZ row.
type key struct {
	z, x, y int
}

// op is one operation that waits in the queue of the writer goroutine.
type op struct {
	key
	// data is the tile data of a Put.
	data []byte
	// reason is the failure reason of a Fail. A Put has no reason.
	reason string
	// reply is set only for a Failed request, and the writer goroutine sends the failed tiles to it.
	reply chan<- failedList
}

// failedList is the answer of the writer goroutine to a Failed request.
type failedList struct {
	tiles []FailedTile
	err   error
}

// FailedTile is one row of the atakdl_failed_tiles table. Y is the XYZ row.
type FailedTile struct {
	Z, X, Y int
	// Reason is the failure reason of the last failure of the tile.
	Reason string
	// Attempts counts the calls of Fail for the tile, one per failed pass.
	Attempts int
}

// Writer writes the tiles of one job to an output file.
// One writer goroutine owns the database, and it commits one transaction per 500 tiles.
// Has, Put, Fail and Failed are safe for concurrent use. Close must not run at the same time as any of them.
type Writer struct {
	db    *sql.DB
	queue chan op
	done  chan struct{}

	mu   sync.RWMutex
	keys map[key]struct{}
	// err is the first write error. After it, the writer goroutine drops every queued tile.
	err error

	// commits counts the committed tile transactions. A test reads it after Close.
	commits int
}

// Open returns a writer for the output file at path, and it creates the file when it does not exist.
// It stores the rows in metadata, and it replaces a stored row with the same name.
// It loads the keys of the stored tiles, so that Has needs no query.
// It returns an error when the directory, the schema or the metadata write fails.
func Open(path string, metadata map[string]string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { //nolint:gosec // The mode 0755 is the existing behavior.
		return nil, err
	}
	db, err := sql.Open("sqlite", path+dsnPragmas)
	if err != nil {
		return nil, err
	}
	// Only the writer goroutine writes, so a second connection would only wait on the file lock.
	db.SetMaxOpenConns(1)

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

	w := &Writer{
		db:    db,
		queue: make(chan op, queueSize),
		done:  make(chan struct{}),
		keys:  keys,
	}
	go w.run()
	return w, nil
}

// ReadMetadata returns the value of the metadata row name in the output file at path, and true when the row exists.
// It never writes to the file. Call it only for a file that exists, because the driver creates a missing file.
// It returns an error when the file is not an SQLite database or has no metadata table.
func ReadMetadata(path, name string) (string, bool, error) {
	db, err := sql.Open("sqlite", path+"?_busy_timeout=5000&_query_only=1")
	if err != nil {
		return "", false, err
	}
	defer func() { _ = db.Close() }() // The file is read-only here, so a close error loses no data.

	var value string
	err = db.QueryRow("SELECT value FROM metadata WHERE name = ?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read metadata %s: %w", name, err)
	}
	return value, true, nil
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
func storedKeys(db *sql.DB) (map[key]struct{}, error) {
	rows, err := db.Query("SELECT zoom_level, tile_column, tile_row FROM tiles")
	if err != nil {
		return nil, fmt.Errorf("read the stored tile keys: %w", err)
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	keys := make(map[key]struct{})
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.z, &k.x, &k.y); err != nil {
			return nil, fmt.Errorf("read the stored tile keys: %w", err)
		}
		k.y = flip(k.z, k.y)
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

// Has reports whether the output file holds the tile z, x, y, or whether Put queued it.
// The row y is the XYZ row.
func (w *Writer) Has(z, x, y int) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	_, ok := w.keys[key{z, x, y}]
	return ok
}

// Put queues the tile z, x, y with data for the writer goroutine. The row y is the XYZ row.
// The writer removes the row of the tile from atakdl_failed_tiles in the same transaction (FR-robust-downloads-23).
// It returns the first write error of the writer, and the writer drops every tile after that error.
func (w *Writer) Put(z, x, y int, data []byte) error {
	k := key{z, x, y}
	w.queue <- op{key: k, data: data}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keys[k] = struct{}{}
	return w.err
}

// Fail queues a row in atakdl_failed_tiles for the tile z, x, y with reason. The row y is the XYZ row.
// A tile that has a row keeps one row: the writer replaces the reason and adds 1 to attempts.
// It returns the first write error of the writer.
func (w *Writer) Fail(z, x, y int, reason string) error {
	w.queue <- op{key: key{z, x, y}, reason: reason}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.err
}

// Failed returns the rows of atakdl_failed_tiles, with XYZ rows, in tile order.
// The writer goroutine commits every queued operation first, so the rows include each Fail and Put before the call.
// It returns the first write error of the writer, or the query error.
func (w *Writer) Failed() ([]FailedTile, error) {
	reply := make(chan failedList, 1)
	w.queue <- op{reply: reply}
	l := <-reply
	return l.tiles, l.err
}

// Close commits the queued tiles, stops the writer goroutine and closes the output file.
// It moves the WAL content into the file and sets journal_mode=DELETE, so no -wal or -shm file stays.
// ATAK opens the file read-only, and a read-only reader cannot open a file in WAL mode.
// It returns the first write error, joined with the journal error and the close error.
func (w *Writer) Close() error {
	close(w.queue)
	<-w.done
	w.mu.RLock()
	errs := []error{w.err}
	w.mu.RUnlock()

	// SQLite returns the old mode when another connection blocks the switch, so Close reads the result.
	var mode string
	if _, err := w.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		errs = append(errs, fmt.Errorf("checkpoint the WAL file: %w", err))
	} else if err := w.db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		errs = append(errs, fmt.Errorf("set journal_mode=DELETE: %w", err))
	} else if mode != "delete" {
		errs = append(errs, fmt.Errorf("set journal_mode=DELETE: the mode stays %s", mode))
	}
	return errors.Join(append(errs, w.db.Close())...)
}

// run writes the queued operations until Close closes the queue.
func (w *Writer) run() {
	defer close(w.done)
	batch := make([]op, 0, batchSize)
	for o := range w.queue {
		if o.reply != nil {
			if len(batch) > 0 && !w.failed() {
				w.commit(batch)
			}
			batch = batch[:0]
			o.reply <- w.listFailed()
			continue
		}
		if w.failed() {
			continue
		}
		batch = append(batch, o)
		if len(batch) == batchSize {
			w.commit(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 && !w.failed() {
		w.commit(batch)
	}
}

// failed reports whether the writer has a write error.
func (w *Writer) failed() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.err != nil
}

// commit writes batch in one transaction. It records the first error in w.err.
func (w *Writer) commit(batch []op) {
	err := w.insert(batch)
	if err == nil {
		w.commits++
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err == nil {
		w.err = err
	}
}

// insert writes the operations of batch in one transaction, and it replaces a stored tile with the same key.
func (w *Writer) insert(batch []op) error {
	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("begin the tile transaction: %w", err)
	}
	var stmts []*sql.Stmt
	for _, query := range []string{insertTile, removeFailed, recordFailed} {
		stmt, err := tx.Prepare(query)
		if err != nil {
			return errors.Join(fmt.Errorf("prepare %q: %w", query, err), closeAll(stmts), tx.Rollback())
		}
		stmts = append(stmts, stmt)
	}
	put, remove, record := stmts[0], stmts[1], stmts[2]
	for _, o := range batch {
		row := flip(o.z, o.y)
		var err error
		if o.reason == "" {
			if _, err = put.Exec(o.z, o.x, row, o.data); err == nil {
				_, err = remove.Exec(o.z, o.x, row)
			}
		} else {
			_, err = record.Exec(o.z, o.x, row, o.reason)
		}
		if err != nil {
			return errors.Join(fmt.Errorf("write tile %d/%d/%d: %w", o.z, o.x, o.y, err), closeAll(stmts), tx.Rollback())
		}
	}
	if err := closeAll(stmts); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %d tiles: %w", len(batch), err)
	}
	return nil
}

// closeAll closes each statement in stmts and returns the joined close errors.
func closeAll(stmts []*sql.Stmt) error {
	var errs []error
	for _, s := range stmts {
		if err := s.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close a tile statement: %w", err))
		}
	}
	return errors.Join(errs...)
}

// listFailed returns the rows of atakdl_failed_tiles with XYZ rows. Only the writer goroutine calls it.
func (w *Writer) listFailed() failedList {
	w.mu.RLock()
	err := w.err
	w.mu.RUnlock()
	if err != nil {
		return failedList{err: err}
	}
	rows, err := w.db.Query("SELECT zoom_level, tile_column, tile_row, reason, attempts FROM atakdl_failed_tiles ORDER BY zoom_level, tile_column, tile_row")
	if err != nil {
		return failedList{err: fmt.Errorf("read the failed tiles: %w", err)}
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	var tiles []FailedTile
	for rows.Next() {
		var f FailedTile
		if err := rows.Scan(&f.Z, &f.X, &f.Y, &f.Reason, &f.Attempts); err != nil {
			return failedList{err: fmt.Errorf("read the failed tiles: %w", err)}
		}
		f.Y = flip(f.Z, f.Y)
		tiles = append(tiles, f)
	}
	if err := rows.Err(); err != nil {
		return failedList{err: fmt.Errorf("read the failed tiles: %w", err)}
	}
	return failedList{tiles: tiles}
}
