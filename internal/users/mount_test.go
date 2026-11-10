package users

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Exercise the composition pattern used by app.Router: specific protected routes
// coexist with a root-mounted users router and do not inherit public login.
func TestNestedMountKeepsSiblingAuthentication(t *testing.T) {
	s, _ := fixture(t)
	root := chi.NewRouter()
	root.Mount("/admin", s.Web())
	root.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.Middleware)
			r.Post("/bridge/demo", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
			sub := chi.NewRouter()
			sub.Get("/connections", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
			r.Mount("/dws", sub)
		})
		r.Mount("/", s.Routes())
	})
	for _, test := range []struct {
		method, path string
		code         int
	}{{"GET", "/admin", 200}, {"GET", "/admin/", 200}, {"GET", "/admin/schedules.js", 200}, {"GET", "/api/v1/auth/me", 401}, {"GET", "/api/v1/dws/connections", 401}, {"POST", "/api/v1/bridge/demo", 401}, {"GET", "/api/v1/does-not-exist", 404}} {
		t.Run(test.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			root.ServeHTTP(w, httptest.NewRequest(test.method, "https://example.test"+test.path, nil))
			expectCode(t, w, test.code)
		})
	}
	cookies := loginAs(t, s, "student", "user-password-123")
	r := httptest.NewRequest("GET", "https://example.test/api/v1/dws/connections", nil)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	root.ServeHTTP(w, r)
	expectCode(t, w, 202)
}
