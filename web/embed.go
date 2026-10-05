package web

import (
	"embed"
	"io/fs"
)

// Assets contains the production frontend produced by npm run build.
//
//go:embed all:dist fallback/index.html
var Assets embed.FS

func FS() (fs.FS, error) {
	if _, err := fs.Stat(Assets, "dist/index.html"); err == nil {
		return fs.Sub(Assets, "dist")
	}
	return fs.Sub(Assets, "fallback")
}
