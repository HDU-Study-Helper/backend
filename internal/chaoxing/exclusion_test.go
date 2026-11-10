package chaoxing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func excludedFixtureContext(scope contract.Scope, courses ...course) context.Context {
	refs := make([]string, 0, len(courses))
	for _, co := range courses {
		refs = append(refs, co.key())
	}
	return contract.WithExcludedCourses(context.Background(), scope, refs)
}

// The same names and repeated resource IDs are intentional: only [ID,Class]
// identifies an exclusion, and duplicate tracking must reset between courses.
func TestExcludedCourseAddedAtCurrentPage(t *testing.T) {
	for _, resource := range []contract.Resource{contract.Assignment, contract.Material, discussionResource} {
		t.Run(string(resource), func(t *testing.T) {
			blocked := false
			blockedCalls, listCalls, courseCalls := 0, 0, 0
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				id := q.Get("courseid")
				if id == "" {
					id = q.Get("courseId")
				}
				if r.URL.Path == "/mycourse/backclazzdata" {
					courseCalls++
					fmt.Fprint(w, strings.ReplaceAll(coursesFixture(3), "Course 1", "Course 0"))
					return
				}
				if blocked && id == "c0" {
					blockedCalls++
					t.Errorf("excluded course request: %s", r.URL.Path)
					http.Error(w, "blocked", http.StatusForbidden)
					return
				}
				switch r.URL.Path {
				case "/visit/stucoursemiddle":
					if resource == discussionResource {
						fmt.Fprint(w, discussionMiddleFixture)
					} else {
						fmt.Fprint(w, `<input id=workEnc value=work><input id=enc value=enc>`)
					}
				case "/mooc-ans/mooc2/work/list":
					listCalls++
					if q.Get("pageNum") != "1" {
						t.Errorf("assignment page not reset: %s", q.Get("pageNum"))
					}
					fmt.Fprintf(w, `<div>1/2</div><li data="/work/task?courseId=%s&amp;classId=%s&amp;workId=shared" aria-label="Homework ; 未交"></li>`, id, q.Get("classId"))
				case "/mooc2-ans/coursedata/stu-datalist":
					listCalls++
					if q.Get("pages") != "1" || q.Get("dataId") != "0" || q.Get("enc") != "" {
						t.Errorf("material state not reset: page=%s folder=%s", q.Get("pages"), q.Get("dataId"))
					}
					fmt.Fprint(w, `<input id=totalPages value=2><input id=pages value=1><div class=dataBody><ul class=dataBody_td id=shared dataname=Folder type=afolder enc=private-folder></ul></div>`)
				case "/course/topic/topicList":
					fmt.Fprint(w, `<input id=bbsid value=fixture-bbs>`)
				case "/course/topic/fixture-bbs/getTopicList":
					listCalls++
					if q.Get("page") != "1" || q.Get("last_reply_time") != "" || q.Get("lastAuxValue") != "" {
						t.Error("discussion state not reset")
					}
					fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"shared","title":"Topic","content":"body"}],"poff":{"lastPage":false,"lastValue":"next","lastAuxValue":"aux"}}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			first, err := a.Sync(context.Background(), testScope, authorized(testScope), resource, "")
			if err != nil || !first.HasMore || len(first.Records) != 1 {
				t.Fatalf("first page: err=%v page=%+v", err, first)
			}
			before, err := decode(first.NextCursor, testScope, resource)
			if err != nil || before.Index != 0 || before.Page != 2 {
				t.Fatalf("expected current-course continuation: %+v %v", before, err)
			}
			blocked = true
			ctx := excludedFixtureContext(testScope, course{ID: "c0", Class: "k0"})
			next, err := a.Sync(ctx, testScope, first.Session, resource, first.NextCursor)
			if err != nil || !next.HasMore || len(next.Records) != 1 || next.Records[0].ParentID != `["c1","k1"]` {
				t.Fatalf("excluded continuation: err=%v page=%+v", err, next)
			}
			after, err := decode(next.NextCursor, testScope, resource)
			if err != nil || after.Index != 1 || after.Page != 2 || len(after.Courses) != 3 || after.Courses[0].ID != "c0" {
				t.Fatalf("snapshot/index changed: %+v %v", after, err)
			}
			if blockedCalls != 0 || courseCalls != 1 || listCalls != 2 {
				t.Fatalf("calls: blocked=%d course_list=%d resource_list=%d", blockedCalls, courseCalls, listCalls)
			}
			// A new scan may discover the shared account list, but must skip c0
			// before opening any course endpoint.
			fresh, err := a.Sync(ctx, testScope, next.Session, resource, "")
			if err != nil || len(fresh.Records) != 1 || fresh.Records[0].ParentID != `["c1","k1"]` || blockedCalls != 0 || courseCalls != 2 || listCalls != 3 {
				t.Fatalf("fresh excluded scan: %+v %v blocked=%d course_list=%d resource_list=%d", fresh, err, blockedCalls, courseCalls, listCalls)
			}
			// Removing the policy allows the next complete scan to read c0 again.
			blocked = false
			restarted, err := a.Sync(context.Background(), testScope, next.Session, resource, "")
			if err != nil || len(restarted.Records) != 1 || restarted.Records[0].ParentID != `["c0","k0"]` {
				t.Fatalf("new full scan after removal: %+v %v", restarted, err)
			}
		})
	}
}

func TestExcludedStaleCourseClearsAllPagination(t *testing.T) {
	for _, resource := range []contract.Resource{contract.Course, contract.Assignment, contract.Progress, contract.Material, discussionResource} {
		t.Run(string(resource), func(t *testing.T) {
			stale := course{ID: "stale", Class: "old", Name: "Same name"} // No CPI, absent from account list.
			allowed := course{ID: "allowed", Class: "current", CPI: "p", Name: "Same name"}
			calls := 0
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				q := r.URL.Query()
				id := q.Get("courseid")
				if id == "" {
					id = q.Get("courseId")
				}
				if id != allowed.ID {
					t.Errorf("unexpected/blocked course request %s course=%s", r.URL.Path, id)
				}
				switch r.URL.Path {
				case "/visit/stucoursemiddle":
					if resource == discussionResource {
						fmt.Fprint(w, discussionMiddleFixture)
					} else {
						fmt.Fprint(w, `<input id=workEnc value=work><input id=enc value=enc>`)
					}
				case "/mooc-ans/mooc2/work/list":
					if q.Get("pageNum") != "1" {
						t.Error("stale assignment page survived")
					}
					fmt.Fprint(w, `<div>1/2</div><li data="/work/task?courseId=allowed&amp;classId=current&amp;workId=old-id" aria-label="Work ; 未交"></li>`)
				case "/mooc2-ans/mycourse/studentcourse":
					fmt.Fprint(w, `<div>已完成任务点: 1/2</div>`)
				case "/mooc2-ans/coursedata/stu-datalist":
					if q.Get("pages") != "1" || q.Get("dataId") != "0" || q.Get("enc") != "" {
						t.Error("stale folder/page survived")
					}
					fmt.Fprint(w, `<input id=totalPages value=2><input id=pages value=1><div class=dataBody><ul class=dataBody_td id=old-id dataname=File type=pdf></ul></div>`)
				case "/course/topic/topicList":
					fmt.Fprint(w, `<input id=bbsid value=fixture-bbs>`)
				case "/course/topic/fixture-bbs/getTopicList":
					if q.Get("page") != "1" || q.Get("last_reply_time") != "" || q.Get("lastAuxValue") != "" {
						t.Error("stale discussion cursor survived")
					}
					fmt.Fprint(w, `{"status":true,"datas":[{"uuid":"topic","content":"body"}],"poff":{"lastPage":false,"lastValue":"next"}}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			v := cursor{Version: 1, Scope: testScope, Resource: resource, Courses: []course{stale, allowed}, Page: 7, Seen: 8, Last: "old-last", Fingerprint: "old-fingerprint", WorkIDs: []string{"old-id"}, ExpectedTotal: 999, DiscussionLastValue: "old-topic", DiscussionLastAuxValue: "old-aux", MaterialFolders: []materialFolder{{ID: "old-folder", Enc: "old-enc"}}, MaterialIDs: []string{"old-id"}, MaterialTotalPages: 99}
			page, err := a.Sync(excludedFixtureContext(testScope, stale), testScope, authorized(testScope), resource, encode(v))
			if err != nil || len(page.Records) != 1 {
				t.Fatalf("stale course skip: page=%+v err=%v", page, err)
			}
			if resource == contract.Course {
				if page.Records[0].ExternalID != allowed.key() || calls != 0 {
					t.Fatal("excluded course emitted or cursor triggered I/O")
				}
			} else if page.Records[0].ParentID != allowed.key() {
				t.Fatal("wrong course emitted")
			}
			if page.HasMore {
				after, err := decode(page.NextCursor, testScope, resource)
				if err != nil || after.Last != "" || after.Index != 1 || len(after.Courses) != 2 {
					t.Fatalf("bad reset %+v %v", after, err)
				}
				if resource != contract.Assignment && (after.Seen != 0 || after.Fingerprint != "" || len(after.WorkIDs) != 0 || after.ExpectedTotal != 0) {
					t.Fatal("assignment state survived excluded course")
				}
				if resource != contract.Material && (len(after.MaterialFolders) != 0 || len(after.MaterialIDs) != 0 || after.MaterialTotalPages != 0) {
					t.Fatal("material state survived excluded course")
				}
				if resource != discussionResource && (after.DiscussionLastValue != "" || after.DiscussionLastAuxValue != "") {
					t.Fatal("discussion state survived excluded course")
				}
			}
		})
	}
}

func TestExcludedCourseSnapshotIdentityAndScope(t *testing.T) {
	calls := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/mycourse/backclazzdata" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		fmt.Fprint(w, coursesFixture(51))
	})
	first, err := a.Sync(context.Background(), testScope, authorized(testScope), contract.Course, "")
	if err != nil || !first.HasMore {
		t.Fatal(err)
	}
	blocked := course{ID: "c50", Class: "k50"}
	ctx := excludedFixtureContext(testScope, blocked)
	last, err := a.Sync(ctx, testScope, first.Session, contract.Course, first.NextCursor)
	if err != nil || len(last.Records) != 0 || last.HasMore || last.NextCursor != "" || calls != 1 {
		t.Fatalf("course exclusion after page: %+v %v calls=%d", last, err, calls)
	}
	// Only the exact composite ref is excluded, even when IDs or names overlap.
	v := cursor{Version: 1, Scope: testScope, Resource: contract.Course, Page: 1, Courses: []course{{ID: "same", Class: "a", Name: "Shared"}, {ID: "same", Class: "b", Name: "Shared"}, {ID: "other", Class: "a", Name: "Shared"}}}
	ctx = excludedFixtureContext(testScope, v.Courses[0])
	p, err := a.Sync(ctx, testScope, authorized(testScope), contract.Course, encode(v))
	if err != nil || len(p.Records) != 2 || p.Records[0].ExternalID != v.Courses[1].key() || p.Records[1].ExternalID != v.Courses[2].key() {
		t.Fatalf("identity match: %+v %v", p, err)
	}
	for _, mismatch := range []contract.Scope{{UserID: "another", ConnectionID: testScope.ConnectionID}, {UserID: testScope.UserID, ConnectionID: "another"}} {
		p, err := a.Sync(excludedFixtureContext(mismatch, v.Courses...), testScope, authorized(testScope), contract.Course, encode(v))
		if err != nil || len(p.Records) != 3 {
			t.Fatalf("policy scope mismatch affected courses: %+v %v", p, err)
		}
	}
}

func TestExcludedCoursesAllSkippedWithoutRequests(t *testing.T) {
	for _, resource := range []contract.Resource{contract.Course, contract.Assignment, contract.Progress, contract.Material, discussionResource} {
		t.Run(string(resource), func(t *testing.T) {
			calls := 0
			a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				t.Error("excluded snapshot caused network request")
			})
			v := cursor{Version: 1, Scope: testScope, Resource: resource, Page: 4}
			for i := 0; i < 500; i++ {
				v.Courses = append(v.Courses, course{ID: fmt.Sprint(i), Class: "class"})
			}
			p, err := a.Sync(excludedFixtureContext(testScope, v.Courses...), testScope, authorized(testScope), resource, encode(v))
			if err != nil || p.HasMore || p.NextCursor != "" || len(p.Records) != 0 || len(p.Session) == 0 || calls != 0 {
				t.Fatalf("excluded completion: %+v %v calls=%d", p, err, calls)
			}
		})
	}
}

func TestOptionalDiscussionExclusionAndValidation(t *testing.T) {
	calls := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/mycourse/backclazzdata" {
			t.Errorf("blocked-specific call %s", r.URL.Path)
		}
		fmt.Fprint(w, coursesFixture(2))
	})
	co := course{ID: "c0", Class: "k0"}
	ctx := excludedFixtureContext(testScope, co)
	partitions, err := a.DiscussionCourses(ctx, testScope, authorized(testScope))
	if err != nil || len(partitions) != 1 || partitions[0].CourseID != "c1" || calls != 1 {
		t.Fatalf("partitions=%+v err=%v calls=%d", partitions, err, calls)
	}
	v := cursor{Version: 1, Scope: testScope, Resource: discussionResource, Courses: []course{co}, Page: 4, DiscussionLastValue: "old"}
	for _, raw := range []string{"", encode(v)} {
		p, err := a.SyncDiscussionCourse(ctx, testScope, authorized(testScope), co.ID, co.Class, raw)
		if err != nil || len(p.Records) != 0 || p.HasMore || p.NextCursor != "" || len(p.Session) == 0 || calls != 1 {
			t.Fatalf("excluded partition: %+v %v calls=%d", p, err, calls)
		}
	}
	p, err := a.DiscussionReplyUsers(ctx, testScope, authorized(testScope), co.ID, co.Class, "topic")
	if err != nil || len(p.Records) != 0 || p.HasMore || len(p.Session) == 0 || calls != 1 {
		t.Fatalf("excluded reply users: %+v %v calls=%d", p, err, calls)
	}
	if _, err := a.SyncDiscussionCourse(ctx, testScope, authorized(testScope), co.ID, co.Class, "bad-cursor"); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("exclusion bypassed cursor validation", err)
	}
	v.Courses = append(v.Courses, course{ID: "c1", Class: "k1"})
	if _, err := a.SyncDiscussionCourse(ctx, testScope, authorized(testScope), co.ID, co.Class, encode(v)); !errors.Is(err, contract.ErrInvalid) {
		t.Fatal("exclusion accepted account cursor", err)
	}
	for _, invalidSession := range []contract.Session{nil, authorized(contract.Scope{UserID: "other", ConnectionID: "other"})} {
		if _, err := a.SyncDiscussionCourse(ctx, testScope, invalidSession, co.ID, co.Class, ""); !errors.Is(err, contract.ErrAuthRequired) {
			t.Fatal("exclusion bypassed partition auth", err)
		}
		if _, err := a.DiscussionReplyUsers(ctx, testScope, invalidSession, co.ID, co.Class, "topic"); !errors.Is(err, contract.ErrAuthRequired) {
			t.Fatal("exclusion bypassed reply auth", err)
		}
	}
	if calls != 1 {
		t.Fatalf("blocked/invalid optional calls reached transport: %d", calls)
	}
}

func TestExcludedCoursesKeepAccountNoticesAndExams(t *testing.T) {
	calls := 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/pc/notice/getNoticeList":
			fmt.Fprint(w, `{"status":true,"notices":{"lastPage":true,"list":[{"uuid":"notice","title":"Shared course name","content":"body"}]}}`)
		case "/exam-ans/exam/test/examcode/examlist":
			fmt.Fprint(w, `<table><tr class=dataTr examid=exam><td>1</td><td>Shared course name</td><td>2026-01-01</td><td>60</td><td>进行中</td><td>未交</td><td>---</td><td>未设置终端</td><td></td></tr></table>`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	ctx := excludedFixtureContext(testScope, course{ID: "c0", Class: "k0", Name: "Shared course name"})
	for _, resource := range []contract.Resource{contract.Notice, contract.Exam} {
		p, err := a.Sync(ctx, testScope, authorized(testScope), resource, "")
		if err != nil || len(p.Records) != 1 || p.HasMore {
			t.Fatalf("account %s: %+v %v", resource, p, err)
		}
	}
	if calls != 2 {
		t.Fatal("account resources unexpectedly fetched course data", calls)
	}
}
