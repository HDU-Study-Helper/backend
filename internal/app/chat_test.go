package app

import (
	"academic/backend/internal/agentadmin"
	"academic/backend/internal/model"
	"academic/backend/internal/users"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type chatRouteStore struct {
	model.ChatStore
	owner string
}

func (s *chatRouteStore) NewChatThread(_ context.Context, owner string) (model.ChatThread, error) {
	s.owner = owner
	return model.ChatThread{ID: "test-thread"}, nil
}
func TestRegularUserChatUsesSessionAndCSRFNotAdminPermissions(t *testing.T) {
	us, e := users.New(scheduleUserRepo{}, users.Config{Origin: "http://localhost", BcryptCost: 4})
	if e != nil {
		t.Fatal(e)
	}
	store := &chatRouteStore{}
	router := routerWithAgentAdmin(us, nil, nil, nil, &agentadmin.Console{ChatStore: store})
	page := httptest.NewRecorder()
	router.ServeHTTP(page, httptest.NewRequest("GET", "http://localhost/admin/chat", nil))
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Chat 测试") || !strings.Contains(page.Header().Get("Content-Security-Policy"), "connect-src 'self'") || store.owner != "" {
		t.Fatal("test UI should serve without creating a conversation", page.Code)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
	for _, tc := range []struct {
		session, csrf bool
		want          int
	}{{false, false, 401}, {true, false, 403}, {true, true, 201}} {
		r := httptest.NewRequest("POST", "http://localhost/api/v1/chat/threads", nil)
		if tc.session {
			r.AddCookie(&http.Cookie{Name: users.SessionCookie, Value: token})
		}
		if tc.csrf {
			r.AddCookie(&http.Cookie{Name: users.CSRFCookie, Value: token})
			r.Header.Set("X-CSRF-Token", token)
			r.Header.Set("Origin", "http://localhost")
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Fatal(tc, rec.Code, rec.Body.String())
		}
	}
	if store.owner != "trusted-owner" {
		t.Fatal("owner not derived from session")
	}
}
