package commands

import (
	"fmt"
	"net/http"
	"os"

	"github.com/crank-git/atak-map-downloader/internal/download"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

func NewServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the web interface",
		RunE:  runServe,
	}
	cmd.Flags().String("addr", "localhost:5000", "Address to listen on")
	cmd.Flags().String("mapsources-dir", "mapsources", "Directory containing XML map source files")
	return cmd
}

func runServe(cmd *cobra.Command, _ []string) error {
	dir, _ := cmd.Flags().GetString("mapsources-dir")
	addr, _ := cmd.Flags().GetString("addr")

	parser := xmlparser.New(dir)
	sources := parser.DiscoverSources(false)
	if len(sources) == 0 {
		fmt.Fprintf(os.Stderr, "Warning: No map sources found in %s\n", dir)
	}

	// Ensure downloads dir exists
	os.MkdirAll(download.DownloadsDir(), 0755)

	calc := tilecalc.NewTileCalculator()
	handler := NewWebHandler(parser, calc, dir)

	fmt.Printf("ATAK Map Downloader - Web Interface\n")
	fmt.Printf("Open http://%s in your browser\n", addr)
	fmt.Printf("Mapsources: %s (%d sources)\n", dir, len(sources))

	return http.ListenAndServe(addr, handler)
}
