// Package sqlitequeue holds the writer core that the SQLite output formats share.
// One writer goroutine owns the database, and it commits the queued operations in batches (FR-robust-downloads-2).
// Each output format gives only its schema, its tile insert and the key that Has looks up.
package sqlitequeue

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/crank-git/atak-map-downloader/internal/output"

	// The blank import registers the "sqlite" driver with database/sql.
	_ "modernc.org/sqlite"
)

// BatchSize is the number of operations that the writer goroutine commits in one transaction (FR-robust-downloads-2).
const BatchSize = 500

// queueSize is the number of operations that Put and Fail can queue before they wait for the writer goroutine.
const queueSize = 64

// dsnPragmas sets the pragmas of FR-robust-downloads-3 on every connection.
// modernc.org/sqlite runs each _pragma value as "PRAGMA <value>", and it runs busy_timeout first.
const dsnPragmas = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"

// The statements of the atakdl_failed_tiles table, which every output format has.
const (
	removeFailed = "DELETE FROM atakdl_failed_tiles WHERE zoom_level = ? AND tile_column = ? AND tile_row = ?"
	recordFailed = `INSERT INTO atakdl_failed_tiles (zoom_level, tile_column, tile_row, reason, attempts) VALUES (?, ?, ?, ?, 1)
		ON CONFLICT (zoom_level, tile_column, tile_row) DO UPDATE SET reason = excluded.reason, attempts = attempts + 1`
	selectFailed = "SELECT zoom_level, tile_column, tile_row, reason, attempts FROM atakdl_failed_tiles ORDER BY zoom_level, tile_column, tile_row"
)

// OpenDB opens the SQLite file at path for a writer, and it creates the directory of path first.
// The connection pool holds one connection, because only the writer goroutine writes and a second connection would only wait on the file lock.
func OpenDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { //nolint:gosec // The mode 0755 is the existing behavior.
		return nil, err
	}
	db, err := sql.Open("sqlite", path+dsnPragmas)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// Key identifies one tile by zoom level, column and XYZ row.
type Key struct {
	Z, X, Y int
}

// Tile is one tile of a Put. Y is the XYZ row.
type Tile struct {
	Key
	Data []byte
}

// Format is the part of a writer that differs between the output formats.
type Format[K comparable] struct {
	// Key returns the key of the tile z, x, y that Has looks up. The row y is the XYZ row.
	Key func(z, x, y int) K
	// FailedRow returns the row that atakdl_failed_tiles stores for the XYZ row y at zoom level z, and the reverse.
	// The function is its own inverse.
	FailedRow func(z, y int) int
	// Insert writes tiles in tx, in queue order, and it replaces a stored tile with the same key.
	// The queue calls it for each batch, also for a batch with no tiles.
	// It returns a function that the queue calls after the commit of tx, or nil.
	Insert func(tx *sql.Tx, tiles []Tile) (committed func(), err error)
}

// op is one operation that waits in the queue of the writer goroutine.
type op struct {
	Tile
	// reason is the failure reason of a Fail. A Put has no reason.
	reason string
	// reply is set only for a Failed request, and the writer goroutine sends the failed tiles to it.
	reply chan<- failedList
}

// failedList is the answer of the writer goroutine to a Failed request.
type failedList struct {
	tiles []output.FailedTile
	err   error
}

// Queue writes the tiles of one job to an SQLite output file, and it satisfies output.Writer.
// Has, Put, Fail and Failed are safe for concurrent use. Close must not run at the same time as any of them.
type Queue[K comparable] struct {
	db     *sql.DB
	format Format[K]
	queue  chan op
	done   chan struct{}

	mu   sync.RWMutex
	keys map[K]struct{}
	// err is the first write error. After it, the writer goroutine drops every queued operation.
	err error
}

// New returns a queue that writes to db with format, and it starts the writer goroutine.
// keys holds the keys of the tiles that db already holds. The queue owns db, and Close closes it.
func New[K comparable](db *sql.DB, keys map[K]struct{}, format Format[K]) *Queue[K] {
	q := &Queue[K]{
		db:     db,
		format: format,
		queue:  make(chan op, queueSize),
		done:   make(chan struct{}),
		keys:   keys,
	}
	go q.run()
	return q
}

// Has reports whether the output file holds the tile z, x, y, or whether Put queued it.
// The row y is the XYZ row.
func (q *Queue[K]) Has(z, x, y int) bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	_, ok := q.keys[q.format.Key(z, x, y)]
	return ok
}

// Put queues the tile z, x, y with data for the writer goroutine. The row y is the XYZ row.
// The writer removes the row of the tile from atakdl_failed_tiles in the same transaction (FR-robust-downloads-23).
// It returns the first write error of the writer, and the writer drops every tile after that error.
func (q *Queue[K]) Put(z, x, y int, data []byte) error {
	q.queue <- op{Tile: Tile{Key: Key{z, x, y}, Data: data}}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.keys[q.format.Key(z, x, y)] = struct{}{}
	return q.err
}

// Fail queues a row in atakdl_failed_tiles for the tile z, x, y with reason. The row y is the XYZ row.
// A tile that has a row keeps one row: the writer replaces the reason and adds 1 to attempts.
// It returns the first write error of the writer.
func (q *Queue[K]) Fail(z, x, y int, reason string) error {
	q.queue <- op{Tile: Tile{Key: Key{z, x, y}}, reason: reason}
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.err
}

// Failed returns the rows of atakdl_failed_tiles, with XYZ rows, in tile order.
// The writer goroutine commits every queued operation first, so the rows include each Fail and Put before the call.
// It returns the first write error of the writer, or the query error.
func (q *Queue[K]) Failed() ([]output.FailedTile, error) {
	reply := make(chan failedList, 1)
	q.queue <- op{reply: reply}
	l := <-reply
	return l.tiles, l.err
}

// Close commits the queued tiles, stops the writer goroutine and closes the output file.
// It moves the WAL content into the file and sets journal_mode=DELETE, so no -wal or -shm file stays.
// ATAK opens the file read-only, a data package holds only the one file, and a read-only reader cannot open a file in WAL mode.
// It returns the first write error, joined with the journal error and the close error.
func (q *Queue[K]) Close() error {
	close(q.queue)
	<-q.done
	q.mu.RLock()
	errs := []error{q.err}
	q.mu.RUnlock()

	// SQLite returns the old mode when another connection blocks the switch, so Close reads the result.
	var mode string
	if _, err := q.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		errs = append(errs, fmt.Errorf("checkpoint the WAL file: %w", err))
	} else if err := q.db.QueryRow("PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		errs = append(errs, fmt.Errorf("set journal_mode=DELETE: %w", err))
	} else if mode != "delete" {
		errs = append(errs, fmt.Errorf("set journal_mode=DELETE: the mode stays %s", mode))
	}
	return errors.Join(append(errs, q.db.Close())...)
}

// run writes the queued operations until Close closes the queue.
func (q *Queue[K]) run() {
	defer close(q.done)
	batch := make([]op, 0, BatchSize)
	for o := range q.queue {
		if o.reply != nil {
			if len(batch) > 0 && !q.failed() {
				q.commit(batch)
			}
			batch = batch[:0]
			o.reply <- q.listFailed()
			continue
		}
		if q.failed() {
			continue
		}
		batch = append(batch, o)
		if len(batch) == BatchSize {
			q.commit(batch)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 && !q.failed() {
		q.commit(batch)
	}
}

// failed reports whether the writer has a write error.
func (q *Queue[K]) failed() bool {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.err != nil
}

// commit writes batch in one transaction. It records the first error in q.err.
func (q *Queue[K]) commit(batch []op) {
	err := q.insert(batch)
	if err == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err == nil {
		q.err = err
	}
}

// insert writes the operations of batch in one transaction.
// The tile rows and the atakdl_failed_tiles rows are in separate tables, so the format writes its tiles before the failed rows.
// Each table keeps the queue order, so the result is the same as for one pass in queue order.
func (q *Queue[K]) insert(batch []op) error {
	tx, err := q.db.Begin()
	if err != nil {
		return fmt.Errorf("begin the tile transaction: %w", err)
	}
	tiles := make([]Tile, 0, len(batch))
	for _, o := range batch {
		if o.reason == "" {
			tiles = append(tiles, o.Tile)
		}
	}
	committed, err := q.format.Insert(tx, tiles)
	if err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := q.writeFailed(tx, batch); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %d tiles: %w", len(batch), err)
	}
	if committed != nil {
		committed()
	}
	return nil
}

// writeFailed removes the atakdl_failed_tiles row of each Put in batch, and it records the row of each Fail.
func (q *Queue[K]) writeFailed(tx *sql.Tx, batch []op) error {
	remove, err := tx.Prepare(removeFailed)
	if err != nil {
		return fmt.Errorf("prepare %q: %w", removeFailed, err)
	}
	record, err := tx.Prepare(recordFailed)
	if err != nil {
		return errors.Join(fmt.Errorf("prepare %q: %w", recordFailed, err), CloseAll(remove))
	}
	for _, o := range batch {
		row := q.format.FailedRow(o.Z, o.Y)
		if o.reason == "" {
			_, err = remove.Exec(o.Z, o.X, row)
		} else {
			_, err = record.Exec(o.Z, o.X, row, o.reason)
		}
		if err != nil {
			return errors.Join(fmt.Errorf("write tile %d/%d/%d: %w", o.Z, o.X, o.Y, err), CloseAll(remove, record))
		}
	}
	return CloseAll(remove, record)
}

// CloseAll closes each statement in stmts and returns the joined close errors.
func CloseAll(stmts ...*sql.Stmt) error {
	var errs []error
	for _, s := range stmts {
		if err := s.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close a tile statement: %w", err))
		}
	}
	return errors.Join(errs...)
}

// listFailed returns the rows of atakdl_failed_tiles with XYZ rows. Only the writer goroutine calls it.
func (q *Queue[K]) listFailed() failedList {
	q.mu.RLock()
	err := q.err
	q.mu.RUnlock()
	if err != nil {
		return failedList{err: err}
	}
	rows, err := q.db.Query(selectFailed)
	if err != nil {
		return failedList{err: fmt.Errorf("read the failed tiles: %w", err)}
	}
	defer func() { _ = rows.Close() }() // rows.Err below reports a read failure.

	var tiles []output.FailedTile
	for rows.Next() {
		var f output.FailedTile
		if err := rows.Scan(&f.Z, &f.X, &f.Y, &f.Reason, &f.Attempts); err != nil {
			return failedList{err: fmt.Errorf("read the failed tiles: %w", err)}
		}
		f.Y = q.format.FailedRow(f.Z, f.Y)
		tiles = append(tiles, f)
	}
	if err := rows.Err(); err != nil {
		return failedList{err: fmt.Errorf("read the failed tiles: %w", err)}
	}
	return failedList{tiles: tiles}
}
