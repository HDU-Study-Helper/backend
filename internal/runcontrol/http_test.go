package runcontrol

import (
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

type httpStore struct {
	model.ControlRepository
	activated bool
}

func (s *httpStore) Overview(context.Context) (model.ControlOverview, error) {
	return model.ControlOverview{Mode: "manual"}, nil
}
func (s *httpStore) Activate(_ context.Context, round, source, id string, retry bool) (int, error) {
	if round == "" && id == "" {
		return 0, model.ErrInvalidArgument
	}
	if s.activated {
		return 0, nil
	}
	s.activated = true
	return 1, nil
}
func (s *httpStore) GetBatch(context.Context, string) (model.ControlBatch, error) {
	return model.ControlBatch{}, model.ErrNotFound
}
func (s *httpStore) GetRun(context.Context, string) (model.ControlRun, error) {
	return model.ControlRun{}, model.ErrNotFound
}
func TestControlAdminAPI(t *testing.T) {
	s := &Service{Store: &httpStore{}}
	routes := s.Routes()
	for _, tc := range []struct {
		role, method, path, body string
		want                     int
	}{
		{"", "GET", "/", "", 401}, {"user", "GET", "/", "", 403}, {"admin", "GET", "/", "", 200},
		{"user", "POST", "/start", `{"round_id":"r"}`, 403}, {"admin", "POST", "/start", `{}`, 400},
		{"admin", "POST", "/start", `{"round_id":"r","unexpected":true}`, 400},
		{"admin", "POST", "/start", `{"round_id":"r"}`, 200}, {"admin", "POST", "/start", `{"round_id":"r"}`, 200},
		{"admin", "GET", "/batches/missing", "", 404}, {"admin", "GET", "/runs/missing", "", 404},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if tc.role != "" {
			r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: "admin", Role: model.Role(tc.role)}))
		}
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s %s got %d want %d: %s", tc.role, tc.method, tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}
