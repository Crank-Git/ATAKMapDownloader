package commands

import (
	"errors"
	"fmt"
	"os"

	"github.com/crank-git/atak-map-downloader/internal/output"
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

// outputConflict is an error for an output file that the rule of the job does not accept.
// The CLI exits with exitOutputConflict for it, and the job API returns 409 output_exists.
type outputConflict struct {
	message string
}

func (e *outputConflict) Error() string { return e.message }

// errOutputExists is the conflict of an existing output file under ifExistsFail (FR-robust-downloads-6).
var errOutputExists = &outputConflict{"output file exists: use --resume or --overwrite"}

// errNoMapSource is the conflict of a resume into a v0.1 output file, which names no map source.
var errNoMapSource = &outputConflict{"output file names no map source: use --resume --force"}

// prepareOutput applies rule to the output file at path, before a job of the map source sourceName starts.
// The job writes the output format format. It returns nil when no file exists at path.
// With ifExistsOverwrite, it deletes the file (FR-robust-downloads-7).
// With force, a resume accepts a file that names no map source.
// It returns an *outputConflict in each of these cases:
//   - errOutputExists when rule is ifExistsFail.
//   - A conflict that names the stored output format, when a resume finds another output format (FR-output-formats-5).
//   - A conflict that names the stored map source, when a resume finds another map source (FR-robust-downloads-10).
//   - errNoMapSource when a resume finds no map source name and force is false.
//   - A "not an output file" conflict when no output format accepts the file.
//
// It returns other errors when the file cannot be read or deleted.
func prepareOutput(path string, rule ifExists, sourceName, format string, force bool) error {
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
		info, err := output.ReadInfo(path)
		if err != nil {
			// No output format accepts the file, so the job cannot add tiles to it.
			return &outputConflict{"not an output file: " + err.Error()}
		}
		if info.Format != format {
			return &outputConflict{"output file is " + info.Format}
		}
		if info.Source == "" && !force {
			return errNoMapSource
		}
		if info.Source != "" && info.Source != sourceName {
			return &outputConflict{"output file belongs to map source " + info.Source}
		}
		return nil
	default:
		return errOutputExists
	}
}
