package mooc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

func TestHTTPFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"rate", 429, "", contract.ErrRateLimited}, {"auth", 401, "", contract.ErrAuthRequired}, {"server", 503, "", contract.ErrUpstream}, {"oversize", 200, strings.Repeat("x", maxBody+1), contract.ErrParse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := testAdapter(func(*http.Request) (*http.Response, error) {
				r := response(tc.body)
				r.StatusCode = tc.status
				return r, nil
			})
			s := sessionFor(t, a, scope, "test")
			_, e := a.Sync(context.Background(), scope, s, contract.Course, "")
			if !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
		})
	}
	a := testAdapter(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, e := a.BeginAuth(ctx, scope)
	if !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestRepeatedPageAndMissingDirectory(t *testing.T) {
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		return response(`{"code":0,"result":{"result":[{"id":10,"termPanel":{"id":101},"schoolPanel":{"shortName":"TEST"}}],"pagination":{"totalPageCount":3}}}`), nil
	})
	s := sessionFor(t, a, scope, "alpha")
	p, e := a.Sync(context.Background(), scope, s, contract.Assignment, "")
	if e != nil {
		t.Fatal(e)
	}
	c, _ := newCursor(scope, contract.Assignment, p.NextCursor)
	c.Courses = nil
	c.Index = 0
	b, _ := json.Marshal(c)
	_, e = a.Sync(context.Background(), scope, s, contract.Assignment, base64Cursor(b))
	if !errors.Is(e, contract.ErrParse) {
		t.Fatal("repeated page accepted", e)
	}
	co := course{ID: "10", Term: "101", Type: 1}
	if _, e := termRecords(map[string]any{}, co, contract.Assignment); !errors.Is(e, contract.ErrParse) {
		t.Fatal(e)
	}
	p2, e := termRecords(map[string]any{"chapters": []any{}}, co, contract.Assignment)
	if e != nil || len(p2) != 0 {
		t.Fatal("explicit empty directory", e)
	}
}
func TestConcurrentConnectionCookies(t *testing.T) {
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		cookie, e := r.Cookie("NTESSTUDYSI")
		if e != nil {
			return nil, e
		}
		if r.Header.Get("edu-script-token") != cookie.Value {
			t.Error("mismatched CSRF")
		}
		return response(`{"code":0,"result":{"result":[],"pagination":{"totalPageCount":0}}}`, "NTESSTUDYSI="+cookie.Value+"-refreshed; Path=/; Secure"), nil
	})
	var wg sync.WaitGroup
	for _, id := range []string{"user-a", "user-b"} {
		sc := contract.Scope{UserID: id, ConnectionID: "connection"}
		session := sessionFor(t, a, sc, id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, e := a.Sync(context.Background(), sc, session, contract.Course, "")
			if e != nil {
				t.Error(e)
				return
			}
			st, c, e := a.restore(sc, p.Session)
			if e != nil || !st.Authorized {
				t.Error(e)
				return
			}
			_ = c
			if !strings.Contains(string(p.Session), id+"-refreshed") {
				t.Error("updated cookies missing")
			}
			other := "user-a"
			if sc.UserID == other {
				other = "user-b"
			}
			if strings.Contains(string(p.Session), other) {
				t.Error("cross-user cookies")
			}
		}()
	}
	wg.Wait()
}
func TestRedirectRestriction(t *testing.T) {
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://example.invalid/steal"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	_, e := a.BeginAuth(context.Background(), scope)
	if !errors.Is(e, contract.ErrUpstream) {
		t.Fatal(e)
	}
}

func TestNullableDirectoryChildren(t *testing.T) {
	// Sanitized shape from a live DTO: lessons with units:null coexist with tasks.
	const fixture = `{"id":101,"chapters":[{"units":null,"lessons":[{"units":null},{"units":[{"id":71,"contentId":81,"contentType":2,"name":"Fixture quiz","test":{"type":2}}]}],"exams":null}]}`
	var dto map[string]any
	decoder := json.NewDecoder(strings.NewReader(fixture))
	decoder.UseNumber()
	if e := decoder.Decode(&dto); e != nil {
		t.Fatal(e)
	}
	co := course{ID: "10", Term: "101", Type: 1}
	records, e := termRecords(dto, co, contract.Assignment)
	if e != nil || len(records) != 1 {
		t.Fatalf("task lost beside null collection: count=%d err=%v", len(records), e)
	}
	for _, invalid := range []any{nil, "unexpected", map[string]any{}} {
		if _, e := termRecords(map[string]any{"chapters": invalid}, co, contract.Assignment); !errors.Is(e, contract.ErrParse) {
			t.Fatal("invalid root accepted", e)
		}
	}
	dto["chapters"] = []any{map[string]any{"units": "unexpected"}}
	if _, e := termRecords(dto, co, contract.Assignment); !errors.Is(e, contract.ErrParse) {
		t.Fatal("malformed child accepted", e)
	}
}
