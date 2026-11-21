// Package output holds the writer interface of the download engine and the registry of the output formats.
// Each output format registers itself from its own package, so a new format adds no line here (FR-output-formats-4).
package output

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// The output formats of FR-output-formats-1.
const (
	MBTiles    = "mbtiles"
	OSMDroid   = "osmdroid"
	GeoPackage = "gpkg"
)

// Formats lists the valid values of --format and output_format, with the default first.
var Formats = []string{MBTiles, OSMDroid, GeoPackage}

// extensions maps each output format to its file extension (FR-output-formats-2).
var extensions = map[string]string{
	MBTiles:    ".mbtiles",
	OSMDroid:   ".sqlite",
	GeoPackage: ".gpkg",
}

// ErrNotRegistered is the error of Open for an output format that has no registered writer.
var ErrNotRegistered = errors.New("output format is not registered")

// ErrUnknownFormat is the error of ReadInfo for a file that no registered detector accepts.
var ErrUnknownFormat = errors.New("not a file of a known output format")

// FailedTile is one failed tile that an output file records. Y is the XYZ row.
type FailedTile struct {
	Z, X, Y int
	// Reason is the failure reason of the last failure of the tile.
	Reason string
	// Attempts counts the calls of Fail for the tile, one per failed pass.
	Attempts int
}

// Writer writes the tiles of one job to an output file. Every row y is the XYZ row.
// Has, Put, Fail and Failed are safe for concurrent use. Close must not run at the same time as any of them.
type Writer interface {
	// Has reports whether the output file holds the tile z, x, y, or whether Put queued it.
	Has(z, x, y int) bool
	// Put stores the tile z, x, y with data, and it removes the failed tile record of that tile.
	Put(z, x, y int, data []byte) error
	// Fail records the tile z, x, y as failed with reason.
	Fail(z, x, y int, reason string) error
	// Failed returns the failed tiles of the output file, with each Put and Fail before the call applied.
	Failed() ([]FailedTile, error)
	// Close stores the queued tiles and closes the output file.
	Close() error
}

// Info describes an existing output file.
type Info struct {
	// Format is the output format that the detector found by schema.
	Format string
	// TileFormat is the image format of the tiles, for example png.
	TileFormat string
	// Source is the name of the map source of the file. It is empty for a file that names no map source.
	Source string
	// MinZoom and MaxZoom are 0 when the file does not record them.
	MinZoom, MaxZoom int
	// Bounds holds west, south, east and north. It is nil when the file records no valid bounds.
	Bounds []float64
	// Areas is the atakdl_areas row of the file, the GeoJSON of the job areas. It is empty when the file has no such row.
	Areas string
}

// Detector returns the Info of the file at path, and false when the file is not of its output format.
// A detector reads only the schema and the metadata, and it never writes to the file.
type Detector func(path string) (Info, bool, error)

// Opener returns a writer for the output file at path, and it creates the file when it does not exist.
// The writer stores the rows of metadata in the file.
type Opener func(path string, metadata map[string]string) (Writer, error)

// TileReader returns the data of the tile z, x, y in the output file at path, and true when the file holds the tile.
// The row y is the XYZ row. A reader never writes to the file, and it reads only committed rows.
type TileReader func(path string, z, x, y int) ([]byte, bool, error)

// format is one registered output format.
type format struct {
	detect Detector
	open   Opener
	read   TileReader
}

// registry holds the registered output formats. Register writes it only from init functions, so it needs no lock.
var registry = map[string]format{}

// Register adds the output format name with its detector, its opener and its tile reader.
// Call it from an init function of the package of the format.
// It panics when name is already registered, because two writers for one format are a build error.
func Register(name string, detect Detector, open Opener, read TileReader) {
	if _, ok := registry[name]; ok {
		panic("output: format " + name + " is registered twice")
	}
	registry[name] = format{detect: detect, open: open, read: read}
}

// Extension returns the file extension of the output format name, with the dot.
// It returns "" for a name that is not in Formats.
func Extension(name string) string {
	return extensions[name]
}

// Open returns a writer of the output format name for the output file at path.
// It returns ErrNotRegistered when name has no registered writer, and the error of the opener otherwise.
func Open(name, path string, metadata map[string]string) (Writer, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotRegistered, name)
	}
	return f.open(path, metadata)
}

// ReadInfo returns the Info of the output file at path, with the output format that its schema shows.
// It tries the detectors in name order, so the result does not change between runs.
// Call it only for a file that exists, because the SQLite driver creates a missing file.
// It returns ErrUnknownFormat, joined with the detector errors, when no detector accepts the file.
func ReadInfo(path string) (Info, error) {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(registry)) {
		info, ok, err := registry[name].detect(path)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if ok {
			info.Format = name
			return info, nil
		}
	}
	return Info{}, errors.Join(append([]error{ErrUnknownFormat}, errs...)...)
}

// ReadTile returns the data of the tile z, x, y in the output file at path, and true when the file holds the tile.
// The row y is the XYZ row. ReadTile reads the tile through the reader of the output format that ReadInfo finds.
// Call it only for a file that exists, because the SQLite driver creates a missing file.
// It returns the error of ReadInfo when no detector accepts the file, and the error of the reader otherwise.
func ReadTile(path string, z, x, y int) ([]byte, bool, error) {
	info, err := ReadInfo(path)
	if err != nil {
		return nil, false, err
	}
	return registry[info.Format].read(path, z, x, y)
}
