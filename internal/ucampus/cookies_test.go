package ucampus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"academic/backend/internal/contract"
)

func TestTrackedCookiePaths(t *testing.T) {
	jar := newTrackedJar()
	u, _ := url.Parse(sso + "/sso/7.0/sso/code")
	jar.SetCookies(u, []*http.Cookie{{Name: "auth", Value: "fixture", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}})
	saved := jar.snapshot(sso)
	if len(saved) != 1 || saved[0].Path != "/sso/7.0/sso" || !saved[0].HttpOnly {
		t.Fatal("cookie attributes lost")
	}
	restored := newTrackedJar()
	root, _ := url.Parse(sso)
	restored.SetCookies(root, saved)
	if len(restored.Cookies(u)) != 1 || len(restored.Cookies(root)) != 0 {
		t.Fatal("cookie path scope lost")
	}
}
func TestQRFailureAndLifecycle(t *testing.T) {
	a, done := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) })
	defer done()
	if _, err := a.BeginAuth(context.Background(), testScope); !errors.Is(err, contract.ErrRateLimited) {
		t.Fatal(err)
	}
	b, closeB := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"rs":{"content":"https://evil.example/qr"}}`)
	})
	defer closeB()
	if _, err := b.BeginAuth(context.Background(), testScope); !errors.Is(err, contract.ErrParse) {
		t.Fatal(err)
	}
	s := state{Version: 1, Scope: testScope, Challenge: &qrState{Device: "fixture-device", SID: "fixture-sid", Status: "pending"}}
	raw, _ := json.Marshal(s)
	result, err := a.PollAuth(context.Background(), testScope, raw)
	if err != nil || result.Status != "expired" {
		t.Fatal("restart or expiry must expire challenge")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = a.PollAuth(ctx, testScope, raw); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestQRSessionRequiresLegitimateAnnotator(t *testing.T) {
	a, done := fixture(t, dataHandler)
	defer done()
	session := imported(t, a, "fixture")
	var s state
	json.Unmarshal(session, &s)
	s.Annotator = ""
	session, _ = json.Marshal(s)
	if _, err := a.Sync(context.Background(), testScope, session, contract.Progress, ""); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal("must not pretend QR returns textbook authorization", err)
	}
}
