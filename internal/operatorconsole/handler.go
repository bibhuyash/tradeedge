// Package operatorconsole serves read-only operator assets. It has no domain mutation ports.
package operatorconsole

import (
	"embed"
	"net/http"
)

//go:embed static/*
var assets embed.FS

func New() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", 405)
			return
		}
		files := map[string]string{"/console/": "index.html", "/console/app.js": "app.js", "/console/styles.css": "styles.css", "/console/export.css": "export.css", "/console/config.json": "config.json"}
		name, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		contentTypes := map[string]string{"index.html": "text/html; charset=utf-8", "app.js": "text/javascript; charset=utf-8", "styles.css": "text/css; charset=utf-8", "export.css": "text/css; charset=utf-8", "config.json": "application/json"}
		w.Header().Set("Content-Type", contentTypes[name])
		raw, err := assets.ReadFile("static/" + name)
		if err != nil {
			http.Error(w, "asset unavailable", 500)
			return
		}
		if r.Method != http.MethodHead {
			_, _ = w.Write(raw)
		}
	})
}
