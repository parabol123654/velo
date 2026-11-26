package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var rawDist embed.FS

func DistFS() (fs.FS, error) {
	return fs.Sub(rawDist, "dist")
}
