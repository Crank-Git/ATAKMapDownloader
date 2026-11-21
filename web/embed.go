// Package web holds the embedded files of the web interface.
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

//go:embed jobs.html
var jobsHTML []byte

// JobsHTML returns the embedded jobs.html content, the Jobs page of FR-team-server-25.
func JobsHTML() []byte {
	return jobsHTML
}

//go:embed signin.html
var signinHTML []byte

// SigninHTML returns the embedded signin.html content, the sign-in page of FR-team-server-23.
func SigninHTML() []byte {
	return signinHTML
}
