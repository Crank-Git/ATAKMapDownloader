// Command atak-dl downloads map tiles to MBTiles files for ATAK.
package main

import (
	"fmt"
	"os"

	"github.com/crank-git/atak-map-downloader/cmd/atak-dl/commands"
	"github.com/spf13/cobra"
)

// GoReleaser sets these values with -ldflags "-X main.version=...".
// A plain go build keeps the defaults.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if err := newRootCmd(version, commit, date).Execute(); err != nil {
		os.Exit(1)
	}
}

// userAgent returns the User-Agent of every tile request and every geocode request (FR-polite-downloading-14).
// The project URL lets a server operator find the project.
func userAgent(version string) string {
	return "atak-map-downloader/" + version + " (+https://github.com/Crank-Git/ATAKMapDownloader)"
}

// newRootCmd returns the atak-dl root command with every subcommand.
// The --version flag prints version, commit and date.
func newRootCmd(version, commit, date string) *cobra.Command {
	root := &cobra.Command{
		Use:     "atak-dl",
		Short:   "ATAK Tile Downloader - Download map tiles and create MBTILES databases",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
	}

	root.PersistentFlags().String("mapsources-dir", "mapsources", "Directory containing XML map source files")

	root.AddCommand(commands.NewListSourcesCmd())
	root.AddCommand(commands.NewEstimateCmd())
	root.AddCommand(commands.NewTileInfoCmd())
	root.AddCommand(commands.NewValidateSourceCmd())
	root.AddCommand(commands.NewDownloadCmd(userAgent(version)))
	root.AddCommand(commands.NewServeCmd(userAgent(version)))

	return root
}
