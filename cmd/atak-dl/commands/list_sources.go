package commands

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/crank-git/atak-map-downloader/internal/snapshot"
	"github.com/crank-git/atak-map-downloader/internal/tilecalc"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// NewListSourcesCmd returns the list-sources command, which prints the map sources in the source catalog.
// With --skipped, it prints each skipped source file as "path  reason  detail" (FR-source-sync-28).
func NewListSourcesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list-sources",
		Short: "List all available map sources",
		RunE:  runListSources,
	}
	cmd.Flags().Bool("skipped", false, "List the source files that did not load, each with its reason")
	return cmd
}

func runListSources(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	skippedOnly, err := cmd.Flags().GetBool("skipped")
	if err != nil {
		return fmt.Errorf("read --skipped: %w", err)
	}

	var b strings.Builder
	if skippedOnly {
		skipped := parser.SkippedSources()
		if len(skipped) == 0 {
			fmt.Fprintln(&b, "No skipped source files.")
		}
		for _, s := range skipped {
			fmt.Fprintf(&b, "%s  %s  %s\n", s.Path, s.Reason, s.Detail)
		}
		if _, err := io.WriteString(cmd.OutOrStdout(), b.String()); err != nil {
			return fmt.Errorf("write the skipped list: %w", err)
		}
		return nil
	}

	sources := parser.DiscoverSources()
	if len(sources) == 0 {
		fmt.Fprintln(&b, "No map sources found.")
		fmt.Fprintf(&b, "Make sure XML files exist in: %s\n", parser.MapsourcesDir)
	} else {
		fmt.Fprintln(&b, "Available Map Sources")
		fmt.Fprintln(&b, "--------------------")
		for _, name := range parser.ListSources() {
			s := sources[name]
			fmt.Fprintf(&b, "  %s (%s) - Zoom %d-%d - %s - %s - %s - %s\n",
				name, s.TileType, s.MinZoom, s.MaxZoom, s.TileUpdate, sourcePolicy(s).PolicyClass, s.Kind, s.Origin)
		}
		fmt.Fprintf(&b, "\nFound %d map sources\n", len(sources))
	}
	if _, err := io.WriteString(cmd.OutOrStdout(), b.String()); err != nil {
		return fmt.Errorf("write the source list: %w", err)
	}
	return nil
}

// getParser returns the parser for the global --mapsources-dir and --no-snapshot flags.
// A root command without a flag gives the zero value, so a test can define only the flags it needs.
func getParser(cmd *cobra.Command) *xmlparser.XMLParser {
	dir, _ := cmd.Root().PersistentFlags().GetString("mapsources-dir")
	noSnapshot, _ := cmd.Root().PersistentFlags().GetBool("no-snapshot")
	if noSnapshot {
		return xmlparser.New(dir)
	}
	return xmlparser.NewWithSnapshot(snapshot.FS(), dir)
}

// specError prints msg to stderr as the whole error output, and returns an error with exit code 1.
// The spec names each msg verbatim, so cobra adds no "Error: " prefix and no usage text.
func specError(cmd *cobra.Command, msg string) error {
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), msg); err != nil {
		return fmt.Errorf("print the error: %w", err)
	}
	return errors.New(msg)
}

// parseBBox returns the bounds and the four values of a min_lat,min_lon,max_lat,max_lon string.
// It returns an error when the string does not hold four valid values.
func parseBBox(bboxStr string) (*tilecalc.GeographicBounds, [4]float64, error) {
	var coords [4]float64
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		return nil, coords, errors.New("bounding box must have 4 values: min_lat,min_lon,max_lat,max_lon")
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return nil, coords, fmt.Errorf("invalid bbox value %q: %w", p, err)
		}
		coords[i] = v
	}

	bounds, err := tilecalc.NewGeographicBounds(coords[0], coords[2], coords[1], coords[3])
	if err != nil {
		return nil, coords, fmt.Errorf("invalid bbox: %w", err)
	}
	return bounds, coords, nil
}

// zoomLevelsBetween returns the zoom levels from minZoom to maxZoom, both included.
// It returns nil when minZoom is greater than maxZoom.
func zoomLevelsBetween(minZoom, maxZoom int) []int {
	var zoomLevels []int
	for z := minZoom; z <= maxZoom; z++ {
		zoomLevels = append(zoomLevels, z)
	}
	return zoomLevels
}
