// Package web embeds the built UI. Run `make web` (or any make target that
// needs it) to produce web/dist before compiling.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built UI rooted at index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees "dist" exists
	}
	return sub
}
