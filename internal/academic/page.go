package academic

import (
	_ "embed"
	"net/http"
)

//go:embed academic.html
var page []byte

// Page is a public inspection shell; all archive reads require a user session.
func (s *Service) Page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	_, _ = w.Write(page)
}
