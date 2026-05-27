// Package webui embeds the compiled Svelte SPA so auracpd ships as a single
// binary. Build the UI first (cd web && npm run build) then copy web/dist here
// (handled by the release build); see docs/DEVELOPMENT.md.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves the SPA: static assets directly, with a fallback to
// index.html for client-side routes (so deep links work).
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			// Unknown path → SPA route: serve index.html.
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
