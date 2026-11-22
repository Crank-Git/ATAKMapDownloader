package commands

import (
	"fmt"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

// NewTileInfoCmd returns the tile-info command, which prints the tile that holds a coordinate.
func NewTileInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tile-info",
		Short: "Get tile information for a specific coordinate and zoom level",
		RunE:  runTileInfo,
	}
	cmd.Flags().Float64("lat", 0, "Latitude (required)")
	cmd.Flags().Float64("lon", 0, "Longitude (required)")
	cmd.Flags().Int("zoom", 10, "Zoom level (required)")
	cobra.CheckErr(cmd.MarkFlagRequired("lat"))
	cobra.CheckErr(cmd.MarkFlagRequired("lon"))
	cobra.CheckErr(cmd.MarkFlagRequired("zoom"))
	return cmd
}

func runTileInfo(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	calc := tilecalc.NewTileCalculator()

	lat, err := cmd.Flags().GetFloat64("lat")
	if err != nil {
		return fmt.Errorf("read --lat: %w", err)
	}
	lon, err := cmd.Flags().GetFloat64("lon")
	if err != nil {
		return fmt.Errorf("read --lon: %w", err)
	}
	zoom, err := cmd.Flags().GetInt("zoom")
	if err != nil {
		return fmt.Errorf("read --zoom: %w", err)
	}
	// LatLonToTile clamps a coordinate outside Web Mercator, so an unchecked value gives a tile that does not hold it (FR-area-estimate-21 to -23).
	if lat < -tilecalc.MaxLatitude || lat > tilecalc.MaxLatitude {
		return fmt.Errorf("--lat %g is outside the range %g to %g", lat, -tilecalc.MaxLatitude, tilecalc.MaxLatitude)
	}
	if lon < -180 || lon > 180 {
		return fmt.Errorf("--lon %g is outside the range -180 to 180", lon)
	}
	if zoom < 0 || zoom > tilecalc.MaxZoomLevel {
		return fmt.Errorf("--zoom %d is outside the range 0 to %d", zoom, tilecalc.MaxZoomLevel)
	}

	tileX, tileY := calc.LatLonToTile(lat, lon, zoom)
	tileLat, tileLon := calc.TileToLatLon(tileX, tileY, zoom)
	maxLat, maxLon := calc.TileToLatLon(tileX+1, tileY+1, zoom)
	res := calc.CalculateResolutionAtLatitude(lat, zoom)

	fmt.Printf("Tile Information for (%.4f, %.4f) at zoom %d:\n", lat, lon, zoom)
	fmt.Printf("  Tile coordinates: (%d, %d)\n", tileX, tileY)
	fmt.Printf("  Tile bounds: (%.6f, %.6f) to (%.6f, %.6f)\n", tileLat, tileLon, maxLat, maxLon)
	fmt.Printf("  Resolution: %.2f meters/pixel\n", res)

	sources := parser.DiscoverSources()
	count := 0
	for _, name := range parser.ListSources() {
		if count >= 3 {
			break
		}
		s := sources[name]
		if zoom >= s.MinZoom && zoom <= s.MaxZoom {
			url := s.FormatURL(tileX, tileY, zoom, "")
			fmt.Printf("\n  %s: %s\n", name, url)
			count++
		}
	}
	return nil
}
