package users

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScheduleScriptUsesJavaScriptMIMEAndSameOriginCSP(t *testing.T) {
	s, _ := fixture(t)
	for _, method := range []string{"GET", "HEAD"} {
		w := httptest.NewRecorder()
		s.Web().ServeHTTP(w, httptest.NewRequest(method, "https://example.test/admin/schedules.js", nil))
		expectCode(t, w, 200)
		if w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("script response headers")
		}
		if method == "GET" && !strings.Contains(w.Body.String(), "function createPeriodEditor") {
			t.Fatal("editor asset missing")
		}
		if method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD should not return script body")
		}
	}
	w := httptest.NewRecorder()
	s.Web().ServeHTTP(w, httptest.NewRequest("GET", "https://example.test/admin", nil))
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || !strings.Contains(w.Body.String(), `src="/admin/schedules.js"`) {
		t.Fatal("script not loadable under CSP")
	}
}
