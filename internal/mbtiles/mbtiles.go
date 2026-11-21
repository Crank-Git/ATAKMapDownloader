// Package mbtiles provides MBTiles database creation for ATAK compatibility.
package mbtiles

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Create creates an MBTiles database with the given metadata.
func Create(outputPath string, metadata map[string]string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return nil, err
	}
	if _, err := os.Stat(outputPath); err == nil {
		if err := os.Remove(outputPath); err != nil {
			return nil, err
		}
	}

	// The engine inserts tiles from many goroutines over the connection pool.
	// mattn/go-sqlite3 waited 5000 ms on a locked file by default, and modernc.org/sqlite does not.
	db, err := sql.Open("sqlite", outputPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	schema := `
		CREATE TABLE metadata (name text, value text);
		CREATE TABLE tiles (zoom_level integer, tile_column integer, tile_row integer, tile_data blob);
		CREATE UNIQUE INDEX name ON metadata (name);
		CREATE UNIQUE INDEX tile_index ON tiles (zoom_level, tile_column, tile_row);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	for k, v := range metadata {
		if _, err := db.Exec("INSERT INTO metadata (name, value) VALUES (?, ?)", k, v); err != nil {
			db.Close()
			return nil, fmt.Errorf("insert metadata %s: %w", k, err)
		}
	}

	return db, nil
}

// InsertTile inserts a tile into the database (TMS row format).
func InsertTile(db *sql.DB, z, x, tmsY int, data []byte) error {
	_, err := db.Exec(
		"INSERT OR REPLACE INTO tiles (zoom_level, tile_column, tile_row, tile_data) VALUES (?, ?, ?, ?)",
		z, x, tmsY, data,
	)
	return err
}
