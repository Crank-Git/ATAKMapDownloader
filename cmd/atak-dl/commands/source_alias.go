package commands

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crank-git/atak-map-downloader/internal/snapshot"
	"github.com/crank-git/atak-map-downloader/internal/xmlparser"
	"github.com/spf13/cobra"
)

// findSource returns the map source with name, and a deprecation warning when name is a v0.1 name (FR-source-sync-16).
// A loaded map source with name comes before the alias, so a user source file with a v0.1 name keeps its name.
// It returns a nil source when no map source has name or its v2 name.
// It returns a *snapshot.RemovedError when upstream removed the map source.
func findSource(parser *xmlparser.XMLParser, name string) (*xmlparser.MapSource, string, error) {
	if source := parser.GetSource(name); source != nil {
		return source, "", nil
	}
	newName, err := snapshot.Alias(name)
	if err != nil || newName == "" {
		return nil, "", err
	}
	warning := fmt.Sprintf("%q is an old map source name. The new name is %q.", name, newName)
	return parser.GetSource(newName), warning, nil
}

// findWebSource returns the map source with name for a web API handler that starts no job.
// The handler has no job log, so the deprecation warning of a v0.1 name goes to the server log.
// It writes a 404 unknown_source response and returns false when no map source has the name or upstream removed it.
func (h *WebHandler) findWebSource(w http.ResponseWriter, name string) (*xmlparser.MapSource, bool) {
	source, warning, err := findSource(h.parser, name)
	if warning != "" {
		slog.Warn(warning)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, "unknown_source", err.Error())
		return nil, false
	}
	if source == nil {
		writeError(w, http.StatusNotFound, "unknown_source", "Source not found")
		return nil, false
	}
	return source, true
}

// sourceFlag returns the map source of the --source flag of cmd.
// It prints the deprecation warning of a v0.1 name on stderr.
// It returns an error when no map source has the name or upstream removed it.
func sourceFlag(cmd *cobra.Command, parser *xmlparser.XMLParser) (*xmlparser.MapSource, error) {
	// Every caller defines --source as a string flag, so GetString cannot fail.
	name, _ := cmd.Flags().GetString("source")
	source, warning, err := findSource(parser, name)
	if warning != "" {
		cmd.PrintErrln("Warning: " + warning)
	}
	if err != nil {
		return nil, specError(cmd, "Source '"+name+"': "+err.Error())
	}
	if source == nil {
		return nil, specError(cmd, "Source '"+name+"' not found")
	}
	return source, nil
}
