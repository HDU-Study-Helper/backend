package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"academic/backend/internal/dws"
	"academic/backend/internal/model"
	"academic/backend/internal/platform"
	"academic/backend/internal/runcontrol"
	"academic/backend/internal/schedule"
	"academic/backend/internal/users"
)

// The web wizard must reach real protected routes, rather than an unrelated
// maintenance server that happens to implement health and login.
func TestWebWizardRoutesMountedBehindSession(t *testing.T) {
	us, err := users.New(scheduleUserRepo{}, users.Config{BcryptCost: 4})
	if err != nil {
		t.Fatal(err)
	}
	handler := routerWithSchedules(us, &dws.Service{}, nil, nil, nil, nil,
		schedule.New(&scheduleRouteRepo{}, nil), &platform.Service{})
	for _, path := range []string{
		"/api/v1/platforms/sources",
		"/api/v1/platforms/connections",
		"/api/v1/dws/connections",
		"/api/v1/schedules",
	} {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("route missing or unprotected: status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

type apiOnlyControlStore struct {
	model.ControlRepository
	claims atomic.Int32
}

func (s *apiOnlyControlStore) Claim(context.Context, string) (model.ControlBatch, error) {
	s.claims.Add(1)
	return model.ControlBatch{}, model.ErrNotFound
}

func TestAPIOnlyDoesNotClaimBackgroundBatches(t *testing.T) {
	store := &apiOnlyControlStore{}
	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()
	a := &App{
		config:  Config{APIOnly: true, HTTPAddr: "127.0.0.1:0"},
		control: &runcontrol.Service{Store: store},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if store.claims.Load() != 0 {
		t.Fatal("API-only instance claimed work owned by the existing worker")
	}
}
