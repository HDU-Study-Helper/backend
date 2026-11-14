package mooc

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

// Sanitized shape observed with a valid long-lived login and stale STUDY_INFO.
const staleCoursePanels = `{"code":0,"result":{"result":null,"pagination":{"pageIndex":1,"pageSize":8,"totleCount":0,"totlePageCount":1}}}`
const restoredCoursePanels = `{"code":0,"result":{"result":[{"id":10,"name":"Fixture","termPanel":{"id":101}}],"pagination":{"totlePageCount":1}}}`

func TestCoursePanelsRefreshExistingLoginAndPersistJar(t *testing.T) {
	calls := []string{}
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/web/j/learnerCourseRpcBean.getMyLearnedCoursePanelList.rpc":
			c, _ := r.Cookie("STUDY_INFO")
			if c != nil && c.Value == "refreshed" {
				return response(restoredCoursePanels), nil
			}
			return response(staleCoursePanels), nil
		case "/home.htm":
			res := response("")
			res.StatusCode = http.StatusFound
			res.Header.Set("Location", origin+"/passport/logingate/changeCookie.htm?private=fixture")
			return res, nil
		case "/passport/logingate/changeCookie.htm":
			return response("official home", "STUDY_INFO=refreshed; Path=/; Secure"), nil
		default:
			t.Fatal("unexpected read", r.URL.Path)
			return nil, nil
		}
	})
	p, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "csrf"), contract.Course, "")
	if err != nil || !p.HasMore {
		t.Fatal(err, p.HasMore)
	}
	if len(calls) != 4 {
		t.Fatal("refresh must read home once and retry once", calls)
	}
	_, h, err := a.restore(scope, p.Session)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, origin+"/", nil)
	found := false
	for _, c := range h.Jar.Cookies(req.URL) {
		if c.Name == "STUDY_INFO" && c.Value == "refreshed" {
			found = true
		}
	}
	if !found {
		t.Fatal("refreshed cookie missing from page.Session")
	}
	// The next ordinary discovery uses the persisted cookie without a home read.
	_, err = a.Sync(context.Background(), scope, p.Session, contract.Course, "")
	if err != nil || len(calls) != 5 {
		t.Fatal(err, calls)
	}
}

func TestCoursePanelsRefreshNeverConvertsUnknownOrNullToEmpty(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantCalls  int
	}{
		{"persistent null", staleCoursePanels, 3},
		{"missing list", `{"code":0,"result":{"pagination":{"pageIndex":1,"totleCount":0,"totlePageCount":1}}}`, 1},
		{"unknown null", `{"code":0,"result":{"result":null}}`, 1},
		{"nonzero totals", `{"code":0,"result":{"result":null,"pagination":{"pageIndex":1,"totleCount":2,"totlePageCount":1}}}`, 1},
		{"wrong page", `{"code":0,"result":{"result":null,"pagination":{"pageIndex":2,"totleCount":0,"totlePageCount":1}}}`, 1},
		{"malformed rows", `{"code":0,"result":{"result":{}}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a := testAdapter(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path == "/home.htm" {
					return response("home"), nil
				}
				return response(tc.body), nil
			})
			p, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "csrf"), contract.Course, "")
			if !errors.Is(err, contract.ErrParse) || len(p.Records) != 0 || p.NextCursor != "" || calls != tc.wantCalls {
				t.Fatal(err, calls, p.HasMore)
			}
		})
	}
}

func TestCoursePanelsRefreshRejectsExternalRedirect(t *testing.T) {
	calls := 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "www.icourse163.org" {
			t.Fatal("credential boundary crossed")
		}
		if strings.HasSuffix(r.URL.Path, ".rpc") {
			return response(staleCoursePanels), nil
		}
		res := response("")
		res.StatusCode = http.StatusFound
		res.Header.Set("Location", "https://untrusted.invalid/")
		return res, nil
	})
	p, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "csrf"), contract.Course, "")
	if err == nil || calls != 2 || p.NextCursor != "" {
		t.Fatal(err, calls)
	}
}

func TestNormalEmptyCourseArrayDoesNotRefresh(t *testing.T) {
	calls := 0
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		calls++
		return response(`{"code":0,"result":{"result":[],"pagination":{"totlePageCount":1}}}`), nil
	})
	p, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "csrf"), contract.Course, "")
	if err != nil || calls != 1 || !p.HasMore {
		t.Fatal(err, calls)
	}
}
