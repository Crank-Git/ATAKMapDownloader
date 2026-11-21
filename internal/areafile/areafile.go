// Package areafile reads the areas of a job from a GeoJSON, KML or KMZ file (feature 11).
//
// GeoJSON follows RFC 7946 (https://datatracker.ietf.org/doc/html/rfc7946).
// KML follows OGC KML 2.2 (https://www.ogc.org/standards/kml), for a Placemark with a Polygon, a LineString or a MultiGeometry.
package areafile

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
)

// MaxBytes is the largest area file that Parse and Read accept, 5 MiB (feature 11, Behaviour rules).
const MaxBytes = 5 << 20

// ErrTooLarge is the error for a file, or a KML in a KMZ, that is larger than MaxBytes.
var ErrTooLarge = errors.New("the area file is larger than 5 MiB")

// errNoArea is the error for a file that holds no polygon and no line.
var errNoArea = errors.New("the file has no polygon and no line")

// Format is the type of an area file.
type Format int

// The area file types of FR-area-selection-12.
const (
	GeoJSON Format = iota + 1
	KML
	KMZ
)

// FormatOf returns the format of the file name from its extension (FR-area-selection-12).
// The extensions are .geojson, .json, .kml and .kmz, in any case.
// It returns an error for another extension.
func FormatOf(name string) (Format, error) {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".geojson", ".json":
		return GeoJSON, nil
	case ".kml":
		return KML, nil
	case ".kmz":
		return KMZ, nil
	default:
		return 0, fmt.Errorf("area file extension %q is not .geojson, .json, .kml or .kmz", ext)
	}
}

// Sniff returns the format of a file that has no name, from its first bytes.
// A zip archive is KMZ, a JSON object is GeoJSON, and XML is KML.
// It returns an error when the bytes start none of these.
func Sniff(data []byte) (Format, error) {
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return KMZ, nil
	}
	text := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \t\r\n")
	switch {
	case bytes.HasPrefix(text, []byte("{")):
		return GeoJSON, nil
	case bytes.HasPrefix(text, []byte("<")):
		return KML, nil
	default:
		return 0, errors.New("the file is not GeoJSON, KML or KMZ")
	}
}

// Defaults holds the values of an area that the file does not set.
// MinZoom and MaxZoom apply to a feature with no zoom property (FR-area-selection-14).
// BufferM applies to a line with no buffer_m property (FR-area-selection-15).
type Defaults struct {
	MinZoom int
	MaxZoom int
	BufferM float64
}

// Result holds the areas of a file in file order, and the count of the Point features that it skipped.
type Result struct {
	Areas  []tilecalc.JobArea
	Points int
}

// Warnings returns the warnings for the user about the file (FR-area-selection-18).
// It returns an empty list when the file has no Point feature.
func (r Result) Warnings() []string {
	switch r.Points {
	case 0:
		return []string{}
	case 1:
		return []string{"ignored 1 Point feature, because a point is not an area"}
	default:
		return []string{fmt.Sprintf("ignored %d Point features, because a point is not an area", r.Points)}
	}
}

// Read returns the areas of the file at path, with the format of its extension.
// It returns ErrTooLarge for a file larger than MaxBytes, and the errors of FormatOf and Parse.
func Read(path string, d Defaults) (Result, error) {
	format, err := FormatOf(path)
	if err != nil {
		return Result{}, err
	}
	f, err := os.Open(path) //nolint:gosec // The path is the --area flag of the user, and the CLI reads it for that user.
	if err != nil {
		return Result{}, fmt.Errorf("open the area file: %w", err)
	}
	defer func() { _ = f.Close() }() // The file is read only, so a close error loses no data.
	data, err := readLimited(f)
	if err != nil {
		return Result{}, err
	}
	return Parse(format, data, d)
}

// readLimited returns the bytes of r, or ErrTooLarge when r holds more than MaxBytes.
func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the area file: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

// Parse returns the areas of data in the format f.
// A polygon becomes a polygon area, and a line becomes a route_buffer area.
// Parse skips Point features and counts them.
// It returns these errors:
//   - ErrTooLarge when data, or the KML in a KMZ, is larger than MaxBytes.
//   - An error that names the problem when the file does not decode.
//   - An error when the file holds no polygon and no line.
//
// Parse does not validate the areas, so call Validate on each area.
func Parse(f Format, data []byte, d Defaults) (Result, error) {
	if len(data) > MaxBytes {
		return Result{}, ErrTooLarge
	}
	var (
		res Result
		err error
	)
	switch f {
	case GeoJSON:
		res, err = parseGeoJSON(data, d)
	case KML:
		res, err = parseKML(data, d)
	case KMZ:
		res, err = parseKMZ(data, d)
	default:
		return Result{}, fmt.Errorf("unknown area file format %d", f)
	}
	if err != nil {
		return Result{}, err
	}
	if len(res.Areas) == 0 {
		return Result{}, errNoArea
	}
	return res, nil
}
