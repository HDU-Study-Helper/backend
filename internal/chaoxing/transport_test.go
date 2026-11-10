package chaoxing

import (
	"academic/backend/internal/contract"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTransientRoutingCookieNotReplayedOrPersisted(t *testing.T) {
	a := fixtureAdapter(t, func(http.ResponseWriter, *http.Request) {})
	raw, _ := json.Marshal(state{Version: 1, Scope: testScope, Authorized: true, Cookies: []cookieEntry{
		{Origin: middle, Cookie: http.Cookie{Name: "k8s", Value: "stale-ticket", Domain: ".chaoxing.com", Path: "/"}},
		{Origin: middle, Cookie: http.Cookie{Name: "_uid", Value: "fixture-user", Domain: ".chaoxing.com", Path: "/"}},
		{Origin: middle, Cookie: http.Cookie{Name: "cx_p_token", Value: "fixture-auth", Domain: ".chaoxing.com", Path: "/"}},
	}})
	s, _, j, e := a.restore(testScope, raw)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(middle)
	cookies := j.Cookies(u)
	if len(cookies) != 2 {
		t.Fatal("login cookies lost or stale routing cookie replayed")
	}
	for _, c := range cookies {
		if c.Name == "k8s" {
			t.Fatal("stale ticket replayed")
		}
	}
	j.SetCookies(u, []*http.Cookie{{Name: "k8s", Value: "fresh-ticket", Path: "/"}})
	found := false
	for _, c := range j.Cookies(u) {
		if c.Name == "k8s" && c.Value == "fresh-ticket" {
			found = true
		}
	}
	if !found {
		t.Fatal("live routing cookie discarded")
	}
	persisted := save(s, j)
	if strings.Contains(string(persisted), "fresh-ticket") || strings.Contains(string(persisted), "stale-ticket") {
		t.Fatal("routing ticket persisted")
	}
	var restored state
	if json.Unmarshal(persisted, &restored) != nil || !restored.Authorized || len(restored.Cookies) != 2 {
		t.Fatal("login session changed")
	}
}
func TestVerificationPageIsExplicitFailure(t *testing.T) {
	for _, mode := range []string{"redirect202", "body200"} {
		t.Run(mode, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect202" && r.URL.Path != "/antispiderShowVerify.ac" {
					http.Redirect(w, r, "https://mooc1.chaoxing.com/antispiderShowVerify.ac", 302)
					return
				}
				if mode == "redirect202" {
					w.WriteHeader(202)
				}
				fmt.Fprint(w, `<!doctype html><title>提示页面</title>操作异常，请输入图片中的验证码`)
			})
			_, c, _, _ := a.restore(testScope, authorized(testScope))
			if _, e := request(context.Background(), c, "GET", middle, nil); !errors.Is(e, contract.ErrVerificationRequired) {
				t.Fatal("verification not recognized", e)
			}
		})
	}
	if verificationPage([]byte(strings.Repeat(" ", 9000) + "操作异常，请输入图片中的验证码")) {
		t.Fatal("course content mistaken for verification document")
	}
	if verificationPage([]byte("课程内容介绍验证码的作用")) {
		t.Fatal("ordinary mention mistaken for verification")
	}
}
func TestPacingAndCancellation(t *testing.T) {
	var calls atomic.Int32
	p := newPacedTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}), 100*time.Millisecond)
	r, _ := http.NewRequest("GET", middle, nil)
	resp, e := p.RoundTrip(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, e := p.RoundTrip(r.WithContext(ctx)); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("cancelled request reached upstream")
	}
	started := time.Now()
	resp, e = p.RoundTrip(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if time.Since(started) < 70*time.Millisecond || calls.Load() != 2 {
		t.Fatal("requests not spaced")
	}
}
