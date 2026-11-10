package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

type localTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (l localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = l.target.Scheme
	u.Host = l.target.Host
	clone.URL = &u
	return l.base.RoundTrip(clone)
}

var testScope = contract.Scope{UserID: "fixture-user", ConnectionID: "fixture-connection"}

func fixtureAdapter(t *testing.T, h http.HandlerFunc) *adapter {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	in := &http.Client{Transport: localTransport{u, http.DefaultTransport}}
	a, e := New(contract.Dependencies{HTTPClient: in})
	if e != nil {
		t.Fatal(e)
	}
	if in.Jar != nil || in.Timeout != 0 || in.CheckRedirect != nil {
		t.Fatal("mutated injected client")
	}
	a.(*adapter).client.Transport.(*pacedTransport).interval = 0
	return a.(*adapter)
}
func authorized(scope contract.Scope) contract.Session {
	b, _ := json.Marshal(state{Version: 1, Scope: scope, Authorized: true})
	return b
}
func coursesFixture(n int) string {
	items := []string{}
	for i := 0; i < n; i++ {
		items = append(items, fmt.Sprintf(`{"cpi":"p%d","content":{"id":"k%d","course":{"data":[{"id":"c%d","name":"Course %d","teacherfactor":"Teacher"}]}}}`, i, i, i, i))
	}
	return `{"result":1,"channelList":[` + strings.Join(items, ",") + `]}`
}
func TestQRPrivateJarStateAndIsolation(t *testing.T) {
	poll := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Header.Get("Cookie") != "" {
				t.Error("new login reused cookie")
			}
			http.SetCookie(w, &http.Cookie{Name: "qr", Value: "synthetic", Path: "/"})
			fmt.Fprint(w, `<input value='synthetic-uuid' id='uuid'><input id=enc value='synthetic-enc'>`)
		case "/createqr":
			if _, e := r.Cookie("qr"); e != nil {
				t.Error("missing creation cookie")
			}
			w.Write([]byte("\x89PNG\r\n\x1a\nfixture"))
		case "/getauthstatus/v2":
			if _, e := r.Cookie("qr"); e != nil {
				t.Error("missing restored cookie")
			}
			r.ParseForm()
			if r.Form.Get("enc") != "synthetic-enc" {
				t.Error("missing challenge")
			}
			poll++
			if poll == 1 {
				fmt.Fprint(w, `{"status":false,"type":4}`)
			} else {
				http.SetCookie(w, &http.Cookie{Name: "_uid", Value: "fixture-id", Domain: ".chaoxing.com", Path: "/"})
				http.SetCookie(w, &http.Cookie{Name: "cx_p_token", Value: "fixture-token", Domain: ".chaoxing.com", Path: "/"})
				fmt.Fprint(w, `{"status":true}`)
			}
		case "/base":
			if _, e := r.Cookie("_uid"); e != nil {
				t.Error("identity cookie missing")
			}
			fmt.Fprint(w, "<html>profile</html>")
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	first, e := a.BeginAuth(context.Background(), testScope)
	if e != nil || first.Status != "pending" || len(first.QRImage) == 0 || first.QRURL != "" {
		t.Fatalf("begin: %+v %v", first, e)
	}
	wire, _ := json.Marshal(first)
	if strings.Contains(string(wire), "synthetic") {
		t.Fatal("HTTP auth result leaked secrets")
	}
	scanned, e := a.PollAuth(context.Background(), testScope, first.Session)
	if e != nil || scanned.Status != "scanned" {
		t.Fatalf("scan %v %s", e, scanned.Status)
	}
	done, e := a.PollAuth(context.Background(), testScope, scanned.Session)
	if e != nil || done.Status != "authorized" {
		t.Fatalf("done %v %s", e, done.Status)
	}
	wrong := testScope
	wrong.UserID = "another-user"
	if _, e = a.PollAuth(context.Background(), wrong, done.Session); !errors.Is(e, contract.ErrAuthRequired) {
		t.Fatal("cross-user accepted", e)
	}
	wrong = testScope
	wrong.ConnectionID = "another-connection"
	if _, e = a.Sync(context.Background(), wrong, done.Session, contract.Course, ""); !errors.Is(e, contract.ErrAuthRequired) {
		t.Fatal("cross-connection accepted", e)
	}
	if _, e = a.BeginAuth(context.Background(), testScope); e != nil {
		t.Fatal(e)
	}
}
func TestCourseAndAssignmentPagination(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(51))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, `<input id=workEnc value='private-signature'><input id=enc value='private-stu'><input id=openc value='private-open'>`)
		case "/mooc-ans/mooc2/work/task":
			fmt.Fprint(w, `<div class="detailsHead"><div class="infoHead"><p>作答时间:11-04 09:30至11-08 23:59</p></div></div>`)
		case "/mooc-ans/mooc2/work/list":
			q := r.URL.Query()
			if q.Get("enc") != "private-signature" || q.Get("stuenc") != "private-stu" {
				t.Error("wrong signatures")
			}
			id := "work-a"
			if q.Get("pageNum") == "2" {
				id = "work-b"
			}
			fmt.Fprintf(w, `<div>1/2</div><li data="https://mooc1.chaoxing.com/mooc-ans/mooc2/work/task?courseId=c0&amp;classId=k0&amp;workId=%s&amp;enc=secret" aria-label="Homework ; 未交"></li>`, id)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	})
	session := authorized(testScope)
	p, e := a.Sync(context.Background(), testScope, session, contract.Course, "")
	if e != nil || len(p.Records) != 50 || !p.HasMore {
		t.Fatalf("course1 %v %+v", e, p)
	}
	p2, e := a.Sync(context.Background(), testScope, p.Session, contract.Course, p.NextCursor)
	if e != nil || len(p2.Records) != 1 || p2.HasMore {
		t.Fatalf("course2 %v %+v", e, p2)
	}
	if _, e = a.Sync(context.Background(), testScope, session, contract.Progress, p.NextCursor); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("resource cursor accepted", e)
	}
	v := cursor{Version: 1, Scope: testScope, Resource: contract.Assignment, Courses: []course{{ID: "c0", Class: "k0", CPI: "p0"}}, Page: 1}
	w1, e := a.Sync(context.Background(), testScope, session, contract.Assignment, encode(v))
	if e != nil || !w1.HasMore || len(w1.Records) != 1 {
		t.Fatalf("work1 %v %+v", e, w1)
	}
	w2, e := a.Sync(context.Background(), testScope, w1.Session, contract.Assignment, w1.NextCursor)
	if e != nil || w2.HasMore || w2.Records[0].ExternalID != "work-b" {
		t.Fatalf("work2 %v %+v", e, w2)
	}
	b, _ := json.Marshal(w1.Records)
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "signature") || w1.Records[0].ParentID != `["c0","k0"]` {
		t.Fatal("identity or redaction", string(b))
	}
}
func TestProgressNoticeAndExam(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(1))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, `<input id=enc value='signature'>`)
		case "/mooc2-ans/mycourse/studentcourse":
			fmt.Fprint(w, `<div>已完成任务点: <span>3</span>/10</div>`)
		case "/pc/notice/getNoticeList":
			r.ParseForm()
			last := "next"
			end := "false"
			if r.Form.Get("lastValue") == "next" {
				last = "final"
				end = "true"
			}
			fmt.Fprintf(w, `{"status":true,"notices":{"lastGetId":%q,"lastPage":%s,"list":[{"uuid":%q,"title":"Notice","content":"<p>Body</p>","enc":"do-not-export"}]}}`, last, end, last)
		case "/exam-ans/exam/test/examcode/examlist":
			fmt.Fprint(w, `<table><tr class=dataTr examid=exam-1><td>1</td><td>Exam</td><td>2026-01-01 至 2026-01-02</td><td>60</td><td>进行中</td><td>未交</td><td>---</td><td>未设置终端</td><td><a href='/start?examId=exam-1'>开始考试</a></td></tr></table>`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	s := authorized(testScope)
	p, e := a.Sync(context.Background(), testScope, s, contract.Progress, "")
	if e != nil || len(p.Records) != 1 || p.Records[0].Fields["completed"] != 3 {
		t.Fatalf("progress %v %+v", e, p)
	}
	n, e := a.Sync(context.Background(), testScope, s, contract.Notice, "")
	if e != nil || !n.HasMore || n.Records[0].Content != "Body" {
		t.Fatalf("notice %v %+v", e, n)
	}
	n2, e := a.Sync(context.Background(), testScope, s, contract.Notice, n.NextCursor)
	if e != nil || n2.HasMore {
		t.Fatalf("notice2 %v %+v", e, n2)
	}
	exam, e := a.Sync(context.Background(), testScope, s, contract.Exam, "")
	if e != nil || len(exam.Records) != 1 || exam.Records[0].ExternalID != "exam-1" {
		t.Fatalf("exam %v %+v", e, exam)
	}
}
func TestProgressPreservesInconsistentUpstreamCounts(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(2))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, `<input id=enc value='signature'>`)
		case "/mooc2-ans/mycourse/studentcourse":
			fmt.Fprint(w, `<h2>已完成任务点: <span>21</span>/16</h2>`)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	})
	p, err := a.Sync(context.Background(), testScope, authorized(testScope), contract.Progress, "")
	if err != nil || len(p.Records) != 1 || !p.HasMore {
		t.Fatal("source inconsistency must not stop later courses", p, err)
	}
	f := p.Records[0].Fields
	if f["completed"] != 21 || f["total"] != 16 || f["source_inconsistent"] != true || f["inconsistency_reason"] != "completed_exceeds_total" {
		t.Fatal(f)
	}
	p, err = a.Sync(context.Background(), testScope, p.Session, contract.Progress, p.NextCursor)
	if err != nil || len(p.Records) != 1 || p.HasMore {
		t.Fatal("second course did not continue", p, err)
	}
}
func TestFailuresAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
		want       error
	}{{"rate", "", 429, contract.ErrRateLimited}, {"login", `<form id="loginForm">`, 200, contract.ErrAuthRequired}, {"shape", `{"result":1}`, 200, contract.ErrParse}, {"invalid-json", "bad", 200, contract.ErrParse}, {"upstream", "", 500, contract.ErrUpstream}, {"auth", "", 401, contract.ErrAuthRequired}} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.code); fmt.Fprint(w, tc.body) })
			if _, e := a.Sync(context.Background(), testScope, authorized(testScope), contract.Course, ""); !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
		})
	}
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("expired/cancelled/unsupported made network request")
	})
	st := state{Version: 1, Scope: testScope, UUID: "fixture", Enc: "fixture", Expires: time.Now().Add(-time.Second)}
	b, _ := json.Marshal(st)
	p, e := a.PollAuth(context.Background(), testScope, b)
	if e != nil || p.Status != "expired" {
		t.Fatal(e, p.Status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = a.Sync(ctx, testScope, authorized(testScope), contract.Course, ""); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = a.Sync(context.Background(), testScope, nil, "unsupported", ""); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal(e)
	}
}
func TestCookieLifetimeAndDomainReplacement(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { t.Fatal("unexpected network") })
	s, _, j, e := a.restore(testScope, nil)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(passport + "/login")
	j.SetCookies(u, []*http.Cookie{{Name: "temporary", Value: "fixture", MaxAge: 60, Path: "/"}, {Name: "shared", Value: "old", Domain: ".chaoxing.com", Path: "/"}})
	if j.Entries[0].Cookie.Expires.IsZero() {
		t.Fatal("MaxAge not converted")
	}
	other, _ := url.Parse(api + "/mycourse")
	j.SetCookies(other, []*http.Cookie{{Name: "shared", Value: "new", Domain: "chaoxing.com", Path: "/"}})
	if len(j.Entries) != 2 {
		t.Fatal("domain cookie duplicated")
	}
	j.Entries[0].Cookie.Expires = time.Now().Add(-time.Hour)
	restored, _, private, e := a.restore(testScope, save(s, j))
	if e != nil {
		t.Fatal(e)
	}
	_ = restored
	for _, c := range private.Cookies(u) {
		if c.Name == "temporary" {
			t.Fatal("expired cookie revived")
		}
		if c.Name == "shared" && c.Value != "new" {
			t.Fatal("domain update lost")
		}
	}
}
func TestQRFailures(t *testing.T) {
	for _, tc := range []struct {
		body   string
		want   error
		status string
	}{{`{"status":false,"type":6}`, nil, "expired"}, {`{"status":false,"type":7}`, contract.ErrAuthRequired, ""}, {`{"status":true,"containTwoFactorLogin":true}`, contract.ErrUnsupported, ""}, {`{"status":false}`, contract.ErrParse, ""}} {
		t.Run(tc.body, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			b, _ := json.Marshal(state{Version: 1, Scope: testScope, UUID: "fixture", Enc: "fixture", Expires: time.Now().Add(time.Minute)})
			p, e := a.PollAuth(context.Background(), testScope, b)
			if !errors.Is(e, tc.want) || p.Status != tc.status {
				t.Fatal(e, p.Status)
			}
		})
	}
}
func TestRepeatedAssignmentPageRejected(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/visit/stucoursemiddle" {
			fmt.Fprint(w, `<input id=workEnc value='fixture'><input id=enc value='fixture'>`)
			return
		}
		fmt.Fprint(w, `<div>1/2</div><li data='/work/task?courseId=c&classId=k&workId=w' aria-label='Work ; 未交'></li>`)
	})
	raw := encode(cursor{Version: 1, Scope: testScope, Resource: contract.Assignment, Courses: []course{{ID: "c", Class: "k", CPI: "p"}}, Page: 1})
	p, e := a.Sync(context.Background(), testScope, authorized(testScope), contract.Assignment, raw)
	if e != nil || !p.HasMore {
		t.Fatal(e)
	}
	if failed, e := a.Sync(context.Background(), testScope, p.Session, contract.Assignment, p.NextCursor); !errors.Is(e, contract.ErrParse) || failed.NextCursor != "" {
		t.Fatal("repeated page accepted", e)
	}
}
func TestDeadlinePreserved(t *testing.T) {
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, e := a.Sync(ctx, testScope, authorized(testScope), contract.Course, ""); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}
func TestStrictParseAndCursorScope(t *testing.T) {
	co := course{ID: "c", Class: "k"}
	for _, s := range []string{`<html>Error page</html>`, `<li data='/work/task?courseId=c&classId=k&workId=w' aria-label='Work ; 未交'></li>`, `<div>1/1</div><li data='/work/task?courseId=wrong&classId=k&workId=w' aria-label='Work ; 未交'></li>`, `没有此页面访问权限`} {
		if _, _, e := assignments([]byte(s), co); !errors.Is(e, contract.ErrParse) {
			t.Fatal("accepted", s, e)
		}
	}
	rr, total, e := assignments([]byte(`<div>暂无作业</div>`), co)
	if e != nil || len(rr) != 0 || total != 0 {
		t.Fatal("explicit empty", e)
	}
	other := testScope
	other.UserID = "other"
	raw := encode(cursor{Version: 1, Scope: other, Resource: contract.Course, Page: 1})
	if _, e := decode(raw, testScope, contract.Course); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("scope cursor", e)
	}
}
