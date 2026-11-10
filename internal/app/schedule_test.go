package app

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"academic/backend/internal/model"
	"academic/backend/internal/schedule"
	"academic/backend/internal/users"
)

type scheduleUserRepo struct{ model.UserRepository }

func (scheduleUserRepo) GetSessionUser(context.Context, []byte) (model.User, error) {
	return model.User{ID: "trusted-owner", Role: model.RoleUser}, nil
}

type scheduleRouteRepo struct {
	model.ScheduleRepository
	creates int
	owner   string
}

func (r *scheduleRouteRepo) Create(_ context.Context, owner string, s model.Schedule) (model.Schedule, error) {
	r.creates++
	r.owner = owner
	s.ID = "created"
	return s, nil
}
func TestScheduleMountUsesSessionAndCSRF(t *testing.T) {
	us, e := users.New(scheduleUserRepo{}, users.Config{Origin: "http://localhost", BcryptCost: 4})
	if e != nil {
		t.Fatal(e)
	}
	repo := &scheduleRouteRepo{}
	svc := schedule.New(repo, nil)
	handler := routerWithSchedules(us, nil, svc, nil, nil, nil, svc)
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	request := func(auth, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/schedules", strings.NewReader(`{"academic_year":2026,"term":1,"teaching_week":8,"start_time":"2026-10-28T14:00:00+08:00","end_time":"2026-10-28T15:00:00+08:00","title":"test"}`))
		r.Header.Set("Content-Type", "application/json")
		if auth {
			r.AddCookie(&http.Cookie{Name: users.SessionCookie, Value: token})
		}
		if csrf {
			r.AddCookie(&http.Cookie{Name: users.CSRFCookie, Value: token})
			r.Header.Set("Origin", "http://localhost")
			r.Header.Set("X-CSRF-Token", token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := request(false, false); w.Code != 401 {
		t.Fatal("anonymous write", w.Code)
	}
	if w := request(true, false); w.Code != 403 {
		t.Fatal("CSRF bypass", w.Code)
	}
	if w := request(true, true); w.Code != 201 {
		t.Fatal("authenticated write", w.Code, w.Body.String())
	}
	if repo.creates != 1 || repo.owner != "trusted-owner" {
		t.Fatal("untrusted ownership or middleware bypass")
	}
}
