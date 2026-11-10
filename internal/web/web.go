// Package web holds the built web interface, compiled into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Files is the built interface: index.html and its assets. It is empty in a
// binary built without the interface.
func Files() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}

// Built reports whether the interface was compiled in.
func Built() bool {
	_, err := fs.Stat(Files(), "index.html")
	return err == nil
}
