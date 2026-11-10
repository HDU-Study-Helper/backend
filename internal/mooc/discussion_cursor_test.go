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
