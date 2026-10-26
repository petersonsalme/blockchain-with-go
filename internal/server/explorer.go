package server

import (
	"bytes"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// explorerPrefix is where Handler mounts Config.Explorer.
const explorerPrefix = "/explorer/"

// explorerCSP forbids everything but same-origin files, so the page can neither run inline script nor reach another origin.
const explorerCSP = "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'"

// handleExplorer serves the read-only files of files under /explorer/; it never calls the Chain.
func handleExplorer(files fs.FS) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", explorerCSP)
		header.Set("X-Content-Type-Options", "nosniff")

		name := strings.TrimPrefix(r.URL.Path, explorerPrefix)
		if name == "" {
			name = "index.html"
		}

		data, err := fs.ReadFile(files, name) // a directory or a missing file fails here
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// ServeContent picks the Content-Type from the extension and answers HEAD without a body.
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	}
}
