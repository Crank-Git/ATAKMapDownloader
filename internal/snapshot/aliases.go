package snapshot

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// aliasesJSON maps the v0.1 map source names to their ATAK-Maps v2 names (FR-source-sync-15).
// ATAK-Maps v1.10.1 renamed every map source, and v2.0.0 released the rename as a breaking change.
// FR-source-sync-17 keeps the table for at least one minor release after v0.5.0. Remove it no earlier than v0.6.0.
//
//go:embed aliases.json
var aliasesJSON []byte

// aliasTable is the content of aliases.json.
type aliasTable struct {
	// Aliases maps an old name to its v2 name.
	Aliases map[string]string `json:"aliases"`
	// Removed maps an old name with no v2 equivalent to the upstream removal.
	Removed map[string]removal `json:"removed"`
}

// removal is the upstream removal of a map source.
type removal struct {
	// Version is the first ATAK-Maps release without the map source.
	// The JSON also holds a reason for the reader of the file, which the code does not need.
	Version string `json:"version"`
}

// aliases returns the decoded alias table.
// The table is embedded, so a decode failure is a build defect and ends the program.
var aliases = sync.OnceValue(func() aliasTable {
	var table aliasTable
	if err := json.Unmarshal(aliasesJSON, &table); err != nil {
		panic(fmt.Sprintf("decode aliases.json: %v", err))
	}
	return table
})

// RemovedError reports an old map source name that upstream removed with no v2 equivalent.
type RemovedError struct {
	// Name is the old map source name.
	Name string
	// Version is the first ATAK-Maps release without the map source.
	Version string
}

func (e *RemovedError) Error() string {
	return "map source removed upstream in ATAK-Maps " + e.Version
}

// Alias returns the ATAK-Maps v2 name of the v0.1 map source name.
// It returns an empty string when name is not a v0.1 name.
// It returns a *RemovedError when upstream removed the map source.
func Alias(name string) (string, error) {
	table := aliases()
	if r, ok := table.Removed[name]; ok {
		return "", &RemovedError{Name: name, Version: r.Version}
	}
	return table.Aliases[name], nil
}
