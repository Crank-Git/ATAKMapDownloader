package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var staticFS embed.FS

// StaticFS returns the embedded static files with the "static" prefix stripped.
func StaticFS() (fs.FS, error) {
	return fs.Sub(staticFS, "static")
}

//go:embed index.html
var indexHTML []byte

// IndexHTML returns the embedded index.html content.
func IndexHTML() []byte {
	return indexHTML
}
