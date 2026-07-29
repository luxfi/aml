//go:build embedui

// Package ui embeds the AML admin dashboard built with Vite + React.
//
// The Go HTTP handler serves the admin UI at the /_/aml/ path. `make ui`
// produces dist/; the //go:embed directive below picks up the result.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistDirFS returns a rooted fs.FS pointing at dist/ so it can be passed
// directly to the Base router's Static handler.
func DistDirFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("aml-ui: dist/ missing — run `make ui` first: " + err.Error())
	}
	return sub
}
