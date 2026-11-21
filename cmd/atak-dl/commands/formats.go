package commands

import (
	"fmt"
	"slices"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/output"

	// Each import registers the writer of one output format with the output package, so every value of output.Formats has a writer.
	_ "github.com/crank-git/atak-map-downloader/internal/mbtiles"
	_ "github.com/crank-git/atak-map-downloader/internal/output/gpkg"
	_ "github.com/crank-git/atak-map-downloader/internal/output/osmdroid"
)

// checkFormat returns an error code and a message when a job cannot write the output format format for tiles of tileType.
// The flag names the value in the message, for example "--format". It returns these codes:
//   - "invalid_output_format" when format is not in output.Formats. The message lists the valid values.
//   - "unsupported_tile_type" when format is gpkg and tileType is not PNG or JPEG.
//
// It returns an empty code when a job can write format.
func checkFormat(flag, format, tileType string) (code, message string) {
	if !slices.Contains(output.Formats, format) {
		return "invalid_output_format", fmt.Sprintf("%s %q is not one of %s", flag, format, strings.Join(output.Formats, ", "))
	}
	// GeoPackage 1.3.1 Req 36 and 37 allow only PNG and JPEG tile data without an extension.
	if format == output.GeoPackage && !slices.Contains([]string{"png", "jpg", "jpeg"}, strings.ToLower(tileType)) {
		return "unsupported_tile_type", fmt.Sprintf("output format gpkg holds only png and jpeg tiles, and the map source has %s tiles", tileType)
	}
	return "", ""
}
