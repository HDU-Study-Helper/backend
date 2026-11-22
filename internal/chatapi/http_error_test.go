package chatapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	f "academic/agent/framework"
	"academic/backend/internal/model"
)

func TestRateLimitIsNotAConversationConflict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"provider rate limit", f.Fail("RATE_LIMITED", "provider rate limit exceeded"), http.StatusTooManyRequests, "RATE_LIMITED"},
		{"in-flight conversation", model.ErrConflict, http.StatusConflict, "conflict"},
		{"stale question", f.Fail("CONFLICT", "question is no longer pending"), http.StatusConflict, "conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/threads/thread/messages", nil)
			w := httptest.NewRecorder()
			respond(w, r, http.StatusOK, nil, tc.err)
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("HTTP %d: %s (decode: %v)", w.Code, w.Body.String(), err)
			}
		})
	}
}
