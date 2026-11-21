// Command atak-dl downloads map tiles to MBTiles files for ATAK.
package main

import (
	"fmt"
	"os"
	"path/filepath"

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
		os.Exit(commands.ExitCode(err))
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

	root.PersistentFlags().String("mapsources-dir", defaultSourcesDir(), "User source directory, loaded after the embedded snapshot")
	root.PersistentFlags().Bool("no-snapshot", false, "Load only the user source directory, without the embedded snapshot")

	root.AddCommand(commands.NewListSourcesCmd())
	root.AddCommand(commands.NewEstimateCmd())
	root.AddCommand(commands.NewTileInfoCmd())
	root.AddCommand(commands.NewValidateSourceCmd())
	root.AddCommand(commands.NewDownloadCmd(userAgent(version)))
	root.AddCommand(commands.NewServeCmd(userAgent(version)))

	return root
}

// defaultSourcesDir returns <user config dir>/atak-dl/mapsources (FR-source-sync-6).
// When the OS names no user config directory, it returns "", so the catalog holds only the snapshot.
func defaultSourcesDir() string {
	config, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(config, "atak-dl", "mapsources")
}
