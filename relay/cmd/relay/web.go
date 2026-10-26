package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// webApp serves the Owl web app (the `npm run build` output) from dir.
// Unknown paths fall back to index.html so links like /c/prod/pods load the app.
func webApp(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := filepath.Join(dir, filepath.FromSlash(filepathClean(r.URL.Path)))
		info, err := os.Stat(clean)
		if r.URL.Path == "/" || err != nil || info.IsDir() {
			w.Header().Set("Cache-Control", "no-cache") // always pick up a new release
			http.ServeFile(w, r, index)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // file names carry a hash
		}
		files.ServeHTTP(w, r)
	})
}

// filepathClean resolves "..", so a request can never leave the web folder.
func filepathClean(p string) string {
	return filepath.ToSlash(filepath.Clean("/" + p))
}

// securityHeaders sets browser protections on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
