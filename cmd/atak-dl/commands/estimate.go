package commands

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

func NewEstimateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estimate",
		Short: "Estimate download size for a given area and zoom level(s)",
		RunE:  runEstimate,
	}
	cmd.Flags().String("source", "", "Map source name (required)")
	cmd.Flags().String("bbox", "", "Bounding box as min_lat,min_lon,max_lat,max_lon (required)")
	cmd.Flags().Int("zoom", 0, "Single zoom level")
	cmd.Flags().String("zoom-range", "", "Zoom range as min-max")
	cmd.Flags().Int("max-tiles", 10000, "Maximum tiles for auto zoom")
	cmd.MarkFlagRequired("source")
	cmd.MarkFlagRequired("bbox")
	return cmd
}

func runEstimate(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	calc := tilecalc.NewTileCalculator()

	bboxStr, _ := cmd.Flags().GetString("bbox")
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		exitErr("Bounding box must have 4 values: min_lat,min_lon,max_lat,max_lon")
	}
	var coords [4]float64
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			exitErr("Invalid bbox value: " + p)
		}
		coords[i] = v
	}

	bounds, err := tilecalc.NewGeographicBounds(coords[0], coords[2], coords[1], coords[3])
	if err != nil {
		exitErr(err.Error())
	}

	sourceName, _ := cmd.Flags().GetString("source")
	source := parser.GetSource(sourceName)
	if source == nil {
		exitErr("Source '" + sourceName + "' not found")
	}

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
		for z := minZ; z <= maxZ; z++ {
			zoomLevels = append(zoomLevels, z)
		}
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
		est := calc.EstimateDownloadSize(tb, 15)
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
