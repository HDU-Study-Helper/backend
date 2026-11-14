package mooc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func discussionFixture(t *testing.T, transport func(*http.Request) (*http.Response, error)) (*http.Client, course, *cursor) {
	t.Helper()
	a := testAdapter(transport)
	_, h, e := a.restore(scope, sessionFor(t, a, scope, "fixture"))
	if e != nil {
		t.Fatal(e)
	}
	co := course{ID: "10", Term: "101", SourceTerm: "202", Type: 2, CurrentUser: "80"}
	root := contract.Record{Kind: "discussion", ExternalID: key("10", "101", "202", "13", "14", "13"), ParentID: co.ref(), Title: "Discussion", Content: "Discuss the assigned reading", Fields: map[string]any{"postId": "14", "unitId": "13", "contentType": json.Number("6"), "sourceTermId": "202"}}
	return h, co, &cursor{Discussion: &discussionCursor{Root: root, Page: 1, Seen: map[string]bool{}}}
}
func TestDiscussionSafeReadsBoundedPagesAndUserState(t *testing.T) {
	calls := 0
	h, co, c := discussionFixture(t, func(r *http.Request) (*http.Response, error) {
		calls++
		wire, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(wire), "c0-scriptName=PostBean") {
			t.Fatal("wrong bean")
		}
		if strings.Contains(r.URL.Path, "getPaginationReplys") && !strings.Contains(string(wire), "c0-param1=number:1\nc0-param2=number:1") {
			t.Fatal("wrong sort/page")
		}
		if r.Header.Get("Referer") != origin+"/spoc/learn/10?tid=101" {
			t.Fatal("lost original route")
		}
		if strings.Contains(r.URL.Path, "getPostDetailById") {
			return response(`dwr.engine._remoteHandleCallback('0','0',{id:14,termId:202,posterId:80,poster:{studentNumber:"PRIVATE"},hasFollowed:false,hasVoteUp:true,hasVoteDown:false,relateUnit:{id:13,replyCount:1,completePercent:1}});`), nil
		}
		if !strings.Contains(r.URL.Path, "getPaginationReplys") {
			t.Fatal("unexpected operation", r.URL.Path)
		}
		return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:1,totalPageCount:1,list:[{id:21,postId:14,replyerId:80,replyerDto:{id:80,nickName:"Reply participant",studentNumber:"PRIVATE"},replyer:{studentNumber:"PRIVATE"},content:"<p>Safe reply</p>",replyTime:100,countComment:5,hasVoteUp:false}]});`), nil
	})
	rows, e := advanceDiscussion(context.Background(), h, co, c)
	if e != nil || len(rows) != 0 || calls != 1 {
		t.Fatal(rows, e, calls)
	}
	rows, e = advanceDiscussion(context.Background(), h, co, c)
	if e != nil || len(rows) != 2 || c.Discussion != nil || calls != 2 {
		t.Fatal(rows, e, calls)
	}
	reply, root := rows[0], rows[1]
	if reply.Content != "" || reply.Kind != contract.DiscussionReplyUser || reply.Fields["user_id"] != "80" || reply.Fields["user_name"] != "Reply participant" || reply.Fields["is_current_user"] != true || root.Fields["is_current_user"] != true || root.Fields["reply_truncated"] != false || root.Fields["reply_count_observed"] != 1 || root.Fields["discussion_record_type"] != "root_post" || root.Content != "Discuss the assigned reading" || root.Fields["reply_body_coverage"] != "not_collected: metadata_only_by_policy" {
		t.Fatal(rows)
	}
	if reply.ExternalID != key(co.ID, co.Term, sourceTerm(co), "discussion", "14", "reply", "21") || reply.ParentID != root.ExternalID || reply.Fields["topic_id"] != root.ExternalID || reply.Fields["root_discussion_id"] != root.ExternalID || reply.Fields["course_ref"] != co.ref() || reply.Fields["sourceTermId"] != "202" {
		t.Fatal("reply identity or root/course association changed", reply)
	}
	if reply.Fields["source_list_endpoint"] != "PostBean.getPaginationReplys" || reply.Fields["body_status"] != "not_collected" || reply.Fields["body_reason"] != "metadata_only_by_policy" || reply.Fields["participation_status"] != "reply_observed" || reply.Fields["participation_semantics"] != "top_level_reply_not_submission_receipt" {
		t.Fatal("reply metadata contract missing", reply)
	}
	if _, exists := reply.Fields["source_body_endpoint"]; exists {
		t.Fatal("reply advertises a body endpoint")
	}
	b, _ := json.Marshal(rows)
	for _, secret := range []string{"PRIVATE", "Safe reply", "replyerId", "replyerDto", "posterId", "studentNumber"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("profile escaped", secret)
		}
	}
	if _, e = readPostDWR(context.Background(), h, co, "incrPostBrowse", []string{"14"}); !errors.Is(e, contract.ErrUnsupported) {
		t.Fatal("mutation allowed")
	}
}
func TestDiscussionReplyPaginationRejectsWrongOwnerDuplicateAndEmptyLoop(t *testing.T) {
	for _, body := range []string{
		`dwr.engine._remoteHandleCallback('0','0',{pageNo:2,totalCount:1,totalPageCount:2,list:[]});`,
		`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:2,totalPageCount:2,list:[]});`,
		`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:1,totalPageCount:1,list:[{id:21,postId:99,content:"x"}]});`,
		`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:2,totalPageCount:1,list:[{id:21,postId:14,content:"x"},{id:21,postId:14,content:"x"}]});`,
	} {
		h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) { return response(body), nil })
		c.Discussion.Detail = true
		if _, e := advanceDiscussion(context.Background(), h, co, c); !errors.Is(e, contract.ErrParse) {
			t.Fatal("invalid page accepted", e)
		}
	}
}
func TestDiscussionReplyBudgetAndUnknownIdentity(t *testing.T) {
	h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:20,totalCount:999,totalPageCount:99,list:[{id:21,postId:14,replyerId:80,content:"x"}]});`), nil
	})
	co.CurrentUser = ""
	c.Discussion.Detail = true
	c.Discussion.Page = 20
	rows, e := advanceDiscussion(context.Background(), h, co, c)
	if e != nil || len(rows) != 2 || rows[1].Fields["reply_truncated"] != true {
		t.Fatal(rows, e)
	}
	if _, ok := rows[0].Fields["is_current_user"]; ok {
		t.Fatal("unknown identity became false")
	}
	h, co, c = discussionFixture(t, func(*http.Request) (*http.Response, error) {
		return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:1,totalPageCount:1,list:[{id:21,postId:14,content:"large"}]});`), nil
	})
	c.Discussion.Detail = true
	c.Discussion.Bytes = maxDiscussionText
	rows, e = advanceDiscussion(context.Background(), h, co, c)
	if e != nil || len(rows) != 2 || rows[0].Kind != contract.DiscussionReplyUser || rows[1].Fields["reply_count_observed"] != 1 || rows[1].Fields["reply_truncated"] != false {
		t.Fatal("legacy text budget truncated metadata roster", rows, e)
	}
}
func TestAutomaticDiscussionPendingRootReadsUnitBodyOnly(t *testing.T) {
	for _, body := range []string{"<p>Discuss the assigned reading</p>", ""} {
		t.Run("body="+body, func(t *testing.T) {
			calls := 0
			a := testAdapter(func(r *http.Request) (*http.Response, error) {
				calls++
				if !strings.Contains(r.URL.Path, "CourseBean.getLessonUnitLearnVo") {
					t.Fatal("automatic sync requested reply roster or post details", r.URL.Path)
				}
				if r.Header.Get("Referer") != origin+"/spoc/learn/10?tid=101" {
					t.Fatal("lost route owner", r.Header.Get("Referer"))
				}
				wire, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(wire), "c0-param0=number:14") || !strings.Contains(string(wire), "c0-param1=number:6") || !strings.Contains(string(wire), "c0-param3=number:13") {
					t.Fatal("wrong unit body parameters", string(wire))
				}
				return response(`dwr.engine._remoteHandleCallback('0','0',{unitId:13,post:{id:14,content:` + strconv.Quote(body) + `}});`), nil
			})
			co := course{ID: "10", Term: "101", SourceTerm: "202", Type: 2, CurrentUser: "80"}
			rows, err := termRecords(fixtureDTO(t, `{"mocTermDto":{"id":202,"chapters":[{"id":11,"lessons":[{"id":12,"units":[{"id":13,"contentId":14,"contentType":6,"viewStatus":1}]}]}]}}`), co, "discussion")
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			c, _ := newCursor(scope, "discussion", "")
			c.Done, c.PendingCourse, c.Pending = true, co, rows
			p, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "fixture"), "discussion", policyCursor(t, c))
			if err != nil || calls != 1 || p.HasMore || p.NextCursor != "" || len(p.Records) != 1 {
				t.Fatal("root should finish after exactly one unit body read", p, err, calls)
			}
			root := p.Records[0]
			wantContent := ""
			if body != "" {
				wantContent = "Discuss the assigned reading"
			}
			if root.Kind != "discussion" || root.ExternalID != rows[0].ExternalID || root.ParentID != co.ref() || root.Content != wantContent || root.Fields["sourceTermId"] != "202" || root.Fields["body_status"] != "read" || root.Fields["postId"] != "14" || root.Fields["task_point_completed"] != true || root.Fields["reply_coverage"] != "not_requested_current_user_task_state_only" || root.Fields["participation_status"] != "unknown" {
				t.Fatal("root requirements/status/policy lost", root)
			}
			for _, key := range []string{"user_id", "user_name", "is_current_user", "source_detail_endpoint", "reply_count_observed"} {
				if _, exists := root.Fields[key]; exists {
					t.Fatal("automatic root contains reply observation", key, root)
				}
			}
		})
	}
}

func TestCatalogProgressHierarchyAndIndependentAssessmentTimes(t *testing.T) {
	dto := fixtureDTO(t, `{"mocTermDto":{"id":202,"chapters":[{"id":11,"completePercent":0,"lessons":[{"id":12,"viewStatus":0,"units":[{"id":13,"contentId":14,"contentType":1,"learnTime":0}]}],"homeworks":[{"id":15,"releaseTime":10,"deadline":20,"test":{"type":3,"releaseTime":11,"deadline":21,"evaluateStart":30,"evaluateEnd":40,"evaluateScoreReleaseTime":50,"userSubmitStatus":0}}]}]}}`)
	co := course{ID: "10", Term: "101", SourceTerm: "202", Type: 2}
	rows, e := termRecords(dto, co, contract.Progress)
	if e != nil || len(rows) != 4 {
		t.Fatal(rows, e)
	}
	for _, r := range rows {
		if r.Fields["progress_status"] != "source_fields_observed" || len(object(r.Fields["native_progress_fields"])) == 0 {
			t.Fatal(r)
		}
	}
	rows, e = termRecords(dto, co, contract.Assignment)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
	times := object(rows[0].Fields["assessment_time_fields"])
	if text(object(times["node"])["deadline"]) != "20" || text(object(times["test"])["deadline"]) != "21" || text(object(times["test"])["evaluateEnd"]) != "40" {
		t.Fatal(times)
	}
	if _, ok := rows[0].Fields["startTime"]; ok {
		t.Fatal("publication became start time")
	}
}
