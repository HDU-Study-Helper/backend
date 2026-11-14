package mooc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string, headers ...string) *http.Response {
	h := http.Header{}
	for _, s := range headers {
		h.Add("Set-Cookie", s)
	}
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}
func base64Cursor(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

var scope = contract.Scope{UserID: "fixture-user", ConnectionID: "fixture-connection"}

func testAdapter(f transport) *adapter {
	a, _ := New(contract.Dependencies{HTTPClient: &http.Client{Transport: f}})
	return a.(*adapter)
}
func sessionFor(t *testing.T, a *adapter, s contract.Scope, token string) contract.Session {
	t.Helper()
	st, c, e := a.restore(s, nil)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(origin)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: "NTESSTUDYSI", Value: token, Path: "/"}, {Name: "STUDY_INFO", Value: "mock", Path: "/"}})
	st.Authorized = true
	b, e := save(st, c)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func pngFixture(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestQRStateMachine(t *testing.T) {
	imageBytes := pngFixture(t)
	imageRequests := 0
	n := 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/fixture.png":
			imageRequests++
			if r.URL.Scheme != "https" || r.URL.Host != "mooc-image.nosdn.127.net" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("edu-script-token") != "" {
				t.Fatal("image credentials or host")
			}
			return response(string(imageBytes)), nil
		case "/logonByQRCode/code.do":
			return response(`{"code":0,"result":{"codeUrl":"http://mooc-image.nosdn.127.net/fixture.png","pollKey":"fixture-poll"}}`, "qr=private; Path=/"), nil
		case "/logonByQRCode/poll.do":
			if r.URL.Query().Get("pollKey") != "fixture-poll" {
				t.Fatal("missing poll key")
			}
			if c, e := r.Cookie("qr"); e != nil || c.Value != "private" {
				t.Fatal("lost QR jar")
			}
			states := []string{`0`, `1`, `2`}
			v := states[n]
			n++
			return response(`{"code":0,"result":{"codeStatus":` + v + `,"token":"mock-token"}}`), nil
		case "/passport/logingate/mocMobChangeCookie.htm":
			if r.URL.Query().Get("token") != "mock-token" {
				t.Fatal("exchange token")
			}
			return response("ok", "STUDY_INFO=fixture; Path=/; Secure", "NTESSTUDYSI=fixture-csrf; Path=/; Secure"), nil
		}
		t.Fatalf("unexpected path %s", r.URL.Path)
		return nil, nil
	})
	r, e := a.BeginAuth(context.Background(), scope)
	if e != nil || r.Status != "pending" || !strings.HasPrefix(r.QRURL, "https://mooc-image.nosdn.127.net/") {
		t.Fatalf("begin %v %+v", e, r)
	}
	if !bytes.Equal(r.QRImage, imageBytes) || r.QRMediaType != "image/png" {
		t.Fatal("missing downloaded QR PNG")
	}
	for _, want := range []string{"pending", "scanned", "authorized"} {
		r, e = a.PollAuth(context.Background(), scope, r.Session)
		if e != nil || r.Status != want {
			t.Fatalf("want %s got %s %v", want, r.Status, e)
		}
	}
	if imageRequests != 1 {
		t.Fatal("poll redownloaded image")
	}
	b, _ := json.Marshal(r)
	if strings.Contains(string(b), "mock-token") || strings.Contains(string(b), "csrf") || strings.Contains(string(b), "Session") {
		t.Fatal("session in public JSON")
	}
	_, e = a.PollAuth(context.Background(), contract.Scope{UserID: "other", ConnectionID: scope.ConnectionID}, r.Session)
	if !errors.Is(e, contract.ErrInvalid) {
		t.Fatal(e)
	}
}
func TestQRExpiredAndInvalid(t *testing.T) {
	for _, status := range []string{"3", "9"} {
		a := testAdapter(func(*http.Request) (*http.Response, error) {
			return response(`{"code":0,"result":{"codeStatus":` + status + `}}`), nil
		})
		s, c, _ := a.restore(scope, nil)
		s.PollKey = "mock"
		s.Expires = time.Now().Add(time.Minute)
		b, _ := save(s, c)
		r, e := a.PollAuth(context.Background(), scope, b)
		if status == "3" {
			if e != nil || r.Status != "expired" {
				t.Fatal(e, r.Status)
			}
		} else if !errors.Is(e, contract.ErrParse) {
			t.Fatal(e)
		}
	}
	a := testAdapter(func(*http.Request) (*http.Response, error) { t.Fatal("expired makes no request"); return nil, nil })
	s, c, _ := a.restore(scope, nil)
	s.PollKey = "mock"
	s.Expires = time.Now().Add(-time.Second)
	b, _ := save(s, c)
	r, e := a.PollAuth(context.Background(), scope, b)
	if e != nil || r.Status != "expired" {
		t.Fatal(e)
	}
}

const termFixture = `{"code":0,"result":{"lastLearnUnitId":71,"mocTermDto":{"id":101,"chapters":[{"id":1,"lessons":[{"id":2,"units":[{"id":71,"contentId":81,"contentType":2,"name":"Quiz fixture","test":{"submitStatus":0,"studentScore":0,"submitEndTime":1800000000000},"cookie":"must not escape","answer":"must not escape","url":"https://example.invalid/?token=secret"},{"id":72,"contentId":82,"contentType":6,"name":"Exam fixture","test":{"type":6,"usedTryCount":0}}]}],"homeworks":[{"id":73,"contentId":83,"unitId":74,"name":"Homework fixture","test":{"evaluateEnd":1800000000001,"peerReviewStatus":"pending"}}]}]}}}`

func TestCourseTwoPagesAndTerms(t *testing.T) {
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return response(`window.courseDto={id:10};<div class="m-infomation_content-section">Introduction</div>`), nil
		}
		r.ParseForm()
		if strings.Contains(r.URL.Path, "getLastLearnedMocTermDto") {
			return response(`{"code":0,"result":{"mocTermDto":{"id":` + r.Form.Get("termId") + `,"chapters":[]}}}`), nil
		}
		typ := r.Form.Get("courseType")
		p := r.Form.Get("p")
		if typ == "2" {
			return response(`{"code":0,"result":{"result":[],"pagination":{"totalPageCount":0}}}`), nil
		}
		term := 101
		if p == "2" {
			term = 102
		}
		return response(fmt.Sprintf(`{"code":0,"result":{"result":[{"id":10,"name":"Fixture","termPanel":{"id":%d},"schoolPanel":{"shortName":"TEST"}}],"pagination":{"totlePageCount":2}}}`, term)), nil
	})
	s := sessionFor(t, a, scope, "alpha")
	cur := ""
	ids := map[string]bool{}
	for i := 0; i < 9; i++ {
		p, e := a.Sync(context.Background(), scope, s, contract.Course, cur)
		if e != nil {
			t.Fatal(e)
		}
		for _, r := range p.Records {
			ids[r.ExternalID] = true
		}
		if i < 8 && !p.HasMore {
			t.Fatal("lost next page")
		}
		if i == 8 && p.HasMore {
			t.Fatal("did not finish")
		}
		cur = p.NextCursor
	}
	if len(ids) != 2 {
		t.Fatal(ids)
	}
}
func TestCourseWithoutSchoolUsesOfficialIDOnlyReferer(t *testing.T) {
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "getMyLearnedCoursePanelList") {
			return response(`{"code":0,"result":{"result":[{"id":10,"name":"Fixture","termPanel":{"id":101},"schoolPanel":null}],"pagination":{"totalPageCount":1}}}`), nil
		}
		if r.Header.Get("Referer") != origin+"/learn/10?tid=101" {
			t.Fatal("empty school must not introduce a hyphen", r.Header.Get("Referer"))
		}
		return response(termFixture), nil
	})
	s := sessionFor(t, a, scope, "alpha")
	p, e := a.Sync(context.Background(), scope, s, contract.Assignment, "")
	if e != nil || !p.HasMore {
		t.Fatal("school name is optional, not a course identity", p, e)
	}
	p, e = a.Sync(context.Background(), scope, p.Session, contract.Assignment, p.NextCursor)
	if e != nil || len(p.Records) != 2 {
		t.Fatal(p, e)
	}
}
func TestCatalogReadRetriesTransientGatewayOnly(t *testing.T) {
	calls := 0
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls < 3 {
			res := response("gateway unavailable")
			res.StatusCode = 502
			return res, nil
		}
		return response(`{"code":0,"result":{"result":[],"pagination":{"totalPageCount":0}}}`), nil
	})
	_, h, e := a.restore(scope, sessionFor(t, a, scope, "alpha"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = rpc(context.Background(), h, "learnerCourseRpcBean.getMyLearnedCoursePanelList", origin+"/", url.Values{}); e != nil || calls != 3 {
		t.Fatal(e, calls)
	}
	for _, status := range []int{401, 429, 500} {
		calls = 0
		a = testAdapter(func(*http.Request) (*http.Response, error) {
			calls++
			res := response("not a catalog")
			res.StatusCode = status
			return res, nil
		})
		_, h, e = a.restore(scope, sessionFor(t, a, scope, "alpha"))
		if e != nil {
			t.Fatal(e)
		}
		_, e = rpc(context.Background(), h, "learnerCourseRpcBean.getMyLearnedCoursePanelList", origin+"/", url.Values{})
		if e == nil || calls != 1 {
			t.Fatal("permanent/auth/rate error retried", status, e, calls)
		}
	}
	calls = 0
	a = testAdapter(func(*http.Request) (*http.Response, error) { calls++; return response(`{"code":0,"result":null}`), nil })
	_, h, _ = a.restore(scope, sessionFor(t, a, scope, "alpha"))
	_, e = rpc(context.Background(), h, "learnerCourseRpcBean.getMyLearnedCoursePanelList", origin+"/", nil)
	if !errors.Is(e, contract.ErrParse) || calls != 1 {
		t.Fatal("schema errors must not retry", e, calls)
	}
}
func TestCatalogRetryRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		calls++
		cancel()
		res := response("gateway unavailable")
		res.StatusCode = 503
		return res, nil
	})
	_, h, _ := a.restore(scope, sessionFor(t, a, scope, "alpha"))
	_, e := rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", origin+"/", nil)
	if !errors.Is(e, context.Canceled) || calls != 1 {
		t.Fatal(e, calls)
	}
}
func TestTaskAndProgressReadOnly(t *testing.T) {
	a := testAdapter(func(r *http.Request) (*http.Response, error) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("termId") != "101" || !strings.Contains(r.Header.Get("Referer"), "tid=101") {
			t.Fatal("term/referer")
		}
		switch r.URL.Path {
		case "/web/j/courseBean.getLastLearnedMocTermDto.rpc":
			return response(termFixture), nil
		case "/web/j/courseBean.getNewExamList.rpc":
			return response(`{"code":0,"result":[]}`), nil
		case "/web/j/mocCourseV2RpcBean.getMemberLearningRate.rpc":
			return response(`{"code":0,"result":{"termId":101,"lessonLearned":2,"lessonsCount":10}}`), nil
		default:
			t.Fatalf("unsafe/unexpected endpoint %s", r.URL.Path)
			return nil, nil
		}
	})
	s := sessionFor(t, a, scope, "alpha")
	for _, r := range []contract.Resource{contract.Assignment, contract.Exam, contract.Progress} {
		c, _ := newCursor(scope, r, "")
		c.Done = true
		c.Courses = []course{{ID: "10", Term: "101", School: "TEST", Type: 2, Discovered: true}}
		b, _ := json.Marshal(c)
		raw := base64Cursor(b)
		p, e := a.Sync(context.Background(), scope, s, r, raw)
		if e != nil {
			t.Fatal(r, e)
		}
		want := 2
		if r == contract.Exam {
			want = 1
		}
		if r == contract.Progress {
			want = 3
		}
		if len(p.Records) != want || p.HasMore != (r == contract.Exam || r == contract.Progress) {
			t.Fatalf("%s got %d records, hasMore=%v", r, len(p.Records), p.HasMore)
		}
		if p.HasMore {
			next, err := a.Sync(context.Background(), scope, p.Session, r, p.NextCursor)
			if err != nil || next.HasMore {
				t.Fatal("supplemental read did not finish", r, next, err)
			}
			if r == contract.Exam && len(next.Records) != 0 || r == contract.Progress && len(next.Records) != 1 {
				t.Fatal("unexpected supplemental records", r, next.Records)
			}
		}
		data, _ := json.Marshal(p.Records)
		for _, secret := range []string{"must not escape", "token=secret", "Cookie", "csrf"} {
			if strings.Contains(string(data), secret) {
				t.Fatal("private source field escaped")
			}
		}
		for _, record := range p.Records {
			if record.SourceVersion != "" {
				t.Fatal("fabricated version")
			}
			if record.Fields["courseId"] != "10" || record.Fields["termId"] != "101" {
				t.Fatal("identity lost")
			}
		}
	}
}
func TestIsolationParseUnsupportedCancel(t *testing.T) {
	seen := []string{}
	client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		c, _ := r.Cookie("NTESSTUDYSI")
		seen = append(seen, c.Value)
		return response(`{"code":0}`), nil
	}), Timeout: time.Minute}
	ai, _ := New(contract.Dependencies{HTTPClient: client})
	a := ai.(*adapter)
	for _, token := range []string{"alpha", "beta", "alpha"} {
		s := sessionFor(t, a, scope, token)
		_, e := a.Sync(context.Background(), scope, s, contract.Course, "")
		if !errors.Is(e, contract.ErrParse) {
			t.Fatal(e)
		}
	}
	if strings.Join(seen, ",") != "alpha,beta,alpha" || client.Jar != nil || client.Timeout != time.Minute {
		t.Fatal("client isolation")
	}
	_, e := a.Sync(context.Background(), scope, nil, contract.Resource("unknown"), "")
	if !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, e = a.BeginAuth(ctx, scope)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s := sessionFor(t, a, scope, "alpha")
	c, _ := newCursor(scope, contract.Assignment, "")
	b, _ := json.Marshal(c)
	_, e = a.Sync(context.Background(), scope, s, contract.Exam, base64Cursor(b))
	if !errors.Is(e, contract.ErrInvalid) {
		t.Fatal(e)
	}
	for _, body := range []string{`{"code":0,"result":null}`, `{"result":{}}`, `{"code":0,"result":{}} {}`, `not json`} {
		if _, e := decode([]byte(body)); !errors.Is(e, contract.ErrParse) {
			t.Fatal(e)
		}
	}
}
