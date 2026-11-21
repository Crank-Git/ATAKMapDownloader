package commands

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/datapackage"
	"github.com/crank-git/atak-map-downloader/internal/snapshot"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// packageOptions are the data package settings of a job (FR-output-formats-19, -23).
// A nil *packageOptions means that the job writes no data package.
type packageOptions struct {
	// name is the package name. It is valid by datapackage.ValidName.
	name string
	// includeSource adds the source file of the map source to the data package.
	includeSource bool
}

// zipPath returns the path of the data package beside the output file at outputPath.
func (p *packageOptions) zipPath(outputPath string) string {
	return filepath.Join(filepath.Dir(outputPath), datapackage.FileName(p.name))
}

// packageNameMessage returns the invalid_package_name message for the package name name, which field holds.
func packageNameMessage(field, name string) string {
	return fmt.Sprintf("%s %q is not 1 to 64 characters of A-Z, a-z, 0-9, space, _ and -", field, name)
}

// packageFlags returns the data package settings of the --data-package, --include-source and --package-name flags of cmd.
// It trims the spaces around the package name before the check, so a name of spaces only is not valid.
// It returns nil settings without --data-package. It returns an error in each of these cases:
//   - --include-source or --package-name without --data-package.
//   - An invalid_package_name error for a package name that datapackage.ValidName rejects.
func packageFlags(cmd *cobra.Command) (*packageOptions, error) {
	dataPackage, err := cmd.Flags().GetBool("data-package")
	if err != nil {
		return nil, fmt.Errorf("read --data-package: %w", err)
	}
	includeSource, err := cmd.Flags().GetBool("include-source")
	if err != nil {
		return nil, fmt.Errorf("read --include-source: %w", err)
	}
	name, err := cmd.Flags().GetString("package-name")
	if err != nil {
		return nil, fmt.Errorf("read --package-name: %w", err)
	}
	if !dataPackage {
		if includeSource {
			return nil, errors.New("--include-source needs --data-package")
		}
		if cmd.Flags().Changed("package-name") {
			return nil, errors.New("--package-name needs --data-package")
		}
		return nil, nil
	}
	name = strings.TrimSpace(name)
	if !datapackage.ValidName(name) {
		return nil, errors.New("invalid_package_name: " + packageNameMessage("--package-name", name))
	}
	return &packageOptions{name: name, includeSource: includeSource}, nil
}

// prepareDataPackage applies rule to the data package at zipPath, before a job starts.
// It returns nil when no file exists at zipPath.
// With ifExistsOverwrite, it deletes the file. With ifExistsResume, it keeps the file, because the job replaces it at the end.
// It returns errOutputExists when rule is ifExistsFail, and other errors when the file cannot be checked or deleted.
func prepareDataPackage(zipPath string, rule ifExists) error {
	if _, err := os.Stat(zipPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("check the data package: %w", err)
	}
	switch rule {
	case ifExistsOverwrite:
		if err := os.Remove(zipPath); err != nil {
			return fmt.Errorf("delete the data package: %w", err)
		}
		return nil
	case ifExistsResume:
		return nil
	default:
		return errOutputExists
	}
}

// writeDataPackage writes the data package of opts beside the output file at outputPath and returns its path.
// With opts.includeSource, the data package also holds the source file of source.
// After the zip is complete, it removes the output file (FR-output-formats-19).
// It returns a package_failed error when the zip step fails, and then it keeps the output file.
func writeDataPackage(outputPath string, opts *packageOptions, source *xmlparser.MapSource) (string, error) {
	files := []datapackage.File{{
		Entry: filepath.Base(outputPath),
		Open: func() (io.ReadCloser, error) {
			return os.Open(outputPath) //nolint:gosec // G304: The job wrote the file at this path.
		},
	}}
	if opts.includeSource {
		files = append(files, sourceFile(source))
	}
	zipPath := opts.zipPath(outputPath)
	if err := datapackage.Write(zipPath, opts.name, files); err != nil {
		return "", fmt.Errorf("package_failed: %w", err)
	}
	// The zip holds the tiles, so a file that stays only uses disk space.
	if err := os.Remove(outputPath); err != nil {
		slog.Warn("remove the output file after the zip step", "path", outputPath, "error", err)
	}
	return zipPath, nil
}

// sourceFile returns the source file of source as a data package file.
// A snapshot source file is in the binary, so it opens from the snapshot and not from the disk.
func sourceFile(source *xmlparser.MapSource) datapackage.File {
	if source.Origin == xmlparser.OriginSnapshot {
		return datapackage.File{
			Entry: path.Base(source.File),
			Open:  func() (io.ReadCloser, error) { return snapshot.FS().Open(source.File) },
		}
	}
	return datapackage.File{
		Entry: filepath.Base(source.File),
		Open: func() (io.ReadCloser, error) {
			return os.Open(source.File) //nolint:gosec // G304: The parser read the source file at this path.
		},
	}
}
