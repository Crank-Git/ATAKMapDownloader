package commands

import (
	"fmt"
	"os"

	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

func NewListSourcesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list-sources",
		Short: "List all available map sources",
		RunE:  runListSources,
	}
}

func runListSources(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Root().PersistentFlags().GetString("mapsources-dir")
	parser := xmlparser.New(dir)
	sources := parser.DiscoverSources(false)

	if len(sources) == 0 {
		fmt.Println("No map sources found.")
		fmt.Printf("Make sure XML files exist in: %s\n", parser.MapsourcesDir)
		return nil
	}

	fmt.Println("Available Map Sources")
	fmt.Println("--------------------")
	for _, name := range parser.ListSources() {
		s := sources[name]
		fmt.Printf("  %s (%s) - Zoom %d-%d - %s\n", name, s.TileType, s.MinZoom, s.MaxZoom, s.TileUpdate)
	}
	fmt.Printf("\nFound %d map sources\n", len(sources))
	return nil
}

func getParser(cmd *cobra.Command) *xmlparser.XMLParser {
	dir, _ := cmd.Root().PersistentFlags().GetString("mapsources-dir")
	if dir == "" {
		dir = "mapsources"
	}
	return xmlparser.New(dir)
}

func exitErr(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
