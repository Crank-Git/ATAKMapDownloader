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
const schema = `
	CREATE TABLE IF NOT EXISTS metadata (name text, value text);
	CREATE TABLE IF NOT EXISTS tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob);
	CREATE UNIQUE INDEX IF NOT EXISTS name ON metadata (name);
	CREATE UNIQUE INDEX IF NOT EXISTS tile_index ON tiles (zoom_level, tile_column, tile_row);
`

// key identifies one tile by zoom level, column and XYZ row.
type key struct {
	z, x, y int
}

// tile is one tile that waits in the queue of the writer goroutine.
type tile struct {
	key
	data []byte
}

// Writer writes the tiles of one job to an output file.
// One writer goroutine owns the database, and it commits one transaction per 500 tiles.
// Has and Put are safe for concurrent use. Close must not run at the same time as Put.
type Writer struct {
	db    *sql.DB
	queue chan tile
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
		queue: make(chan tile, queueSize),
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
// It returns the first write error of the writer, and the writer drops every tile after that error.
func (w *Writer) Put(z, x, y int, data []byte) error {
	k := key{z, x, y}
	w.queue <- tile{key: k, data: data}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.keys[k] = struct{}{}
	return w.err
}

// Close commits the queued tiles, stops the writer goroutine and closes the output file.
// It returns the first write error, joined with the close error.
func (w *Writer) Close() error {
	close(w.queue)
	<-w.done
	w.mu.RLock()
	err := w.err
	w.mu.RUnlock()
	return errors.Join(err, w.db.Close())
}

// run writes the queued tiles until Close closes the queue.
func (w *Writer) run() {
	defer close(w.done)
	batch := make([]tile, 0, batchSize)
	for t := range w.queue {
		if w.failed() {
			continue
		}
		batch = append(batch, t)
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
func (w *Writer) commit(batch []tile) {
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

// insert writes the tiles of batch in one transaction, and it replaces a stored tile with the same key.
func (w *Writer) insert(batch []tile) error {
	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("begin the tile transaction: %w", err)
	}
	stmt, err := tx.Prepare("INSERT OR REPLACE INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)")
	if err != nil {
		return errors.Join(fmt.Errorf("prepare the tile insert: %w", err), tx.Rollback())
	}
	for _, t := range batch {
		if _, err := stmt.Exec(t.z, t.x, flip(t.z, t.y), t.data); err != nil {
			return errors.Join(fmt.Errorf("insert tile %d/%d/%d: %w", t.z, t.x, t.y, err), stmt.Close(), tx.Rollback())
		}
	}
	if err := stmt.Close(); err != nil {
		return errors.Join(fmt.Errorf("close the tile insert: %w", err), tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %d tiles: %w", len(batch), err)
	}
	return nil
}
