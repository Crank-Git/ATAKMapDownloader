package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
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
		return estimateAreaFile(cmd, areaFile, sourceName, source.TileType)
	}
	bounds, coords := parseBBox(bboxStr)

	zoom, _ := cmd.Flags().GetInt("zoom")
	zoomRange, _ := cmd.Flags().GetString("zoom-range")
	maxTiles, _ := cmd.Flags().GetInt("max-tiles")

	var zoomLevels []int
	if zoom > 0 {
		zoomLevels = []int{zoom}
	} else if zoomRange != "" {
		r := strings.Split(zoomRange, "-")
		if len(r) != 2 {
			exitErr("Invalid zoom range format. Use min-max")
		}
		minZ, _ := strconv.Atoi(strings.TrimSpace(r[0]))
		maxZ, _ := strconv.Atoi(strings.TrimSpace(r[1]))
		zoomLevels = zoomLevelsBetween(minZ, maxZ)
	} else {
		optZoom := calc.CalculateZoomForArea(bounds, maxTiles)
		zoomLevels = []int{optZoom}
		fmt.Printf("Auto-selected zoom level: %d\n", optZoom)
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

// estimateAreaFile prints the tile count of each area of the --area file at path and the unique tile count (FR-area-selection-17).
// A feature with no zoom property gets the zoom range of --zoom or --zoom-range.
// It returns an error when a feature has no zoom property and the command has neither flag, or when an area is not valid.
func estimateAreaFile(cmd *cobra.Command, path, sourceName, tileType string) error {
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
	if code, message := checkAreas(areas); code != "" {
		return fmt.Errorf("%s: %s", code, message)
	}
	count := tilecalc.CountJobTiles(areas)
	sizeKB := tilecalc.TileSizeKB(tileType)
	var b strings.Builder
	fmt.Fprintf(&b, "\nDownload Estimate for '%s'\n-----------------------------------\n", sourceName)
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
