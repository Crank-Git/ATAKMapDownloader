package commands

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// NewEstimateCmd returns the estimate command, which prints the tile count and size of an area.
func NewEstimateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estimate",
		Short: "Estimate download size for a given area and zoom level(s)",
		RunE:  runEstimate,
	}
	cmd.Flags().String("source", "", "Map source name (required)")
	cmd.Flags().String("bbox", "", "Bounding box as min_lat,min_lon,max_lat,max_lon (required, except with --area)")
	cmd.Flags().String("area", "", "GeoJSON, KML or KMZ file of the areas, in place of --bbox")
	cmd.Flags().Float64("buffer", 0, "Buffer in metres for each line of the --area file with no buffer_m property")
	cmd.Flags().Int("zoom", 0, "Single zoom level")
	cmd.Flags().String("zoom-range", "", "Zoom range as min-max")
	cmd.Flags().Int("max-tiles", 10000, "Maximum tiles for auto zoom")
	cobra.CheckErr(cmd.MarkFlagRequired("source"))
	return cmd
}

func runEstimate(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	calc := tilecalc.NewTileCalculator()

	areaFile, bboxStr, err := areaOrBBox(cmd)
	if err != nil {
		return err
	}
	if areaFile == "" && bboxStr == "" {
		return errors.New("--bbox or --area is required")
	}

	source := sourceFlag(cmd, parser)
	sourceName := source.Name
	if areaFile != "" {
		return estimateAreaFile(cmd, areaFile, source)
	}
	bounds, coords := parseBBox(bboxStr)

	zoomLevels, err := estimateZoomLevels(cmd, calc, bounds)
	if err != nil {
		return err
	}

	// Filter valid zooms
	var valid []int
	for _, z := range zoomLevels {
		if z >= source.MinZoom && z <= source.MaxZoom {
			valid = append(valid, z)
		} else {
			fmt.Fprintf(os.Stderr, "Warning: Zoom %d outside source range (%d-%d)\n", z, source.MinZoom, source.MaxZoom)
		}
	}
	if len(valid) == 0 {
		exitErr("No valid zoom levels for this source")
	}

	fmt.Printf("\nDownload Estimate for '%s'\n", sourceName)
	fmt.Println("-----------------------------------")
	totalTiles := 0
	totalSizeKB := 0.0
	for _, z := range valid {
		tb := calc.CalculateTileBounds(bounds, z)
		est := calc.EstimateDownloadSize(tb, tilecalc.TileSizeKB(source.TileType))
		clat, _ := bounds.Center()
		res := calc.CalculateResolutionAtLatitude(clat, z)
		tiles := est["tile_count"].(int)
		sizeKB := est["size_kb"].(float64)
		totalTiles += tiles
		totalSizeKB += sizeKB
		fmt.Printf("  Zoom %2d: %s tiles, %s, %.1f m/px\n", z, formatInt(tiles), est["size_human"], res)
	}
	if len(valid) > 1 {
		fmt.Printf("\nTotal: %s tiles, %s\n", formatInt(totalTiles), tilecalc.FormatSize(totalSizeKB*1024))
	}
	fmt.Printf("\nArea: %.4f, %.4f to %.4f, %.4f\n", coords[0], coords[1], coords[2], coords[3])
	clat, clon := bounds.Center()
	fmt.Printf("Center: %.4f, %.4f\n", clat, clon)
	fmt.Printf("Area: %.6f square degrees\n", bounds.AreaDegrees())
	return nil
}

// estimateZoomLevels returns the zoom levels of a bounds estimate: --zoom, each level of --zoom-range, or the auto zoom for --max-tiles.
// It returns an error when --zoom-range is not min-max.
func estimateZoomLevels(cmd *cobra.Command, calc *tilecalc.TileCalculator, bounds *tilecalc.GeographicBounds) ([]int, error) {
	zoom, err := cmd.Flags().GetInt("zoom")
	if err != nil {
		return nil, fmt.Errorf("read --zoom: %w", err)
	}
	zoomRange, err := cmd.Flags().GetString("zoom-range")
	if err != nil {
		return nil, fmt.Errorf("read --zoom-range: %w", err)
	}
	maxTiles, err := cmd.Flags().GetInt("max-tiles")
	if err != nil {
		return nil, fmt.Errorf("read --max-tiles: %w", err)
	}
	if zoom > 0 {
		return []int{zoom}, nil
	}
	if zoomRange != "" {
		var minZ, maxZ int
		if _, err := fmt.Sscanf(zoomRange, "%d-%d", &minZ, &maxZ); err != nil {
			return nil, fmt.Errorf("--zoom-range %q is not min-max: %w", zoomRange, err)
		}
		return zoomLevelsBetween(minZ, maxZ), nil
	}
	optZoom := calc.CalculateZoomForArea(bounds, maxTiles)
	fmt.Printf("Auto-selected zoom level: %d\n", optZoom)
	return []int{optZoom}, nil
}

// estimateAreaFile prints the tile count of each area of the --area file at path and the unique tile count (FR-area-selection-17).
// A feature with no zoom property gets the zoom range of --zoom or --zoom-range.
// As the bounds estimate does, it removes each zoom level outside the source zoom range and warns about it (feature 02).
// It returns an error for these cases:
//   - A feature has no zoom property, and the command has neither flag.
//   - No area has a zoom level in the source zoom range.
//   - An area is not valid.
func estimateAreaFile(cmd *cobra.Command, path string, source *xmlparser.MapSource) error {
	zoom, err := cmd.Flags().GetInt("zoom")
	if err != nil {
		return fmt.Errorf("read --zoom: %w", err)
	}
	zoomRange, err := cmd.Flags().GetString("zoom-range")
	if err != nil {
		return fmt.Errorf("read --zoom-range: %w", err)
	}
	// A zoom level of -1 marks a feature that needs the zoom range of a flag, because no valid area has it.
	minZoom, maxZoom := -1, -1
	switch {
	case zoom > 0:
		minZoom, maxZoom = zoom, zoom
	case zoomRange != "":
		if _, err := fmt.Sscanf(zoomRange, "%d-%d", &minZoom, &maxZoom); err != nil {
			return fmt.Errorf("--zoom-range %q is not min-max: %w", zoomRange, err)
		}
	}
	areas, err := readAreaFile(cmd, path, minZoom, maxZoom)
	if err != nil {
		return err
	}
	for i, a := range areas {
		if a.MinZoom < 0 || a.MaxZoom < 0 {
			return fmt.Errorf("area %d of %s has no zoom property: set --zoom or --zoom-range", i+1, path)
		}
	}
	areas, err = sourceZoomAreas(cmd, areas, source)
	if err != nil {
		return err
	}
	if code, message := checkAreas(areas); code != "" {
		return fmt.Errorf("%s: %s", code, message)
	}
	count := tilecalc.CountJobTiles(areas)
	sizeKB := tilecalc.TileSizeKB(source.TileType)
	var b strings.Builder
	fmt.Fprintf(&b, "\nDownload Estimate for '%s'\n-----------------------------------\n", source.Name)
	for i, a := range areas {
		name := a.Name
		if name == "" {
			name = fmt.Sprintf("area %d", i+1)
		}
		fmt.Fprintf(&b, "  %s (%s, zoom %d-%d): %s tiles\n", name, a.Kind, a.MinZoom, a.MaxZoom, formatInt(count.Areas[i]))
	}
	fmt.Fprintf(&b, "\nUnique tiles: %s, %s\n", formatInt(count.Unique), tilecalc.FormatSize(float64(count.Unique)*sizeKB*1024))
	if _, err := io.WriteString(cmd.OutOrStdout(), b.String()); err != nil {
		return fmt.Errorf("print the estimate: %w", err)
	}
	return nil
}

// sourceZoomAreas returns the areas with their zoom ranges cut to the zoom range of source.
// It prints one warning for each zoom level that it removes, and it drops an area that keeps no zoom level.
// It returns an error when no area keeps a zoom level.
func sourceZoomAreas(cmd *cobra.Command, areas []tilecalc.JobArea, source *xmlparser.MapSource) ([]tilecalc.JobArea, error) {
	removed := map[int]bool{}
	kept := make([]tilecalc.JobArea, 0, len(areas))
	for _, a := range areas {
		// checkAreas reports an empty zoom range with its own message, so the cut leaves it as it is.
		if a.MinZoom > a.MaxZoom {
			kept = append(kept, a)
			continue
		}
		for z := a.MinZoom; z <= a.MaxZoom; z++ {
			if z < source.MinZoom || z > source.MaxZoom {
				removed[z] = true
			}
		}
		a.MinZoom, a.MaxZoom = max(a.MinZoom, source.MinZoom), min(a.MaxZoom, source.MaxZoom)
		if a.MinZoom <= a.MaxZoom {
			kept = append(kept, a)
		}
	}
	for _, z := range slices.Sorted(maps.Keys(removed)) {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: Zoom %d outside source range (%d-%d)\n", z, source.MinZoom, source.MaxZoom); err != nil {
			return nil, fmt.Errorf("print the zoom warning: %w", err)
		}
	}
	if len(kept) == 0 {
		return nil, errors.New("no valid zoom levels for this source")
	}
	return kept, nil
}

func formatInt(n int) string {
	s := strconv.Itoa(n)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteRune(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
