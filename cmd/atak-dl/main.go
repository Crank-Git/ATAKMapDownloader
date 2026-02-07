package main

import (
	"os"

	"github.com/crank-git/atak-map-downloader/cmd/atak-dl/commands"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "atak-dl",
		Short: "ATAK Tile Downloader - Download map tiles and create MBTILES databases",
	}

	root.PersistentFlags().String("mapsources-dir", "mapsources", "Directory containing XML map source files")

	root.AddCommand(commands.NewListSourcesCmd())
	root.AddCommand(commands.NewEstimateCmd())
	root.AddCommand(commands.NewTileInfoCmd())
	root.AddCommand(commands.NewValidateSourceCmd())
	root.AddCommand(commands.NewDownloadCmd())
	root.AddCommand(commands.NewServeCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
