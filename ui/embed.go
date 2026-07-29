//go:build !embedui

// Package ui provides a placeholder dashboard filesystem when the server is
// built without the `embedui` tag, so the bare Go toolchain (go build, go vet,
// go test, go install) never depends on a Vite build having run first.
//
// The product build embeds the real dashboard: `make build` runs the UI build
// and passes -tags embedui. See ui/embed_prod.go.
package ui

import (
	"io/fs"
	"testing/fstest"
)

var emptyFS = fstest.MapFS{
	"index.html": &fstest.MapFile{Data: []byte(
		`<!doctype html><title>AML</title><p>UI not embedded. Build with -tags embedui.</p>`,
	)},
}

// DistDirFS returns a placeholder dashboard tree.
func DistDirFS() fs.FS { return emptyFS }
