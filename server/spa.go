package main

import (
	"io/fs"
	"net/http"
	"strings"
)

// spaHandler serves a single-page-app bundle: known static assets are
// returned as-is, anything that looks like a client-route (no extension
// in the last path segment, and not /assets/...) falls back to
// index.html so react-router can take over on the browser side.
func spaHandler(root fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(root, p); err != nil {
			// Unknown path → rewrite to index.html for SPA routing.
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		// Hashed asset files get long cache; index.html stays uncached so
		// the user picks up new builds immediately.
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else if p == "index.html" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		}
		fileServer.ServeHTTP(w, r)
	})
}
