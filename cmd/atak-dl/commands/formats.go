package commands

import (
	"fmt"
	"slices"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/output"

	// Each import registers the writer of one output format with the output package.
	_ "github.com/crank-git/atak-map-downloader/internal/mbtiles"
	_ "github.com/crank-git/atak-map-downloader/internal/output/gpkg"
)

// checkFormat returns an error code and a message when a job cannot write the output format format.
// The flag names the value in the message, for example "--format". It returns these codes:
//   - "invalid_output_format" when format is not in output.Formats. The message lists the valid values.
//   - "not_implemented" when format is valid but has no registered writer.
//
// It returns an empty code when a job can write format.
func checkFormat(flag, format string) (code, message string) {
	if !slices.Contains(output.Formats, format) {
		return "invalid_output_format", fmt.Sprintf("%s %q is not one of %s", flag, format, strings.Join(output.Formats, ", "))
	}
	if !output.Registered(format) {
		return "not_implemented", fmt.Sprintf("output format %s is not implemented", format)
	}
	return "", ""
}
