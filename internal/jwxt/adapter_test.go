package jwxt

import (
	"context"
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

type fixtureTransport struct{ target *url.URL }

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !allowed(r.URL) {
		return nil, errors.New("unexpected origin")
	}
	clone := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = f.target.Scheme
	u.Host = f.target.Host
	clone.URL = &u
	clone.Host = r.URL.Host
	return http.DefaultTransport.RoundTrip(clone)
}

var scope = contract.Scope{UserID: "fixture-owner", ConnectionID: "fixture-connection"}

func fixture(t *testing.T, h http.HandlerFunc) *adapter {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	u, _ := url.Parse(server.URL)
	input := &http.Client{Transport: fixtureTransport{u}}
	a, e := New(contract.Dependencies{HTTPClient: input})
	if e != nil {
		t.Fatal(e)
	}
	if input.Jar != nil || input.CheckRedirect != nil || input.Timeout != 0 {
		t.Fatal("mutated dependency")
	}
	return a.(*adapter)
}
func fixtureSession(t *testing.T, a *adapter) contract.Session {
	t.Helper()
	_, j, s, e := a.clientFor(scope, nil)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(menu)
	j.SetCookies(u, []*http.Cookie{{Name: "JSESSIONID", Value: "PRIVATE_COOKIE", Path: "/", MaxAge: 600, Secure: true}})
	return saveSession(s, j)
}
func TestPasswordLoginSSOAndScopedSession(t *testing.T) {
	posts := 0
	a := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.URL.Query().Get("service") != "http://newjw.hdu.edu.cn/sso/driot4login" {
				t.Error("service changed")
			}
			if r.Method == http.MethodGet {
				http.SetCookie(w, &http.Cookie{Name: "SESSION", Value: "challenge-cookie", Path: "/", Secure: true})
				fmt.Fprint(w, `<p id=login-page-flowkey>fixture-flow</p><p id=login-croypto>MDEyMzQ1Njc4OWFiY2RlZg==</p>`)
			} else {
				posts++
				r.ParseForm()
				if r.Form.Get("username") != "student" || r.Form.Get("execution") != "fixture-flow" || r.Form.Get("type") != "UsernamePassword" {
					t.Error("invalid login form")
				}
				if _, e := r.Cookie("SESSION"); e != nil {
					t.Error("challenge cookie lost")
				}
				b, e := base64.StdEncoding.DecodeString(r.Form.Get("password"))
				if e != nil || len(b) == 0 || len(b)%16 != 0 {
					t.Fatal("invalid encrypted password")
				}
				block, _ := aes.NewCipher([]byte("0123456789abcdef"))
				for i := 0; i < len(b); i += 16 {
					block.Decrypt(b[i:i+16], b[i:i+16])
				}
				pad := int(b[len(b)-1])
				if string(b[:len(b)-pad]) != "fixture-password" {
					t.Error("password encryption mismatch")
				}
				http.Redirect(w, r, "http://newjw.hdu.edu.cn/sso/driot4login?ticket=PRIVATE_TICKET", 302)
			}
		case "/sso/driot4login":
			http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "PRIVATE_COOKIE", Path: "/", Secure: true, MaxAge: 600})
			http.Redirect(w, r, "http://newjw.hdu.edu.cn/sso/driot4login;jsessionid=PRIVATE_SESSION", 302)
		case "/sso/driot4login;jsessionid=PRIVATE_SESSION":
			http.Redirect(w, r, "http://newjw.hdu.edu.cn/jwglxt/ticketlogin?uid=PRIVATE_UID&verify=PRIVATE_VERIFY", 302)
		case "/jwglxt/ticketlogin":
			http.Redirect(w, r, "https://newjw.hdu.edu.cn/jwglxt/xtgl/login_slogin.html", 302)
		case "/jwglxt/xtgl/login_slogin.html":
			http.Redirect(w, r, menu, 302)
		case "/jwglxt/xtgl/index_initMenu.html":
			if _, e := r.Cookie("JSESSIONID"); e != nil {
				t.Error("JW cookie lost")
			}
			fmt.Fprint(w, "杭州电子科技大学本科教学管理服务平台")
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	result, e := a.Login(context.Background(), scope, "student", "fixture-password")
	if e != nil || result.Status != "authorized" || posts != 1 {
		t.Fatal("login failed", e, posts)
	}
	if strings.Contains(string(result.Session), "fixture-password") || strings.Contains(string(result.Session), "PRIVATE_TICKET") || strings.Contains(string(result.Session), "PRIVATE_VERIFY") {
		t.Fatal("password or one-time ticket retained")
	}
	wire, _ := json.Marshal(result)
	if strings.Contains(string(wire), "PRIVATE_") {
		t.Fatal("session leaked to HTTP")
	}
	_, j, s, e := a.clientFor(scope, result.Session)
	if e != nil || s.Username != "student" {
		t.Fatal("session restore failed", e)
	}
	u, _ := url.Parse(menu)
	if len(j.Cookies(u)) != 1 {
		t.Fatal("cookie not restored")
	}
	other := scope
	other.UserID = "other"
	if _, _, _, e = a.clientFor(other, result.Session); !errors.Is(e, contract.ErrAuthRequired) {
		t.Fatal("cross-user session accepted")
	}
	other = scope
	other.ConnectionID = "other"
	if _, _, _, e = a.clientFor(other, result.Session); !errors.Is(e, contract.ErrAuthRequired) {
		t.Fatal("cross-connection session accepted")
	}
}
func TestLoginFailureDoesNotRetryOrReplayPassword(t *testing.T) {
	for _, mode := range []string{"bad_password", "external_redirect", "replay_redirect", "missing_cookie", "missing_tokens"} {
		t.Run(mode, func(t *testing.T) {
			posts := 0
			a := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts++
					switch mode {
					case "external_redirect":
						http.Redirect(w, r, "https://evil.test/steal", 302)
					case "replay_redirect":
						http.Redirect(w, r, "https://newjw.hdu.edu.cn/replay", 307)
					case "missing_cookie":
						http.Redirect(w, r, menu, 302)
					default:
						fmt.Fprint(w, `<p id=login-page-flowkey>retry</p>`)
					}
					return
				}
				if r.URL.Path == "/login" {
					if mode == "missing_tokens" {
						fmt.Fprint(w, "captcha required")
					} else {
						fmt.Fprint(w, `<p id=login-page-flowkey>flow</p><p id=login-croypto>MDEyMzQ1Njc4OWFiY2RlZg==</p>`)
					}
					return
				}
				fmt.Fprint(w, "杭州电子科技大学本科教学管理服务平台")
			})
			result, e := a.Login(context.Background(), scope, "student", "password")
			if e == nil || len(result.Session) > 0 || posts > 1 {
				t.Fatal("failed authentication accepted or retried", e, posts)
			}
		})
	}
}
func TestTermReadsAndMetadataProjection(t *testing.T) {
	requests := 0
	a := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		r.ParseForm()
		if r.Method != http.MethodPost {
			t.Error("expected read-only POST")
		}
		if _, e := r.Cookie("JSESSIONID"); e != nil {
			t.Error("session missing")
		}
		switch r.URL.Path {
		case "/jwglxt/kbcx/xskbcx_cxXsKb.html":
			if r.Form.Get("xnm") != "2026" || r.Form.Get("xqm") != "12" {
				t.Error("semester mapping failed")
			}
			fmt.Fprint(w, `{"xsxx":{"XH":"PRIVATE_STUDENT"},"kbList":[{"kch_id":"course","jxb_id":"class","kcmc":"数学","xm":"教师","xqj":"2","jc":"3-4节","zcd":"1-16周(单)","cdmc":"教室","secret":"PRIVATE_TICKET"}],"sjkList":[{"qtkcgs":"实践周"}]}`)
		case "/jwglxt/xsxk/zzxkyzb_cxZzxkYzbChoosedDisplay.html":
			if r.Form.Get("xkxnm") != "2026" || r.Form.Get("xkxqm") != "3" {
				t.Error("selection semester mapping failed")
			}
			fmt.Fprint(w, `[{"kch":"course","jxb_id":"class","kcmc":"数学","jsxx":"教师原文","xf":"3.5","do_jxb_id":"PRIVATE_OPERATION","secret":"PRIVATE_VALUE"}]`)
		default:
			t.Error("unexpected read endpoint")
		}
	})
	session := fixtureSession(t, a)
	p, e := a.SyncTerm(context.Background(), scope, session, contract.Schedule, 2026, 2, "")
	if e != nil || len(p.Records) != 2 || p.HasMore {
		t.Fatal("schedule read failed", e)
	}
	f := p.Records[0].Fields
	if f["periods_raw"] != "3-4节" || f["weeks_raw"] != "1-16周(单)" || f["weekday"] != 2 || p.Records[1].Fields["entry_type"] != "practice" {
		t.Fatal("schedule metadata lost", f)
	}
	c, e := a.SyncTerm(context.Background(), scope, p.Session, contract.Course, 2026, 1, "")
	if e != nil || len(c.Records) != 1 || c.Records[0].ExternalID != `["course","class"]` || c.Records[0].ParentID != `["2026","1"]` {
		t.Fatal("course read failed", e)
	}
	wire, _ := json.Marshal(append(p.Records, c.Records...))
	if strings.Contains(string(wire), "PRIVATE_") {
		t.Fatal("unknown source fields escaped")
	}
	if _, e = a.Sync(context.Background(), scope, session, contract.Schedule, ""); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("missing semester accepted")
	}
	if _, e = a.SyncTerm(context.Background(), scope, session, contract.Course, 2026, 3, ""); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("invalid semester accepted")
	}
	if requests != 2 {
		t.Fatal("unexpected network request count", requests)
	}
}
func TestReadPaginationAndCursorBinding(t *testing.T) {
	requests := 0
	a := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		rows := make([]map[string]string, 101)
		for i := range rows {
			rows[i] = map[string]string{"kch": "course", "jxb_id": strconv.Itoa(i), "kcmc": "课程"}
		}
		json.NewEncoder(w).Encode(rows)
	})
	session := fixtureSession(t, a)
	p, e := a.SyncTerm(context.Background(), scope, session, contract.Course, 2026, 1, "")
	if e != nil || !p.HasMore || len(p.Records) != 100 {
		t.Fatal("first page", e)
	}
	if _, e = a.SyncTerm(context.Background(), scope, session, contract.Course, 2026, 2, p.NextCursor); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("cross-term cursor accepted")
	}
	if _, e = a.SyncTerm(context.Background(), scope, session, contract.Schedule, 2026, 1, p.NextCursor); !errors.Is(e, contract.ErrInvalid) {
		t.Fatal("cross-resource cursor accepted")
	}
	next, e := a.SyncTerm(context.Background(), scope, p.Session, contract.Course, 2026, 1, p.NextCursor)
	if e != nil || len(next.Records) != 1 || next.HasMore || next.NextCursor != "" || requests != 1 {
		t.Fatal("local continuation failed", e)
	}
}
func TestUnrecognizedReadsAndExpiry(t *testing.T) {
	for _, body := range []string{`null`, ` null `, `{"items":[]}`, `[{"kcmc":"missing identity"}]`, `[{"kch":"c","jxb_id":"x","kcmc":"A"},{"kch":"c","jxb_id":"x","kcmc":"A"}]`} {
		if _, e := parseRecords([]byte(body), contract.Course, 2026, 1); !errors.Is(e, contract.ErrParse) {
			t.Fatal("invalid selected-course response accepted")
		}
	}
	for _, body := range []string{`{}`, `{"kbList":null}`, `{"kbList":[{"kcmc":"A","xqj":9,"jc":"1-2","zcd":"1-16"}]}`} {
		if _, e := parseRecords([]byte(body), contract.Schedule, 2026, 1); !errors.Is(e, contract.ErrParse) {
			t.Fatal("invalid schedule response accepted")
		}
	}
	for r, body := range map[contract.Resource]string{contract.Schedule: `{"kbList":[],"sjkList":[]}`, contract.Course: `[]`} {
		if rows, e := parseRecords([]byte(body), r, 2026, 1); e != nil || len(rows) != 0 {
			t.Fatal("explicit empty data rejected", e)
		}
	}
	for _, status := range []int{302, 401, 403, 429, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			a := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })
			p, e := a.SyncTerm(context.Background(), scope, fixtureSession(t, a), contract.Course, 2026, 1, "")
			if e == nil || len(p.Records) > 0 || len(p.Session) > 0 {
				t.Fatal("failed request returned committable data")
			}
		})
	}
	a := fixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("expired or cancelled session made request") })
	s := sessionState{Version: 1, Scope: scope, Cookies: []cookieEntry{{Origin: base, Cookie: http.Cookie{Name: "JSESSIONID", Value: "expired", Path: "/", Expires: time.Now().Add(-time.Minute)}}}}
	b, _ := json.Marshal(s)
	if _, e := a.SyncTerm(context.Background(), scope, b, contract.Course, 2026, 1, ""); !errors.Is(e, contract.ErrAuthRequired) {
		t.Fatal("expired session accepted", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := a.SyncTerm(ctx, scope, fixtureSession(t, a), contract.Course, 2026, 1, ""); !errors.Is(e, context.Canceled) {
		t.Fatal("cancel not propagated", e)
	}
}
