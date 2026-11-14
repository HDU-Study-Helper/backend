package mooc

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestDiscussionCursorResumesTwoReplyPagesAndMarksSnapshotChange(t *testing.T) {
	call := 0
	h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		call++
		return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:` + strconv.Itoa(call) + `,totalCount:` + strconv.Itoa(2+call) + `,totalPageCount:2,list:[{id:` + strconv.Itoa(20+call) + `,postId:14,content:"reply"}]});`), nil
	})
	c.Discussion.Detail = true
	c.PendingCourse = co
	rows, e := advanceDiscussion(context.Background(), h, co, c)
	if e != nil || len(rows) != 1 || c.Discussion.Page != 2 {
		t.Fatal(rows, e)
	}
	c.Scope = scope
	c.Source = "mooc"
	c.Resource = "discussion"
	c.Version = 1
	c.Type = 1
	c.Page = 1
	raw, _ := json.Marshal(c)
	restored, e := newCursor(scope, "discussion", base64Cursor(raw))
	if e != nil {
		t.Fatal(e)
	}
	rows, e = advanceDiscussion(context.Background(), h, co, &restored)
	if e != nil || len(rows) != 2 || rows[1].Fields["reply_snapshot_changed"] != true || rows[1].Fields["reply_count_observed"] != 2 {
		t.Fatal(rows, e)
	}
}
func TestDiscussionCursorVersionCompatibilityKeepsPosition(t *testing.T) {
	for _, version := range []int{1, 2} {
		c, _ := newCursor(scope, "discussion", "")
		c.Version = version
		c.Type = 2
		c.Page = 3
		c.Done = true
		raw, _ := json.Marshal(c)
		got, e := newCursor(scope, "discussion", base64Cursor(raw))
		if e != nil || got.Version != 1 || got.Type != 2 || got.Page != 3 || !got.Done {
			t.Fatal(got, e)
		}
	}
}
func TestDiscussionLegacyTextBudgetCursorKeepsReplyPosition(t *testing.T) {
	h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
		return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:2,totalCount:2,totalPageCount:3,list:[{id:22,postId:14}]});`), nil
	})
	c.Scope, c.Source, c.Resource = scope, "mooc", "discussion"
	c.Version, c.Type, c.Page, c.PendingCourse = 1, 1, 1, co
	c.Discussion.Detail, c.Discussion.Page, c.Discussion.Bytes = true, 2, maxDiscussionText
	c.Discussion.Seen["21"] = true
	raw, _ := json.Marshal(c)
	restored, err := newCursor(scope, "discussion", base64Cursor(raw))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := advanceDiscussion(context.Background(), h, co, &restored)
	if err != nil || len(rows) != 1 || rows[0].Kind != contract.DiscussionReplyUser || restored.Discussion.Page != 3 || restored.Discussion.Bytes != maxDiscussionText || len(restored.Discussion.Seen) != 2 {
		t.Fatal("legacy cursor lost position or charged reply content", rows, restored, err)
	}
}

func TestDiscussionPolicyRecheckedWhileRepliesPending(t *testing.T) {
	calls := 0
	a := testAdapter(func(*http.Request) (*http.Response, error) { calls++; return response(``), nil })
	_, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) { t.Fatal("unexpected read"); return nil, nil })
	c.Scope = scope
	c.Source = "mooc"
	c.Resource = "discussion"
	c.Version = 1
	c.Type = 1
	c.Page = 1
	c.Done = true
	c.PendingCourse = co
	b, _ := json.Marshal(c)
	ctx := contract.WithExcludedCourses(context.Background(), scope, []string{co.ref()})
	p, e := a.Sync(ctx, scope, sessionFor(t, a, scope, "fixture"), "discussion", base64Cursor(b))
	if e != nil || len(p.Records) != 0 || p.HasMore || calls != 0 {
		t.Fatal(p, e, calls)
	}
}
func TestAutomaticDiscussionRetiresLegacyCursorWithoutReadsOrLostWork(t *testing.T) {
	for _, content := range []string{"Discuss the assigned reading", ""} {
		t.Run("content="+content, func(t *testing.T) {
			calls := 0
			a := testAdapter(func(*http.Request) (*http.Response, error) {
				calls++
				t.Fatal("legacy cursor requested network")
				return nil, nil
			})
			_, co, old := discussionFixture(t, nil)
			c, _ := newCursor(scope, "discussion", "")
			c.Done, c.PendingCourse, c.Discussion = true, co, old.Discussion
			c.Discussion.Page, c.Discussion.Detail = 7, true
			c.Discussion.Seen["21"] = true
			c.Discussion.Root.Content = content
			rootID := c.Discussion.Root.ExternalID
			pending := contract.Record{Kind: "discussion", ExternalID: "later-root", ParentID: co.ref(), Title: "Later root", Fields: map[string]any{"sourceTermId": "202"}}
			c.Pending = []contract.Record{pending}
			next := course{ID: "20", Term: "303", Type: 1, Discovered: true}
			c.Courses = []course{next}
			p, err := a.Sync(context.Background(), scope, sessionFor(t, a, scope, "fixture"), "discussion", policyCursor(t, c))
			if err != nil || calls != 0 || len(p.Records) != 1 || !p.HasMore {
				t.Fatal(p, err, calls)
			}
			root := p.Records[0]
			if root.ExternalID != rootID || root.ParentID != co.ref() || root.Content != content || root.Fields["sourceTermId"] != "202" || root.Fields["reply_collection_policy"] != "disabled_current_user_task_state_only" || root.Fields["participation_status"] != "unknown" {
				t.Fatal("legacy root lost or reply completion invented", root)
			}
			if _, exists := root.Fields["task_point_completed"]; exists {
				t.Fatal("missing catalog view status became completed", root)
			}
			saved, err := newCursor(scope, "discussion", p.NextCursor)
			if err != nil || saved.Discussion != nil || len(saved.Pending) != 1 || saved.Pending[0].ExternalID != pending.ExternalID || len(saved.Courses) != 1 || saved.Courses[0].ref() != next.ref() || saved.Index != 0 {
				t.Fatal("retiring replies lost pending work", saved, err)
			}
			p, err = a.Sync(context.Background(), scope, p.Session, "discussion", p.NextCursor)
			if err != nil || calls != 0 || len(p.Records) != 1 || p.Records[0].ExternalID != pending.ExternalID || !p.HasMore {
				t.Fatal("pending root did not resume without a request", p, err, calls)
			}
		})
	}
}

func TestAutomaticDiscussionExcludedLegacyCursorPreservesNextCourse(t *testing.T) {
	a := testAdapter(func(*http.Request) (*http.Response, error) {
		t.Fatal("excluded legacy cursor requested network")
		return nil, nil
	})
	_, co, old := discussionFixture(t, nil)
	c, _ := newCursor(scope, "discussion", "")
	c.Done, c.PendingCourse, c.Discussion = true, co, old.Discussion
	c.Discussion.Page = 7
	c.Pending = []contract.Record{{Kind: "discussion", ExternalID: "excluded-pending", ParentID: co.ref()}}
	next := course{ID: "20", Term: "303", Type: 1}
	c.Courses = []course{next}
	ctx := contract.WithExcludedCourses(context.Background(), scope, []string{co.ref()})
	p, err := a.Sync(ctx, scope, sessionFor(t, a, scope, "fixture"), "discussion", policyCursor(t, c))
	if err != nil || len(p.Records) != 0 || !p.HasMore {
		t.Fatal(p, err)
	}
	saved, err := newCursor(scope, "discussion", p.NextCursor)
	if err != nil || saved.Discussion != nil || len(saved.Pending) != 0 || len(saved.Courses) != 1 || saved.Courses[0].ref() != next.ref() || saved.Index != 0 {
		t.Fatal("exclusion lost next course", saved, err)
	}
}

func TestDiscussionCursorRejectsCrossScopeAndUnboundedPage(t *testing.T) {
	_, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) { return nil, nil })
	c.Scope = scope
	c.Source = "mooc"
	c.Resource = "discussion"
	c.Version = 1
	c.Type = 1
	c.Page = 1
	c.PendingCourse = co
	c.Discussion.Page = maxReplyPages + 1
	raw, _ := json.Marshal(c)
	if _, e := newCursor(scope, "discussion", base64Cursor(raw)); e == nil {
		t.Fatal("unbounded page allowed")
	}
	for _, method := range []string{"addReply", "clearUnreadCount", "incrPostBrowse", "saveLearnInfo", "openPaper"} {
		if !strings.Contains(method, "get") { // ensure names are mutation probes
			h, co, _ := discussionFixture(t, func(*http.Request) (*http.Response, error) { t.Fatal("mutation reached network"); return nil, nil })
			if _, e := readPostDWR(context.Background(), h, co, method, []string{"14"}); e == nil {
				t.Fatal(method)
			}
		}
	}
}
