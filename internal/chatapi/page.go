package chatapi

import (
	_ "embed"
	"net/http"
)

//go:embed chat.html
var testPage []byte

// Page serves only the static test UI. Data and mutations go through the same
// authenticated, owner-scoped API used by an App client.
func Page(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	_, _ = w.Write(testPage)
}
