// Package snapshot holds the ATAK-Maps source files that the binary embeds (FR-source-sync-1 to 3).
package snapshot

import (
	"embed"
	"io/fs"
)

// The license and the notice sit in the same file system as the source files.
// Thus the binary carries them whenever it carries the source catalog.
//
//go:embed VERSION LICENSE NOTICE.md maps
var files embed.FS

// FS returns the snapshot files.
// The source files are under maps/, in the ATAK-Maps layout.
// VERSION, LICENSE and NOTICE.md are at the root.
func FS() fs.FS {
	return files
}
