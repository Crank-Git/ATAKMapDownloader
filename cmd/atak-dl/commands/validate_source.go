package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func NewValidateSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate-source",
		Short: "Validate a specific map source configuration",
		RunE:  runValidateSource,
	}
	cmd.Flags().String("source", "", "Map source name (required)")
	cmd.MarkFlagRequired("source")
	return cmd
}

func runValidateSource(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	sourceName, _ := cmd.Flags().GetString("source")

	source := parser.GetSource(sourceName)
	if source == nil {
		exitErr("Source '" + sourceName + "' not found")
	}

	errors := parser.ValidateSource(source)
	if len(errors) == 0 {
		fmt.Printf("✓ Source '%s' is valid\n", sourceName)
		fmt.Printf("\nSource Details: %s\n", sourceName)
		fmt.Println("-------------------")
		fmt.Printf("  Name: %s\n", source.Name)
		fmt.Printf("  URL Template: %s\n", source.URL)
		fmt.Printf("  Tile Type: %s\n", source.TileType)
		fmt.Printf("  Zoom Range: %d - %d\n", source.MinZoom, source.MaxZoom)
		fmt.Printf("  Update Policy: %s\n", source.TileUpdate)
		fmt.Printf("  Background Color: %s\n", source.BackgroundColor)
		fmt.Printf("  Ignore Errors: %v\n", source.IgnoreErrors)
		if len(source.ServerParts) > 0 {
			fmt.Printf("  Server Parts: %v\n", source.ServerParts)
		}
		return nil
	}

	fmt.Fprintf(os.Stderr, "✗ Source '%s' has validation errors:\n", sourceName)
	for _, e := range errors {
		fmt.Fprintf(os.Stderr, "  - %s\n", e)
	}
	os.Exit(1)
	return nil
}
