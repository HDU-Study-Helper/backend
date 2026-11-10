package chaoxing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestAssignmentSafeRoutesAndIdentity(t *testing.T) {
	origin, _ := url.Parse("https://mooc1.chaoxing.com")
	co := course{ID: "c", Class: "k"}
	for _, target := range []string{"https://evil.test" + assignmentTaskPath + "?courseId=c&classId=k&workId=w", assignmentTaskPath + "?courseId=c&classId=k&workId=other", assignmentTaskPath + "?courseId=c&courseId=c&classId=k&workId=w", assignmentTaskPath + "?courseId=c&classId=k&workId=w&action=start", "/work/task?courseId=c&classId=k&workId=w", "/mooc-ans/mooc2/work/dowork?courseId=c&classId=k&workId=w", "javascript:alert(1)", "https://mooc1.chaoxing.com:444" + assignmentTaskPath + "?courseId=c&classId=k&workId=w"} {
		if _, ok := safeAssignmentURL(target, origin, co, "w"); ok {
			t.Fatal("accepted route", target)
		}
	}
}
func TestAssignmentHeaderBoundedPaginationAndPrivateURLs(t *testing.T) {
	headers, lists := 0, 0
	a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mycourse/backclazzdata":
			fmt.Fprint(w, coursesFixture(1))
		case "/visit/stucoursemiddle":
			fmt.Fprint(w, `<input id=enc value=x><input id=workEnc value=y>`)
		case "/mooc-ans/mooc2/work/list":
			lists++
			fmt.Fprint(w, `<div>1/7</div>`)
			for i := 0; i < 7; i++ {
				fmt.Fprintf(w, `<li data="%s?courseId=c0&amp;classId=k0&amp;workId=w%d&amp;enc=PRIVATE_TOKEN" aria-label="2026 Homework;已完成"></li>`, assignmentTaskPath, i)
			}
		case assignmentTaskPath:
			headers++
			http.Redirect(w, r, assignmentViewPath+"?"+r.URL.RawQuery, http.StatusFound)
		case assignmentViewPath:
			fmt.Fprint(w, `<div class=detailsHead><h1>2026 Homework</h1><div class="infoHead fl"><span>题量:10</span><span>满分:100</span><div><p class=rightBord>作答时间:11-04 09:30 至 11-08 23:59</p></div></div></div><div class=question>PRIVATE_QUESTION</div>`)
		default:
			t.Fatal(r.URL.Path)
		}
	})
	p, err := a.Sync(context.Background(), testScope, authorized(testScope), contract.Assignment, "")
	if err != nil || len(p.Records) != 5 || !p.HasMore || headers != 5 {
		t.Fatal(len(p.Records), headers, err)
	}
	if p.Records[0].Fields["deadline_raw"] != "11-08 23:59" || p.Records[0].Fields["deadline_reason"] != "missing_year" {
		t.Fatal(p.Records[0])
	}
	next, err := a.Sync(context.Background(), testScope, p.Session, contract.Assignment, p.NextCursor)
	if err != nil || len(next.Records) != 2 || next.HasMore || headers != 7 || lists != 1 {
		t.Fatal(next, headers, lists, err)
	}
	wire, _ := json.Marshal(append(p.Records, next.Records...))
	if strings.Contains(string(wire), "PRIVATE_") || strings.Contains(string(wire), "https:") {
		t.Fatal(string(wire))
	}
}
func TestAssignmentHeaderRejectsRedirectBeforeRequest(t *testing.T) {
	for _, target := range []string{"/mooc-ans/mooc2/work/dowork?courseId=c&classId=k&workId=w", "https://evil.test/mooc-ans/mooc2/work/view?courseId=c&classId=k&workId=w", assignmentViewPath + "?courseId=wrong&classId=k&workId=w"} {
		calls := 0
		a := fixtureAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != assignmentTaskPath {
				t.Fatal("unsafe request issued", r.URL.Path)
			}
			http.Redirect(w, r, target, http.StatusFound)
		})
		_, err := assignmentHeader(context.Background(), &a.client, assignmentLanding{URL: "https://mooc1.chaoxing.com" + assignmentTaskPath + "?courseId=c&classId=k&workId=w", Record: contract.Record{ExternalID: "w"}}, course{ID: "c", Class: "k"})
		if err == nil || calls != 1 {
			t.Fatal(calls, err)
		}
	}
}
