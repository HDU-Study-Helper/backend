package chaoxing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

const discussionMiddleFixture = `<input id=enc value=private-enc><input id=bbsid value=middle-bbs><input id=bbsUrlSwitch value=true><input id=learnSilverStartTime value=start><input id=learnSilverEndTime value=end><span id=t>1722222222222</span><li dataname=other><a data-url="https://evil.test/topicList"></a></li><li dataname=tl><a data-url="https://groupweb.chaoxing.com/course/topic/topicList?original=keep&amp;enc=original-enc&amp;learnSilverStartTime=original-start">讨论</a></li>`

func TestDiscussionPagesAcrossCoursesAndPrivateSession(t *testing.T) {
	calls, lists := 0, 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(2))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, discussionMiddleFixture)
		case "/course/topic/topicList":
			q := r.URL.Query()
			if q.Get("original") != "keep" || q.Get("enc") != "original-enc" || q.Get("learnSilverStartTime") != "original-start" || q.Get("learnSilverEndTime") != "end" || q.Get("t") != "1722222222222" {
				t.Errorf("navigation query not preserved")
			}
			if lists > 0 {
				if _, err := r.Cookie("discussion-cookie"); err != nil {
					t.Error("scope jar not restored")
				}
			}
			http.SetCookie(w, &http.Cookie{Name: "discussion-cookie", Value: "fixture", Path: "/", Secure: true})
			fmt.Fprint(w, `<script>Discuss.init({circle: {"bbsid":"bootstrap-bbs"},</script>`)
		case "/course/topic/bootstrap-bbs/getTopicList":
			lists++
			if r.Method != http.MethodPost {
				t.Error("list must POST")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			q := r.URL.Query()
			expectedTags := "classId0000001,classIdk0,courseIdc0"
			if lists == 3 {
				expectedTags = "classId0000001,classIdk1,courseIdc1"
			}
			if r.PostForm.Get("tags") != expectedTags || q.Get("pageSize") != "20" || q.Get("learnSilverStartTime") != "original-start" {
				t.Error("missing actual tags or page query")
			}
			if !strings.Contains(r.Header.Get("Referer"), "original=keep") {
				t.Error("missing actual referer")
			}
			if _, err := r.Cookie("discussion-cookie"); err != nil {
				t.Error("list lacks page cookie")
			}
			switch lists {
			case 1:
				if q.Get("page") != "1" || q.Get("last_reply_time") != "" {
					t.Error("first page query")
				}
				fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"topic-1","title":"Discussion","content":"<p>Hello <b>world</b></p>","createrName":"Teacher","reply_count":2,"praise_count":3,"topicType":1,"createTime":"2026-01-01","enc":"private-secret","url":"https://signed.test/?ticket=private-secret"}],"poff":{"lastPage":false,"lastValue":"next-1","lastAuxValue":"aux-1"}}`)
			case 2:
				if q.Get("page") != "2" || q.Get("last_reply_time") != "next-1" || q.Get("lastAuxValue") != "aux-1" {
					t.Error("continuation query")
				}
				fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":1,"lastValue":"final","lastAuxValue":""}}`)
			case 3:
				if q.Get("page") != "1" || q.Get("last_reply_time") != "" || q.Get("lastAuxValue") != "" {
					t.Error("course pagination not reset")
				}
				fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":true}}`)
			default:
				t.Error("unexpected extra list request")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	p, err := a.Sync(context.Background(), testScope, authorized(testScope), discussionResource, "")
	if err != nil || len(p.Records) != 1 || !p.HasMore || calls != 4 {
		t.Fatalf("first page err=%v records=%d calls=%d", err, len(p.Records), calls)
	}
	row := p.Records[0]
	if row.Kind != discussionResource || row.ExternalID != "topic-1" || row.ParentID != `["c0","k0"]` || row.Content != "Hello world" || row.Fields["author"] != "Teacher" || row.Fields["reply_count"] != float64(2) || row.Fields["topic_type"] != float64(1) || row.Fields["createTime"] != "2026-01-01" {
		t.Fatalf("record %+v", row)
	}
	wire, _ := json.Marshal(p.Records)
	if strings.Contains(string(wire), "private-") || strings.Contains(string(wire), "https:") {
		t.Fatal("ticket URL leaked")
	}
	other := testScope
	other.ConnectionID = "different"
	if _, err = a.Sync(context.Background(), other, authorized(other), discussionResource, p.NextCursor); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("cross-connection cursor", err)
	}
	other = testScope
	other.UserID = "different"
	if _, err = a.Sync(context.Background(), other, authorized(other), discussionResource, p.NextCursor); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("cross-user cursor", err)
	}
	if _, err = a.Sync(context.Background(), testScope, p.Session, contract.Notice, p.NextCursor); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("cross-resource cursor", err)
	}
	p2, err := a.Sync(context.Background(), testScope, p.Session, discussionResource, p.NextCursor)
	if err != nil || !p2.HasMore || len(p2.Records) != 0 || p2.NextCursor == p.NextCursor {
		t.Fatal("course transition", err)
	}
	p3, err := a.Sync(context.Background(), testScope, p2.Session, discussionResource, p2.NextCursor)
	if err != nil || p3.HasMore || p3.NextCursor != "" || calls != 10 {
		t.Fatalf("final page err=%v calls=%d", err, calls)
	}
}

func TestDiscussionEmptyMoreAndStrictFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error
		more       bool
	}{
		{"empty-more", `{"status":true,"datas":[],"poff":{"lastPage":0,"lastValue":123,"lastAuxValue":""}}`, nil, true},
		{"status-false", `{"status":false,"datas":[],"poff":{"lastPage":1}}`, contract.ErrParse, false},
		{"status-string", `{"status":"true","datas":[],"poff":{"lastPage":1}}`, contract.ErrParse, false},
		{"missing-pagination", `{"status":true,"datas":[]}`, contract.ErrParse, false},
		{"missing-lastPage", `{"status":true,"datas":[],"poff":{}}`, contract.ErrParse, false},
		{"missing-lastValue", `{"status":true,"datas":[],"poff":{"lastPage":false,"lastAuxValue":""}}`, contract.ErrParse, false},
		{"missing-aux", `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"next"}}`, nil, true},

		{"terminal-missing-datas", `{"status":true,"poff":{"lastPage":1}}`, nil, false},
		{"continuing-missing-datas", `{"status":true,"poff":{"lastPage":0,"lastValue":"next"}}`, contract.ErrParse, false},
		{"null-aux", `{"status":true,"datas":[],"poff":{"lastPage":0,"lastValue":"next","lastAuxValue":null}}`, contract.ErrParse, false},
		{"null-datas", `{"status":true,"datas":null,"poff":{"lastPage":1}}`, contract.ErrParse, false},
		{"invalid-row", `{"status":true,"datas":[{"id":"fallback","title":"x","content":"x"}],"poff":{"lastPage":1}}`, contract.ErrParse, false},
		{"expiry", `<form id="loginForm">`, contract.ErrAuthRequired, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/visit/stucoursemiddle":
					fmt.Fprint(w, discussionMiddleFixture)
				case "/course/topic/topicList":
					fmt.Fprint(w, `<input id=bbsid value=fixture-bbs>`)
				case "/course/topic/fixture-bbs/getTopicList":
					fmt.Fprint(w, tc.body)
				default:
					t.Error("unexpected request")
				}
			})
			v := cursor{Version: 1, Scope: testScope, Resource: discussionResource, Courses: []course{{ID: "c", Class: "k", CPI: "p"}}, Page: 1}
			raw := encode(v)
			p, err := a.Sync(context.Background(), testScope, authorized(testScope), discussionResource, raw)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v want %v", err, tc.want)
			}
			if err != nil && (p.NextCursor != "" || len(p.Records) != 0) {
				t.Fatal("failure returned advanceable data")
			}
			if tc.more && (!p.HasMore || p.NextCursor == "" || p.NextCursor == raw) {
				t.Fatal("empty page did not advance")
			}
		})
	}
}

func TestDiscussionCoursePartitions(t *testing.T) {
	lists := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(2))
		case "/visit/stucoursemiddle":
			if r.URL.Query().Get("courseid") != "c1" || r.URL.Query().Get("clazzid") != "k1" || r.URL.Query().Get("cpi") != "p1" {
				t.Error("wrong course selected")
			}
			fmt.Fprint(w, discussionMiddleFixture)
		case "/course/topic/topicList":
			fmt.Fprint(w, `<input id=bbsid value=course-bbs>`)
		case "/course/topic/course-bbs/getTopicList":
			lists++
			if lists == 1 {
				fmt.Fprint(w, `{"status":true,"datas":[],"poff":{"lastPage":false,"lastValue":"next","lastAuxValue":"aux"}}`)
			} else {
				fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"topic","content":"Body"}],"poff":{"lastPage":true}}`)
			}
		default:
			t.Error("unexpected path")
		}
	})
	session := authorized(testScope)
	courses, err := a.DiscussionCourses(context.Background(), testScope, session)
	if err != nil || len(courses) != 2 || courses[1].CourseID != "c1" || courses[1].ClassID != "k1" {
		t.Fatal("discovery", courses, err)
	}
	if _, err = a.SyncDiscussionCourse(context.Background(), testScope, session, "c9", "k9", ""); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("unowned course accepted", err)
	}
	p, err := a.SyncDiscussionCourse(context.Background(), testScope, session, "c1", "k1", "")
	if err != nil || !p.HasMore || p.NextCursor == "" {
		t.Fatal("first partition page", err)
	}
	if _, err = a.SyncDiscussionCourse(context.Background(), testScope, p.Session, "c0", "k0", p.NextCursor); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("other course cursor accepted", err)
	}
	v, err := decode(p.NextCursor, testScope, discussionResource)
	if err != nil || len(v.Courses) != 1 || v.Courses[0].CPI != "p1" {
		t.Fatal("partition cursor", err)
	}
	v.Courses = append(v.Courses, course{ID: "c0", Class: "k0", CPI: "p0"})
	if _, err = a.SyncDiscussionCourse(context.Background(), testScope, p.Session, "c1", "k1", encode(v)); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("account cursor accepted", err)
	}
	p2, err := a.SyncDiscussionCourse(context.Background(), testScope, p.Session, "c1", "k1", p.NextCursor)
	if err != nil || p2.HasMore || len(p2.Records) != 1 || p2.Records[0].ParentID != `["c1","k1"]` {
		t.Fatal("final partition page", err)
	}
}

func TestDiscussionTopicBoundsAndOptionalTitle(t *testing.T) {
	for _, title := range []string{"", `,"title":null`} {
		rows, _, _, _, err := parseTopics([]byte(`{"status":true,"datas":[{"uuid":"topic","content":"<p>Body</p>"` + title + `}],"poff":{"lastPage":true}}`))
		if err != nil || len(rows) != 1 || rows[0].Title != "" || rows[0].Text != "Body" {
			t.Fatal("optional title", err)
		}
	}
	rows := make([]string, 501)
	for i := range rows {
		rows[i] = fmt.Sprintf(`{"uuid":"topic-%d","title":"T","content":"Body"}`, i)
	}
	if _, _, _, _, err := parseTopics([]byte(`{"status":true,"datas":[` + strings.Join(rows, ",") + `],"poff":{"lastPage":true}}`)); !errors.Is(err, contract.ErrParse) {
		t.Fatal("oversized page accepted", err)
	}
}

func TestDiscussionNavigationWhitelistAndTime(t *testing.T) {
	co := course{ID: "c", Class: "k", CPI: "p"}
	for _, target := range []string{"http://groupweb.chaoxing.com/topicList", "https://chaoxing.com.evil.test/topicList", "https://evilchaoxing.com/topicList", "https://user@groupweb.chaoxing.com/topicList", "https://groupweb.chaoxing.com:444/topicList"} {
		if _, _, err := discussionURL([]byte(`<li dataname=tl><a data-url="`+target+`"></a></li>`), co); !errors.Is(err, contract.ErrParse) {
			t.Fatal("unsafe navigation accepted", target, err)
		}
	}
	u, _, err := discussionURL([]byte(`<input id=t value=123><li dataname=tl><a data-url="https://groupweb.chaoxing.com/topicList?t=original&amp;x=1&amp;x=2#fragment"></a></li>`), co)
	if err != nil || u.Query().Get("t") != "original" || len(u.Query()["x"]) != 2 || u.Fragment != "fragment" {
		t.Fatal("original navigation lost", err)
	}
	u, _, err = discussionURL([]byte(`<input id=t value=123><li dataname=tl><a data-url="https://groupweb.chaoxing.com/topicList"></a></li>`), co)
	if err != nil || u.Query().Get("t") != "123" {
		t.Fatal("hidden t lost", err)
	}
}

func TestDiscussionContinuationWithoutAuxiliaryCursor(t *testing.T) {
	pages := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, discussionMiddleFixture)
		case "/course/topic/topicList":
			fmt.Fprint(w, `<input id=bbsid value=fixture-bbs>`)
		case "/course/topic/fixture-bbs/getTopicList":
			pages++
			if pages == 1 {
				fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"first","content":"First page"}],"poff":{"lastPage":0,"lastValue":123}}`)
			} else {
				if pages != 2 || r.URL.Query().Get("page") != "2" || r.URL.Query().Get("last_reply_time") != "123" || r.URL.Query().Get("lastAuxValue") != "" {
					t.Error("required cursor lost or unexpected page")
				}
				fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"second","content":"Second page"}],"poff":{"lastPage":1}}`)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	initial := encode(cursor{Version: 1, Scope: testScope, Resource: discussionResource, Courses: []course{{ID: "c", Class: "k", CPI: "p"}}, Page: 1})
	first, e := a.Sync(context.Background(), testScope, authorized(testScope), discussionResource, initial)
	if e != nil || !first.HasMore || len(first.Records) != 1 {
		t.Fatal("first page", e)
	}
	second, e := a.Sync(context.Background(), testScope, first.Session, discussionResource, first.NextCursor)
	if e != nil || second.HasMore || second.NextCursor != "" || len(second.Records) != 1 || second.Records[0].ExternalID != "second" {
		t.Fatal("second page", e)
	}
}
