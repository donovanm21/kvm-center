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
			// Paths that clearly ask for a non-HTML resource must NOT
			// fall through to index.html — a curl-for-binary that
			// silently received the SPA page took down the whole
			// hv fleet during the v5.28.0 rollout (the HTML got
			// installed over the agent binary and systemd failed
			// with `Exec format error`). 404 is the honest answer.
			if isNonSPAPath(p) {
				http.NotFound(w, r)
				return
			}
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

// isNonSPAPath returns true for URL paths that clearly ask for a
// binary, script, or API-like resource. Keeps the SPA catch-all from
// silently handing back index.html when a caller expected bytes.
func isNonSPAPath(p string) bool {
	// Path prefixes that were known download/API namespaces at some
	// point and must never degrade to HTML if a handler regresses.
	prefixes := []string{"dist/", "downloads/", "api/", "health"}
	for _, pref := range prefixes {
		if strings.HasPrefix(p, pref) {
			return true
		}
	}
	// Extensions that are never client routes. Missing a file at one
	// of these paths means the resource really is gone, not that the
	// user hit /hosts/abc123 in the browser.
	exts := []string{
		".amd64", ".arm64", ".exe", ".sh", ".ps1",
		".bin", ".qcow2", ".img", ".iso",
		".tar", ".gz", ".zip", ".json", ".xml",
		".pem", ".crt", ".key",
	}
	for _, ext := range exts {
		if strings.HasSuffix(p, ext) {
			return true
		}
	}
	return false
}
