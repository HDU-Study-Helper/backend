package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
)

func TestAuthSuccessPersistsCookiesWithoutProfile(t *testing.T) {
	for _, name := range []string{"cx_p_token", "p_auth_token", "vc3", "_d"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/getauthstatus/v2" {
					t.Error("unexpected profile request")
					http.Error(w, "unavailable", http.StatusBadGateway)
					return
				}
				if r.Header.Get("Accept") != "application/json,*/*" || !strings.Contains(r.UserAgent(), "Chrome/120") || r.Header.Get("Accept-Language") != "zh-CN,zh;q=0.9" {
					t.Error("headers differ from successful research flow")
				}
				http.SetCookie(w, &http.Cookie{Name: "UID", Value: "synthetic-identity", Domain: ".chaoxing.com", Path: "/", Secure: true})
				http.SetCookie(w, &http.Cookie{Name: name, Value: "synthetic-auth", Domain: ".chaoxing.com", Path: "/", Secure: true})
				fmt.Fprint(w, `{"status":false,"data":{"status":true}}`)
			})
			session, _ := json.Marshal(state{Version: 1, Scope: testScope, UUID: "fixture", Enc: "fixture", Expires: time.Now().Add(time.Minute)})
			result, err := a.PollAuth(context.Background(), testScope, session)
			if err != nil || result.Status != "authorized" {
				t.Fatalf("status=%s err=%v", result.Status, err)
			}
			s, _, j, err := a.restore(testScope, result.Session)
			if err != nil || !s.Authorized || s.UUID != "" || s.Enc != "" || !hasIdentity(j) {
				t.Fatal("successful session not persisted")
			}
			again, err := a.PollAuth(context.Background(), testScope, result.Session)
			if err != nil || again.Status != "authorized" || calls != 1 {
				t.Fatal("authorized session repeated upstream login")
			}
		})
	}
}

func TestNestedAuthStatesAndStrictTypes(t *testing.T) {
	for _, tc := range []struct {
		body, status string
		want         error
	}{
		{`{"status":false,"data":{"type":"4"}}`, "scanned", nil},
		{`{"data":{"status":false,"type":3}}`, "pending", nil},
		{`{"status":false,"data":{"status":false,"type":"6"}}`, "expired", nil},
		{`{"status":false,"data":{"status":false,"type":7}}`, "", contract.ErrAuthRequired},
		{`{"data":{"status":true}}`, "", contract.ErrAuthRequired},
		{`{"status":true}`, "", contract.ErrAuthRequired},
		{`{"data":{"status":"true","type":4}}`, "", contract.ErrParse},
		{`{"status":"false","data":{"status":true}}`, "", contract.ErrParse},
		{`{"data":{"status":1,"type":4}}`, "", contract.ErrParse},
		{`{"status":false,"data":{"type":"04"}}`, "", contract.ErrParse},
		{`{"status":false,"data":{"type":true}}`, "", contract.ErrParse},
		{`{"status":false,"data":[]}`, "", contract.ErrParse},
		{`{"status":true,"data":{"containTwoFactorLogin":true}}`, "", contract.ErrUnsupported},
		{`{"status":false,"data":{"status":true,"type":{}}}`, "", contract.ErrParse},
		{`not-json synthetic-private-marker`, "", contract.ErrParse},
	} {
		t.Run(tc.body, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })
			session, _ := json.Marshal(state{Version: 1, Scope: testScope, UUID: "fixture", Enc: "fixture", Expires: time.Now().Add(time.Minute)})
			result, err := a.PollAuth(context.Background(), testScope, session)
			if !errors.Is(err, tc.want) || result.Status != tc.status {
				t.Fatalf("status=%s err=%v", result.Status, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-private-marker") {
				t.Fatal("response leaked into error")
			}
		})
	}
}

func TestAuthRejectsUnusableIdentityCookies(t *testing.T) {
	for _, cookie := range []*http.Cookie{
		{Name: "_uid", Value: "", Path: "/"},
		{Name: "_uid", Value: "synthetic", Path: "/", MaxAge: -1},
		{Name: "_uid", Value: "synthetic", Path: "/", Expires: time.Now().Add(-time.Hour)},
		{Name: "_uid", Value: "synthetic", Path: "/other"},
		{Name: "_uid", Value: "synthetic", Domain: "example.com", Path: "/"},
		{Name: "JSESSIONID", Value: "synthetic", Path: "/"},
	} {
		a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, cookie)
			http.SetCookie(w, &http.Cookie{Name: "cx_p_token", Value: "synthetic-auth", Domain: ".chaoxing.com", Path: "/"})
			fmt.Fprint(w, `{"data":{"status":true}}`)
		})
		session, _ := json.Marshal(state{Version: 1, Scope: testScope, UUID: "fixture", Enc: "fixture", Expires: time.Now().Add(time.Minute)})
		if _, err := a.PollAuth(context.Background(), testScope, session); !errors.Is(err, contract.ErrAuthRequired) {
			t.Fatalf("unusable identity accepted: %v", err)
		}
	}
}
