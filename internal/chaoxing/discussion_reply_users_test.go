package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func replyFixture(t *testing.T, topics func(http.ResponseWriter, *http.Request), replies func(http.ResponseWriter, *http.Request)) *adapter {
	t.Helper()
	return fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(1))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, discussionMiddleFixture)
		case "/course/topic/topicList":
			http.SetCookie(w, &http.Cookie{Name: "reply-cookie", Value: "private", Path: "/", Secure: true})
			fmt.Fprint(w, `<input id=bbsid value=reply-bbs>`)
		case "/course/topic/reply-bbs/getTopicList":
			topics(w, r)
		case "/pc/invitation/getReplyList":
			if _, err := r.Cookie("reply-cookie"); err != nil {
				t.Error("private jar lost")
			}
			if r.Method != http.MethodGet || r.URL.Query().Get("bbsid") != "reply-bbs" || r.URL.Query().Get("uuid") != "topic" || r.URL.Query().Get("order") != "1" {
				t.Error("reply query")
			}
			replies(w, r)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
}
func ownedTopic(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"topic","content":"TOPIC BODY"}],"poff":{"lastPage":true}}`)
}

func TestDiscussionReplyUsersPaginationPrivacy(t *testing.T) {
	lists, replies := 0, 0
	a := replyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		lists++
		if lists == 1 {
			fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"next","lastAuxValue":"aux"}}`)
			return
		}
		if r.URL.Query().Get("last_reply_time") != "next" {
			t.Error("topic continuation")
		}
		ownedTopic(w, r)
	}, func(w http.ResponseWriter, r *http.Request) {
		replies++
		if replies == 1 {
			if r.URL.Query().Get("lastValue") != "" {
				t.Error("initial lastValue")
			}
			fmt.Fprint(w, `{"status":true,"datas":[{"id":101,"puid":12,"createrName":"张三","content":"PRIVATE_BODY","images":["PRIVATE_IMAGE"],"files":["PRIVATE_FILE"],"phone":"PRIVATE_PHONE","studentNo":"PRIVATE_STUDENT","secretURLs":"PRIVATE_URL","create_time":1794139200000,"subreply":[{"id":102,"createrName":"Nested"}]}],"poff":{"lastPage":false,"lastValue":101}}`)
		} else {
			if r.URL.Query().Get("lastValue") != "101" {
				t.Error("reply continuation")
			}
			fmt.Fprint(w, `{"status":true,"datas":[{"id":103,"puid":99,"createrName":"HIDDEN_REAL_NAME","replyName":"匿名同学","anonymous":1}],"poff":{"lastPage":true}}`)
		}
	})
	p, err := a.DiscussionReplyUsers(context.Background(), testScope, authorized(testScope), "c0", "k0", "topic")
	if err != nil || len(p.Records) != 2 || p.HasMore || p.NextCursor != "" || len(p.Session) == 0 || lists != 2 || replies != 2 {
		t.Fatalf("result %+v err=%v", p, err)
	}
	row := p.Records[0]
	if row.ExternalID != "101" || row.ParentID != "topic" || row.Kind != discussionReplyUserResource || row.Title != "张三" || row.Content != "" || row.Fields["user_id"] != "12" || row.Fields["coverage"] != "top_level_only" {
		t.Fatalf("record %+v", row)
	}
	if row.Fields["published_at_raw"] != "1794139200000" || row.Fields["participation_status"] != "reply_observed" || row.Fields["current_user_match"] != "unknown" {
		t.Fatal(row.Fields)
	}
	if _, ok := p.Records[1].Fields["user_id"]; ok {
		t.Fatal("anonymous identity retained")
	}
	if p.Records[1].Title != "匿名同学" || p.Records[1].Fields["anonymous"] != true {
		t.Fatal("anonymous alias lost")
	}
	wire, _ := json.Marshal(p.Records)
	for _, bad := range []string{"PRIVATE_", "HIDDEN_REAL_NAME", "Nested", "content", "images", "files", "phone", "studentNo", "secretURLs"} {
		if strings.Contains(string(wire), bad) {
			t.Fatalf("leaked %s", bad)
		}
	}
}

func TestDiscussionReplyUsersFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"missing-pagination", `{"status":true,"datas":[]}`, 200, contract.ErrParse},
		{"missing-end", `{"status":true,"datas":[],"poff":{}}`, 200, contract.ErrParse},
		{"short-more-no-cursor", `{"status":true,"datas":[],"poff":{"lastPage":false}}`, 200, contract.ErrParse},
		{"null-data", `{"status":true,"datas":null,"poff":{"lastPage":true}}`, 200, contract.ErrParse},
		{"status-false", `{"status":false,"datas":[],"poff":{"lastPage":true}}`, 200, contract.ErrParse},
		{"bad-id", `{"status":true,"datas":[{"uuid":"not-reply-id"}],"poff":{"lastPage":true}}`, 200, contract.ErrParse},
		{"bad-name", `{"status":true,"datas":[{"id":1,"createrName":{}}],"poff":{"lastPage":true}}`, 200, contract.ErrParse},
		{"bad-anonymous", `{"status":true,"datas":[{"id":1,"anonymous":null}],"poff":{"lastPage":true}}`, 200, contract.ErrParse},
		{"cursor-loop", `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"same"}}`, 200, contract.ErrParse},
		{"duplicate-id", `{"status":true,"datas":[{"id":1},{"id":1}],"poff":{"lastPage":true}}`, 200, contract.ErrParse},
		{"rate-limit", "", 429, contract.ErrRateLimited},
		{"permission", "", 403, contract.ErrAuthRequired},
		{"upstream", "", 500, contract.ErrUpstream},
		{"login", `<form id="loginForm">`, 200, contract.ErrAuthRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := replyFixture(t, ownedTopic, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) })
			p, err := a.DiscussionReplyUsers(context.Background(), testScope, authorized(testScope), "c0", "k0", "topic")
			if !errors.Is(err, tc.want) || len(p.Records) != 0 || p.NextCursor != "" || len(p.Session) != 0 {
				t.Fatalf("failure returned %+v %v", p, err)
			}
		})
	}
}

func TestDiscussionReplyUsersOwnershipAndBudgets(t *testing.T) {
	for _, mode := range []string{"wrong-course", "wrong-class", "absent-topic", "topic-loop", "topic-budget", "reply-pages", "reply-count", "cross-scope", "unauthorized", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			topics, replies := 0, 0
			a := replyFixture(t, func(w http.ResponseWriter, r *http.Request) {
				topics++
				switch mode {
				case "absent-topic":
					fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":true}}`)
				case "topic-loop":
					fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"same","lastAuxValue":"same"}}`)
				case "topic-budget":
					fmt.Fprintf(w, `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"%d","lastAuxValue":""}}`, topics)
				default:
					ownedTopic(w, r)
				}
			}, func(w http.ResponseWriter, r *http.Request) {
				replies++
				if mode == "reply-count" {
					rows := make([]string, 251)
					for i := range rows {
						rows[i] = fmt.Sprintf(`{"id":"%d-%d"}`, replies, i)
					}
					fmt.Fprintf(w, `{"status":true,"datas":[%s],"poff":{"lastPage":false,"lastValue":"%d"}}`, strings.Join(rows, ","), replies)
				} else {
					fmt.Fprintf(w, `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"%d"}}`, replies)
				}
			})
			courseID, classID := "c0", "k0"
			session := authorized(testScope)
			ctx := context.Background()
			if mode == "wrong-course" {
				courseID = "other"
			}
			if mode == "wrong-class" {
				classID = "other"
			}
			if mode == "cross-scope" {
				session = authorized(contract.Scope{UserID: "other", ConnectionID: "other"})
			}
			if mode == "unauthorized" {
				session = nil
			}
			if mode == "cancel" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			p, err := a.DiscussionReplyUsers(ctx, testScope, session, courseID, classID, "topic")
			if err == nil || len(p.Records) != 0 || len(p.Session) != 0 {
				t.Fatalf("accepted %s: %+v %v", mode, p, err)
			}
			if mode == "topic-budget" && topics != 20 {
				t.Fatalf("topic requests %d", topics)
			}
			if mode == "reply-pages" && replies != 25 {
				t.Fatalf("reply requests %d", replies)
			}
			if mode != "reply-pages" && mode != "reply-count" && replies != 0 {
				t.Fatal("unverified topic read replies")
			}
		})
	}
}

func TestReplyCurrentIdentityConflictsFailClosed(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse("https://groupweb.chaoxing.com")
	c := &http.Client{Jar: jar}
	jar.SetCookies(u, []*http.Cookie{{Name: "_uid", Value: "12", Path: "/"}})
	if replyCurrentIdentity(c) != "12" {
		t.Fatal("identity not read")
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "UID", Value: "99", Path: "/"}})
	if replyCurrentIdentity(c) != "" {
		t.Fatal("conflicting identity accepted")
	}
}

func TestDiscussionReplyUsersEmptyMoreAndAnonymous(t *testing.T) {
	calls := 0
	a := replyFixture(t, ownedTopic, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":0,"lastValue":"advance"}}`)
		} else {
			fmt.Fprint(w, `{"status":true,"datas":[{"id":"r","anonymous":true,"createrName":"Private","puid":"private"}],"poff":{"lastPage":1}}`)
		}
	})
	p, err := a.DiscussionReplyUsers(context.Background(), testScope, authorized(testScope), "c0", "k0", "topic")
	if err != nil || calls != 2 || len(p.Records) != 1 || p.Records[0].Title != "匿名用户" {
		t.Fatal(p, err)
	}
}
