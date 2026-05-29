// Package webui embeds the compiled Aura DB Svelte SPA so auracpd can serve
// it without an external build step at runtime.
//
// The Aura DB shell is a separate Vite build (../../web-aura-db) — see the
// project Makefile for the build+copy step. At runtime this package is mounted
// at /dbadmin/ in cmd/auracpd alongside the panel SPA (at /) and the dbadmin
// JSON API (at /api/dbadmin). All three cohabit on the same eTLD so the
// auracp_session + auracp_csrf cookies cross-mount automatically.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves the Aura DB SPA. Static assets resolve directly; any path
// that doesn't exist in the embedded tree falls back to index.html so client-
// side hash routes work after a deep-link refresh.
//
// The returned handler expects to be wrapped with http.StripPrefix("/dbadmin")
// before being mounted, so paths arrive without the /dbadmin/ prefix.
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
		if _, statErr := fs.Stat(sub, p); statErr != nil {
			// Unknown path → SPA route → serve index.html.
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}
