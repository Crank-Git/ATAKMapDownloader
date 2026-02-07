package commands

import (
	"fmt"

	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/spf13/cobra"
)

func NewTileInfoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tile-info",
		Short: "Get tile information for a specific coordinate and zoom level",
		RunE:  runTileInfo,
	}
	cmd.Flags().Float64("lat", 0, "Latitude (required)")
	cmd.Flags().Float64("lon", 0, "Longitude (required)")
	cmd.Flags().Int("zoom", 10, "Zoom level (required)")
	cmd.MarkFlagRequired("lat")
	cmd.MarkFlagRequired("lon")
	cmd.MarkFlagRequired("zoom")
	return cmd
}

func runTileInfo(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	calc := tilecalc.NewTileCalculator()

	lat, _ := cmd.Flags().GetFloat64("lat")
	lon, _ := cmd.Flags().GetFloat64("lon")
	zoom, _ := cmd.Flags().GetInt("zoom")

	tileX, tileY := calc.LatLonToTile(lat, lon, zoom)
	tileLat, tileLon := calc.TileToLatLon(tileX, tileY, zoom)
	maxLat, maxLon := calc.TileToLatLon(tileX+1, tileY+1, zoom)
	res := calc.CalculateResolutionAtLatitude(lat, zoom)

	fmt.Printf("Tile Information for (%.4f, %.4f) at zoom %d:\n", lat, lon, zoom)
	fmt.Printf("  Tile coordinates: (%d, %d)\n", tileX, tileY)
	fmt.Printf("  Tile bounds: (%.6f, %.6f) to (%.6f, %.6f)\n", tileLat, tileLon, maxLat, maxLon)
	fmt.Printf("  Resolution: %.2f meters/pixel\n", res)

	sources := parser.DiscoverSources(false)
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
