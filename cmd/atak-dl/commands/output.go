package commands

import (
	"errors"
	"fmt"
	"os"

	"github.com/crank-git/atak-map-downloader/internal/mbtiles"
)

// exitOutputConflict is the exit code for an output file that the job cannot use (feature 07 exit codes).
const exitOutputConflict = 3

// ifExists is the rule for an output file that exists when a job starts (FR-robust-downloads-13).
type ifExists string

const (
	ifExistsFail      ifExists = "fail"
	ifExistsResume    ifExists = "resume"
	ifExistsOverwrite ifExists = "overwrite"
)

// sourceKey is the metadata row that names the map source of an output file.
const sourceKey = "atakdl_source"

// areasKey is the metadata row that holds the areas of an output file as GeoJSON (FR-area-selection-29).
const areasKey = "atakdl_areas"

// outputConflict is an error for an output file that the rule of the job does not accept.
// The CLI exits with exitOutputConflict for it, and the job API returns 409 output_exists.
type outputConflict struct {
	message string
}

func (e *outputConflict) Error() string { return e.message }

// errOutputExists is the conflict of an existing output file under ifExistsFail (FR-robust-downloads-6).
var errOutputExists = &outputConflict{"output file exists: use --resume or --overwrite"}

// errNoMapSource is the conflict of a resume into a v0.1 output file, which has no sourceKey row.
var errNoMapSource = &outputConflict{"output file names no map source: use --resume --force"}

// prepareOutput applies rule to the output file at path, before a job of the map source sourceName starts.
// It returns nil when no file exists at path. With ifExistsOverwrite, it deletes the file (FR-robust-downloads-7).
// With force, a resume accepts a file that has no sourceKey row.
// It returns an *outputConflict in each of these cases:
//   - errOutputExists when rule is ifExistsFail.
//   - A conflict that names the stored map source, when a resume finds another map source (FR-robust-downloads-10).
//   - errNoMapSource when a resume finds no sourceKey row and force is false.
//   - A "not an output file" conflict when a resume cannot read the metadata of the file.
//
// It returns other errors when the file cannot be read or deleted.
func prepareOutput(path string, rule ifExists, sourceName string, force bool) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check the output file: %w", err)
	}
	switch rule {
	case ifExistsOverwrite:
		// A WAL file of an earlier job does not belong to the new file, so it goes with the file.
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("delete the output file: %w", err)
			}
		}
		return nil
	case ifExistsResume:
		stored, ok, err := mbtiles.ReadMetadata(path, sourceKey)
		if err != nil {
			// The file is not SQLite or has no metadata table, so the job cannot add tiles to it.
			return &outputConflict{"not an output file: " + err.Error()}
		}
		if !ok && !force {
			return errNoMapSource
		}
		if ok && stored != sourceName {
			return &outputConflict{"output file belongs to map source " + stored}
		}
		return nil
	default:
		return errOutputExists
	}
}
