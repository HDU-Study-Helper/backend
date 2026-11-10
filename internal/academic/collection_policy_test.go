package academic

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"academic/backend/internal/httpkit"
	"academic/backend/internal/model"
	"academic/backend/internal/users"
)

type policyRepo struct {
	fakeRepo
	policy  model.CollectionPolicy
	version int
	ids     []string
}

func (f *policyRepo) CollectionPolicy(_ context.Context, owner string) (model.CollectionPolicy, error) {
	f.owner = owner
	f.calls++
	return f.policy, f.err
}
func (f *policyRepo) SaveCollectionPolicy(_ context.Context, owner string, version int, ids []string) (model.CollectionPolicy, error) {
	f.owner = owner
	f.version = version
	f.ids = ids
	f.calls++
	if version != f.policy.Version {
		return model.CollectionPolicy{}, model.ErrConflict
	}
	return f.policy, f.err
}
func (f *policyRepo) ExcludedCourseRefs(context.Context, model.DWSScope) ([]string, error) {
	panic("HTTP must not evaluate collection scope")
}

var _ model.CollectionPolicyRepository = (*policyRepo)(nil)

func policyRequest(h http.Handler, method, path, body, owner string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if owner != "" {
		r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: owner}))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestPolicyAuthenticationOwnerAndReplacement(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		f := &policyRepo{}
		h := New(f).Routes()
		w := policyRequest(h, method, "/collection-policy", `{"version":0,"offering_ids":[]}`, "")
		if w.Code != 401 || f.calls != 0 {
			t.Fatalf("anonymous %s: %d calls%d", method, w.Code, f.calls)
		}
		w = policyRequest(h, method, "/collection-policy", `{"version":0,"offering_ids":[]}`, "alice")
		if w.Code != 200 || f.owner != "alice" || !strings.Contains(w.Body.String(), `"excluded":[]`) {
			t.Fatalf("owner %s: %d %s", method, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("policy must not be cached")
		}
	}
	f := &policyRepo{policy: model.CollectionPolicy{Version: 7, Excluded: []model.CollectionExclusion{{OfferingID: "old", Name: "数学", ConnectionID: "disabled"}}}}
	w := policyRequest(New(f).Routes(), "PUT", "/collection-policy", `{"version":7,"offering_ids":["old","other-class"]}`, "alice")
	if w.Code != 200 || f.version != 7 || strings.Join(f.ids, ",") != "old,other-class" {
		t.Fatalf("replacement: %d %+v", w.Code, f)
	}
}
func TestPolicyStrictValidation(t *testing.T) {
	cases := []string{
		`{}`, `null`, `[]`, `{"version":0}`, `{"offering_ids":[]}`, `{"version":null,"offering_ids":[]}`, `{"version":-1,"offering_ids":[]}`, `{"version":0.5,"offering_ids":[]}`, `{"version":"0","offering_ids":[]}`, `{"version":0,"offering_ids":null}`, `{"version":0,"offering_ids":[1]}`, `{"version":0,"offering_ids":[""]}`, `{"version":0,"offering_ids":[" off"]}`, `{"version":0,"offering_ids":["a","a"]}`, `{"version":0,"offering_ids":["a\n"]}`, `{"version":0,"offering_ids":[],"user_id":"bob"}`, `{"version":0,"version":1,"offering_ids":[]}`, `{"version":0,"offering_ids":[],"offering_ids":["a"]}`, `{"Version":0,"offering_ids":[]}`, `{"version":0,"offering_ids":[]} {}`, `{"version":0,"offering_ids":[]`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			f := &policyRepo{}
			w := policyRequest(New(f).Routes(), "PUT", "/collection-policy", body, "alice")
			if w.Code != 400 || f.calls != 0 {
				t.Fatalf("status%d calls%d %s", w.Code, f.calls, w.Body.String())
			}
		})
	}
	for _, method := range []string{"GET", "PUT"} {
		for _, query := range []string{"?user_id=bob", "?source=mooc", "?version=0&version=1", "?%"} {
			f := &policyRepo{}
			w := policyRequest(New(f).Routes(), method, "/collection-policy"+query, `{"version":0,"offering_ids":[]}`, "alice")
			if w.Code != 400 || f.calls != 0 {
				t.Fatalf("query %s %s: %d", method, query, w.Code)
			}
		}
	}
	f := &policyRepo{}
	w := policyRequest(New(f).Routes(), "POST", "/collection-policy", `{}`, "alice")
	if w.Code != 405 || f.calls != 0 {
		t.Fatalf("method status%d", w.Code)
	}
}
func TestPolicyCapacityAndContentType(t *testing.T) {
	ids := make([]string, 2000)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	for _, count := range []int{2000, 2001} {
		selected := ids
		if count == 2001 {
			selected = append(append([]string{}, ids...), "extra")
		}
		body, _ := json.Marshal(map[string]any{"version": 0, "offering_ids": selected})
		f := &policyRepo{}
		w := policyRequest(New(f).Routes(), "PUT", "/collection-policy", string(body), "alice")
		want := 200
		if count > 2000 {
			want = 400
		}
		if w.Code != want {
			t.Fatalf("%d IDs status%d %s", count, w.Code, w.Body.String())
		}
	}
	f := &policyRepo{}
	r := httptest.NewRequest("PUT", "/collection-policy", strings.NewReader(`{"version":0,"offering_ids":[]}`))
	r = r.WithContext(httpkit.WithUser(r.Context(), model.User{ID: "alice"}))
	w := httptest.NewRecorder()
	New(f).Routes().ServeHTTP(w, r)
	if w.Code != 415 || f.calls != 0 {
		t.Fatalf("media status%d", w.Code)
	}
	w = policyRequest(New(f).Routes(), "PUT", "/collection-policy", `{"version":0,"offering_ids":["`+strings.Repeat("x", 256<<10)+`"]}`, "alice")
	if w.Code != 400 || f.calls != 0 {
		t.Fatalf("body bound status%d", w.Code)
	}
}
func TestPolicyConflictAndSafeErrors(t *testing.T) {
	f := &policyRepo{policy: model.CollectionPolicy{Version: 2}}
	w := policyRequest(New(f).Routes(), "PUT", "/collection-policy", `{"version":1,"offering_ids":[]}`, "alice")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"conflict"`) {
		t.Fatalf("stale %d %s", w.Code, w.Body.String())
	}
	for _, tc := range []struct {
		err    error
		status int
	}{{model.ErrNotFound, 404}, {model.ErrInvalidArgument, 400}, {model.ErrUnsupported, 422}, {errors.New("secret database password"), 500}} {
		f := &policyRepo{fakeRepo: fakeRepo{err: tc.err}}
		w := policyRequest(New(f).Routes(), "GET", "/collection-policy", "", "alice")
		if w.Code != tc.status || strings.Contains(w.Body.String(), "secret") {
			t.Fatalf("error %d %s", w.Code, w.Body.String())
		}
	}
	w = policyRequest(New(&fakeRepo{}).Routes(), "GET", "/collection-policy", "", "alice")
	if w.Code != 422 {
		t.Fatalf("optional repo %d", w.Code)
	}
}

type policyUserRepo struct{ model.UserRepository }

func (policyUserRepo) GetSessionUser(context.Context, []byte) (model.User, error) {
	return model.User{ID: "alice"}, nil
}
func TestPolicyUsesExistingSessionAndCSRF(t *testing.T) {
	auth, err := users.New(policyUserRepo{}, users.Config{Origin: "http://example.com", BcryptCost: 4})
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	cases := []struct {
		name, origin, csrf, header string
		session                    bool
		status                     int
	}{{"anonymous", "http://example.com", token, token, false, 401}, {"missingcsrf", "http://example.com", "", "", true, 403}, {"mismatch", "http://example.com", token, "wrong", true, 403}, {"origin", "http://elsewhere", token, token, true, 403}, {"missingorigin", "", token, token, true, 403}, {"valid", "http://example.com", token, token, true, 200}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &policyRepo{}
			r := httptest.NewRequest("PUT", "http://example.com/collection-policy", strings.NewReader(`{"version":0,"offering_ids":[]}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("X-CSRF-Token", tc.header)
			if tc.session {
				r.AddCookie(&http.Cookie{Name: users.SessionCookie, Value: token})
			}
			if tc.csrf != "" {
				r.AddCookie(&http.Cookie{Name: users.CSRFCookie, Value: tc.csrf})
			}
			w := httptest.NewRecorder()
			auth.Middleware(New(f).Routes()).ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status%d %s", w.Code, w.Body.String())
			}
			if tc.status != 200 && f.calls != 0 {
				t.Fatal("rejected request reached policy repository")
			}
			if tc.status == 200 && f.owner != "alice" {
				t.Fatal("wrong authenticated owner")
			}
		})
	}
}
