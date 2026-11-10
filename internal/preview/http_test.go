package preview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"academic/backend/internal/contract"
	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
)

type fakeRepo struct {
	owner string
	limit int
}

func (r *fakeRepo) Overview(_ context.Context, owner string) ([]model.ConnectionPreview, error) {
	r.owner = owner
	return []model.ConnectionPreview{{Source: "chaoxing", ConnectionID: "connection", State: "active", Enabled: true, Resources: []model.ResourcePreview{{Kind: "course", RecordCount: 2, EntityCount: 1}}}}, nil
}
func (r *fakeRepo) Records(_ context.Context, owner, source, connection, resource string, after int64, limit int) ([]model.PlatformRecord, error) {
	r.owner = owner
	r.limit = limit
	if connection != "connection" {
		return nil, model.ErrNotFound
	}
	return []model.PlatformRecord{{ID: 1, Source: source, Kind: resource, Content: json.RawMessage(`{"title":"课程"}`)}, {ID: 2, Source: source, Kind: resource, Content: json.RawMessage(`{"title":"课程更新"}`)}}, nil
}
func (r *fakeRepo) Courses(_ context.Context, owner, source, connection string) ([]model.CoursePreview, int64, error) {
	r.owner = owner
	if connection != "connection" {
		return nil, 0, model.ErrNotFound
	}
	return []model.CoursePreview{{Key: "opaque", CourseID: "1", ClassID: "2", Title: "课程"}}, 3, nil
}
func (r *fakeRepo) CourseRecords(ctx context.Context, owner, source, connection, kind, key string, after int64, limit int) ([]model.PlatformRecord, error) {
	if key != "opaque" {
		return nil, model.ErrInvalidArgument
	}
	return r.Records(ctx, owner, source, connection, kind, after, limit)
}
func TestPreviewHTTPAuthAndBoundaries(t *testing.T) {
	repo := &fakeRepo{}
	s := New(repo, []contract.Descriptor{{Source: "chaoxing", Resources: []contract.Resource{contract.Course, contract.Exam}}}, 600)
	request := func(path string, user bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if user {
			r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: "trusted-owner"}))
		}
		w := httptest.NewRecorder()
		s.Routes().ServeHTTP(w, r)
		return w
	}
	if w := request("/overview", false); w.Code != 401 {
		t.Fatal("anonymous preview allowed")
	}
	w := request("/overview?user_id=attacker", true)
	if w.Code != 200 || repo.owner != "trusted-owner" || !strings.Contains(w.Body.String(), `"kind":"exam"`) || !strings.Contains(w.Body.String(), `"poll_interval_seconds":600`) || !strings.Contains(w.Body.String(), `"dws_poll_interval_seconds":600`) {
		t.Fatal("owner/empty resource mismatch", w.Body.String())
	}
	w = request("/records?source=chaoxing&connection_id=connection&resource=course&limit=1", true)
	if w.Code != 200 || repo.limit != 2 || !strings.Contains(w.Body.String(), `"next_after":1`) {
		t.Fatal("pagination failed", w.Body.String())
	}
	for _, path := range []string{"/records?limit=0", "/records?limit=51", "/records?after=-1", "/records?after=bad"} {
		if w = request(path, true); w.Code != 400 {
			t.Fatal("invalid pagination allowed", path)
		}
	}
	w = request("/courses?source=chaoxing&connection_id=connection&user_id=attacker", true)
	if w.Code != 200 || repo.owner != "trusted-owner" || !strings.Contains(w.Body.String(), `"unassociated_count":3`) {
		t.Fatal("course overview scope failed", w.Body.String())
	}
	w = request("/records?source=chaoxing&connection_id=connection&course_key=opaque", true)
	if w.Code != 200 {
		t.Fatal("cross-resource course query failed")
	}
	if w = request("/records?source=chaoxing&connection_id=foreign", true); w.Code != 404 {
		t.Fatal("missing scope not rejected")
	}
}
