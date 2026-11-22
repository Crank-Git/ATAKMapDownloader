package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// NewValidateSourceCmd returns the validate-source command, which checks one map source.
func NewValidateSourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate-source",
		Short: "Validate a specific map source configuration",
		RunE:  runValidateSource,
	}
	cmd.Flags().String("source", "", "Map source name (required)")
	cobra.CheckErr(cmd.MarkFlagRequired("source"))
	return cmd
}

func runValidateSource(cmd *cobra.Command, _ []string) error {
	parser := getParser(cmd)
	source, err := sourceFlag(cmd, parser)
	if err != nil {
		return err
	}
	sourceName := source.Name

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

	return specError(cmd, "✗ Source '"+sourceName+"' has validation errors:\n  - "+strings.Join(errors, "\n  - "))
}
