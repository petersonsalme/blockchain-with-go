// Package explorer holds the static files of the read-only web explorer served at /explorer/.
package explorer

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Files returns the explorer files rooted at static, so index.html is at the root.
func Files() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil { // "static" is a valid, embedded path
		panic(err)
	}

	return sub
}
