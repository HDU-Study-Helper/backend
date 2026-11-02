package users

import (
	"embed"
	"net/http"
)

//go:embed admin.html
var webFiles embed.FS

// Web serves the embedded management page at /admin or /admin/.
func (s *Service) Web() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/admin" && r.URL.Path != "/admin/" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		data, _ := webFiles.ReadFile("admin.html")
		if r.Method != "HEAD" {
			_, _ = w.Write(data)
		}
	})
}
