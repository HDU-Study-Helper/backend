package httpkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"academic/backend/internal/model"
	"github.com/go-chi/chi/v5/middleware"
)

func TestStrictDecodeAndErrorEnvelope(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `{"actor":"admin"}`, `{} {}`, `{"name":"` + strings.Repeat("a", 17000) + `"}`} {
		t.Run(body[:min(20, len(body))], func(t *testing.T) {
			var dst struct {
				Name string `json:"name"`
			}
			r := httptest.NewRequest("POST", "/", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			handler := middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if DecodeJSON(w, r, &dst) == nil {
					t.Fatal("accepted invalid JSON")
				}
			}))
			handler.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("status %d", w.Code)
			}
			var envelope struct {
				Error struct {
					Code      string
					Retryable bool
				}
				RequestID string `json:"request_id"`
			}
			if json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error.Code != "invalid_argument" || envelope.Error.Retryable || envelope.RequestID == "" {
				t.Fatal(w.Body.String())
			}
		})
	}
	var dst struct {
		Name string `json:"name"`
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"中文"}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	if err := DecodeJSON(httptest.NewRecorder(), r, &dst); err != nil || dst.Name != "中文" {
		t.Fatal("valid JSON rejected")
	}
}
func TestUserContextDoesNotExposeHash(t *testing.T) {
	ctx := WithUser(context.Background(), model.User{ID: "id", Role: model.RoleAdmin, PasswordHash: "secret"})
	u, ok := CurrentUser(ctx)
	if !ok || u.PasswordHash != "" {
		t.Fatal("unsafe context")
	}
	if _, ok = CurrentUser(WithUser(ctx, model.User{ID: "id", Disabled: true})); ok {
		t.Fatal("disabled user trusted")
	}
}
