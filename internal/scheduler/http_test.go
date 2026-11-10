package scheduler

import (
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminTaskAPI(t *testing.T) {
	task := New("information-refresh", "信息刷新", 10*time.Minute)
	task.Start()
	defer task.Stop()
	event(t, task)
	if !task.Begin() {
		t.Fatal("start")
	}
	task.Finish(false)
	routes := Routes(task)
	request := func(role, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if role != "" {
			r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: "actor", Role: model.Role(role)}))
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		role string
		want int
	}{{"", 401}, {"user", 403}, {"admin", 200}} {
		if w := request(tc.role, "GET", "/", ""); w.Code != tc.want {
			t.Fatalf("%s: %d", tc.role, w.Code)
		}
	}
	for _, body := range []string{`{}`, `{"enabled":false,"interval_seconds":1}`, `{"enabled":true,"interval_seconds":86401}`, `{"enabled":true,"interval_seconds":600,"unexpected":true}`} {
		if w := request("admin", "PUT", "/information-refresh", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := request("admin", "PUT", "/information-refresh", `{"enabled":false,"interval_seconds":120}`); w.Code != 200 || task.Snapshot().Enabled {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("admin", "POST", "/information-refresh/trigger", ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("admin", "POST", "/information-refresh/trigger", ""); w.Code != 409 {
		t.Fatal("duplicate accepted", w.Code)
	}
	if w := request("admin", http.MethodPut, "/missing", `{}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
