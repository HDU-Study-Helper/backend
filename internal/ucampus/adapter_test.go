package ucampus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"academic/backend/internal/contract"
	"github.com/coder/websocket"
)

type reroute struct{ target *url.URL }

func (r reroute) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme = r.target.Scheme
	u.Host = r.target.Host
	clone.URL = &u
	return http.DefaultTransport.RoundTrip(clone)
}
func fixture(t *testing.T, h http.HandlerFunc) (*adapter, func()) {
	t.Helper()
	server := httptest.NewServer(h)
	u, _ := url.Parse(server.URL)
	a, _ := New(contract.Dependencies{HTTPClient: &http.Client{Transport: reroute{u}, Timeout: 5 * time.Second}})
	return a.(*adapter), server.Close
}

var testScope = contract.Scope{UserID: "fixture-user", ConnectionID: "fixture-connection"}

func imported(t *testing.T, a *adapter, name string) contract.Session {
	t.Helper()
	input, _ := json.Marshal(SessionInput{Authorization: name, OpenID: "fixture-open", Annotator: "fixture-annotator", PortalCookies: []*http.Cookie{{Name: "account", Value: name, Path: "/"}}})
	s, err := ImportSessionWithDependencies(context.Background(), testScope, input, contract.Dependencies{HTTPClient: &a.client})
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func dataHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/account/user/info":
		fmt.Fprint(w, `{"code":1,"value":{"userInfo":{"appUserId":"fixture-account","ssoId":"fixture-open"}}}`)
	case strings.Contains(r.URL.Path, "getHomeCourseList"):
		c, _ := r.Cookie("account")
		if c == nil || c.Value != r.Header.Get("Authorization") {
			w.WriteHeader(403)
			return
		}
		fmt.Fprint(w, `{"code":1,"value":{"courseList":[{"id":"course1","classId":"class1","name":"English","courseResourceList":[{"id":"resource1","instanceId":"instance1","name":"Textbook"}]}]}}`)
	case strings.HasSuffix(r.URL.Path, "fixture-open/default"):
		fmt.Fprint(w, `{"code":0,"rt":{"units":{"unit2":{},"unit1":{}}}}`)
	default:
		fmt.Fprint(w, `{"code":0,"rt":{"publish_version":"v1","leafs":{"task1":{"state":{"pass":0,"perm":1},"strategies":{"end_time":1800000000,"required":true},"tab_type":"task","duration":0,"signatureUrl":"SECRET"}}}}`)
	}
}
func TestTaskPagesIsolationAndUpdate(t *testing.T) {
	a, done := fixture(t, dataHandler)
	defer done()
	s1 := imported(t, a, "one")
	s2 := imported(t, a, "two")
	for _, session := range []contract.Session{s1, s2, s1} {
		first, err := a.Sync(context.Background(), testScope, session, contract.Assignment, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Records) != 1 || !first.HasMore || first.NextCursor == "" {
			t.Fatalf("first page: %+v", first)
		}
		second, err := a.Sync(context.Background(), testScope, first.Session, contract.Assignment, first.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		if second.HasMore || len(second.Records) != 1 || first.Records[0].ExternalID == second.Records[0].ExternalID {
			t.Fatal("lost unit compound identity")
		}
		b, _ := json.Marshal(first.Records)
		if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "fixture-annotator") {
			t.Fatal("secret leaked")
		}
		if _, err = a.Sync(context.Background(), testScope, first.Session, contract.Progress, first.NextCursor); !errors.Is(err, contract.ErrInvalid) {
			t.Fatal("cursor resource not checked")
		}
		if _, err = a.Sync(context.Background(), contract.Scope{UserID: "other", ConnectionID: "other"}, session, contract.Course, ""); !errors.Is(err, contract.ErrAuthRequired) {
			t.Fatal("scope isolation failed")
		}
	}
	// Source status updates retain identity and are visible on a fresh full scan.
	version := 0
	b, closeB := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "default/") {
			fmt.Fprintf(w, `{"code":0,"rt":{"publish_version":"v%d","leafs":{"task1":{"state":{"pass":%d}}}}}`, version, version)
			return
		}
		dataHandler(w, r)
	})
	defer closeB()
	initial, err := b.Sync(context.Background(), testScope, s1, contract.Progress, "")
	if err != nil {
		t.Fatal(err)
	}
	version = 1
	updated, err := b.Sync(context.Background(), testScope, s1, contract.Progress, "")
	if err != nil {
		t.Fatal(err)
	}
	if initial.Records[0].ExternalID != updated.Records[0].ExternalID || initial.Records[0].SourceVersion == updated.Records[0].SourceVersion {
		t.Fatal("update not observed")
	}
}
func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{{"expired", 401, "", contract.ErrAuthRequired}, {"limited", 429, "", contract.ErrRateLimited}, {"parse", 200, `{"code":1,"value":{}}`, contract.ErrParse}, {"upstream", 503, "", contract.ErrUpstream}} {
		t.Run(tc.name, func(t *testing.T) {
			a, done := fixture(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			defer done()
			s, _ := newSession(testScope, SessionInput{Authorization: "fixture"})
			p, err := a.Sync(context.Background(), testScope, s, contract.Course, "")
			if !errors.Is(err, tc.want) || p.NextCursor != "" {
				t.Fatalf("%+v %v", p, err)
			}
		})
	}
	a, _ := New(contract.Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Sync(ctx, testScope, nil, contract.Course, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := a.Sync(context.Background(), testScope, nil, contract.Exam, ""); !errors.Is(err, contract.ErrUnsupported) {
		t.Fatal(err)
	}
}
func TestQRStates(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   string
	}{{"7202", "scanned"}, {"7201", "expired"}, {"7205", "expired"}, {"login", "authorized"}} {
		t.Run(tc.status, func(t *testing.T) {
			a, done := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/sso/7.0/sso/code":
					var p map[string]string
					json.NewDecoder(r.Body).Decode(&p)
					json.NewEncoder(w).Encode(map[string]any{"code": "0", "rs": map[string]string{"content": sso + "/sso/mobile-default.html?target=" + p["deviceId"] + "@" + portal + "@123@ssoScanLogin"}})
				case "/sso/serviceTicket/validate":
					fmt.Fprint(w, `{"code":"0","rs":{"jwt":"fixture-jwt"}}`)
				case "/api/account/user/info":
					dataHandler(w, r)
				case "/umcs/":
					ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
					if err != nil {
						return
					}
					defer ws.CloseNow()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					ws.Write(ctx, websocket.MessageText, []byte(`0{"sid":"fixture-sid"}`))
					ws.Read(ctx)
					ws.Write(ctx, websocket.MessageText, []byte(`40/sso,{"sid":"namespace"}`))
					packet := `42/sso,["msg_sso",{"eventType":"code","status":"` + tc.status + `"}]`
					if tc.status == "login" {
						packet = `42/sso,["msg_sso",{"eventType":"login","ticket":"fixture-ticket","openId":"fixture-open"}]`
					}
					ws.Write(ctx, websocket.MessageText, []byte(packet))
					ws.Read(ctx)
				}
			})
			defer done()
			defer a.Close(context.Background())
			auth, err := a.BeginAuth(context.Background(), testScope)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(auth.QRURL, sso+"/sso/mobile-default.html?") {
				t.Fatal("not official scan URI")
			}
			auth, err = a.PollAuth(context.Background(), testScope, auth.Session)
			if err != nil {
				t.Fatal(err)
			}
			auth, err = a.PollAuth(context.Background(), testScope, auth.Session)
			if err != nil || auth.Status != tc.want {
				t.Fatalf("%+v %v", auth, err)
			}
			if tc.want == "expired" && auth.QRURL != "" {
				t.Fatal("expired QR exposed")
			}
			if _, err = a.PollAuth(context.Background(), contract.Scope{UserID: "wrong", ConnectionID: "wrong"}, auth.Session); !errors.Is(err, contract.ErrInvalid) {
				t.Fatal("QR scope not checked")
			}
		})
	}
}
func TestAnonymousOfficialHandshake(t *testing.T) {
	if os.Getenv("UCAMPUS_PUBLIC_PROBE") != "1" {
		t.Skip("opt-in anonymous public QR and WebSocket handshake only; never scan")
	}
	a, _ := New(contract.Dependencies{})
	defer a.(*adapter).Close(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := a.BeginAuth(ctx, testScope)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" || result.QRURL == "" {
		t.Fatal("missing official QR")
	}
	result, err = a.PollAuth(ctx, testScope, result.Session)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "pending" {
		t.Fatal("unexpected non-anonymous status")
	}
	t.Log("Official anonymous QR, Engine.IO WebSocket handshake and Socket.IO namespace acknowledgement verified; no account or scan used")
}
