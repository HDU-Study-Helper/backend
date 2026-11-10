package mooc

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"academic/backend/internal/contract"
)

const maxReplyPages = 20
const maxDiscussionReplies = 300
const maxDiscussionText = 512 << 10

type discussionCursor struct {
	Root    contract.Record
	Detail  bool
	Page    int
	Seen    map[string]bool
	Bytes   int
	Total   int
	Pages   int
	Changed bool
}

func validDiscussionCursor(d *discussionCursor) bool {
	return d.Root.Kind == "discussion" && validID(text(d.Root.Fields["postId"])) && d.Page >= 1 && d.Page <= maxReplyPages && len(d.Seen) <= maxDiscussionReplies && d.Bytes >= 0 && d.Bytes <= maxDiscussionText && d.Total >= 0 && d.Pages >= 0
}
func userState(m map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"hasFollowed", "hasVoteUp", "hasVoteDown"} {
		if b, ok := m[k].(bool); ok {
			out[k] = b
		}
	}
	return out
}
func copyScalars(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if scalar(src[k]) {
			dst[k] = src[k]
		}
	}
}
func finishDiscussion(c *cursor, status string) contract.Record {
	d := c.Discussion
	f := d.Root.Fields
	f["reply_coverage"] = status
	f["reply_pages_read"] = d.Page
	f["reply_count_observed"] = len(d.Seen)
	f["reply_total_count"] = d.Total
	f["reply_total_pages"] = d.Pages
	f["reply_snapshot_changed"] = d.Changed
	f["reply_truncated"] = status != "visible_reply_pages_exhausted"
	f["discussion_coverage"] = "partial: visible root and bounded top-level replies; nested comments and course-wide forums excluded"
	f["comment_coverage"] = "unsupported: nested comments not fetched"
	c.Discussion = nil
	return d.Root
}

// advanceDiscussion performs one read per Sync call. Reply bodies are emitted
// immediately, while only IDs and a cumulative text budget remain in the cursor.
func advanceDiscussion(ctx context.Context, h *http.Client, co course, c *cursor) ([]contract.Record, error) {
	d := c.Discussion
	root := &d.Root
	if root.ParentID != co.ref() || root.Fields["sourceTermId"] != sourceTerm(co) {
		return nil, contract.ErrInvalid
	}
	post := text(root.Fields["postId"])
	if !d.Detail {
		v, e := readPostDWR(ctx, h, co, "getPostDetailById", []string{post})
		if e != nil {
			return nil, e
		}
		m := object(v)
		if text(m["id"]) != post {
			return nil, contract.ErrParse
		}
		// The caller's route may differ from the source term, but source attribution
		// of the post itself must agree with the catalog used to select it.
		if id := text(m["termId"]); id != "" && id != sourceTerm(co) {
			return nil, contract.ErrParse
		}
		f := root.Fields
		f["discussion_record_type"] = "root_post"
		f["root_discussion_id"] = root.ExternalID
		f["source_detail_endpoint"] = "PostBean.getPostDetailById"
		f["current_user_state"] = userState(m)
		f["current_user_identity_coverage"] = "unknown"
		if co.CurrentUser != "" && validID(text(m["posterId"])) {
			f["is_current_user"] = co.CurrentUser == text(m["posterId"])
			f["current_user_identity_coverage"] = "official_webUser_id_comparison"
		}
		if related := object(m["relateUnit"]); related != nil {
			if text(related["id"]) != text(f["unitId"]) {
				return nil, contract.ErrParse
			}
			f["native_progress_fields"] = nativeProgress(metadata(related, nil))
		}
		copyScalars(f, m, "countReply", "countVote", "countBrowse", "gmtCreate", "gmtModified", "postTime", "lastReplyTime", "lockFlag", "tagAgree", "tagTop", "tagSolve")
		d.Detail = true
		return []contract.Record{}, nil
	}
	v, e := readPostDWR(ctx, h, co, "getPaginationReplys", []string{post, "1", strconv.Itoa(d.Page)})
	if e != nil {
		return nil, e
	}
	m := object(v)
	rows, ok := m["list"].([]any)
	if !ok || len(rows) > 100 {
		return nil, contract.ErrParse
	}
	integer := func(k string) (int, error) {
		n, e := strconv.Atoi(text(m[k]))
		if e != nil || n < 0 || n > 10000000 {
			return 0, contract.ErrParse
		}
		return n, nil
	}
	page, e := integer("pageNo")
	if e != nil || page != d.Page {
		return nil, contract.ErrParse
	}
	total, e := integer("totalCount")
	if e != nil {
		return nil, e
	}
	pages, e := integer("totalPageCount")
	if e != nil {
		return nil, e
	}
	if d.Page > 1 && (d.Total != total || d.Pages != pages) {
		d.Changed = true
	}
	d.Total = total
	d.Pages = pages
	if len(rows) > total || len(rows) > 0 && (pages == 0 || d.Page > pages) {
		return nil, contract.ErrParse
	}
	if len(rows) == 0 && d.Page < pages {
		return nil, contract.ErrParse
	}
	out := []contract.Record{}
	for _, v := range rows {
		m := object(v)
		id := text(m["id"])
		if !validID(id) || text(m["postId"]) != post || d.Seen[id] {
			return nil, contract.ErrParse
		}
		raw, ok := m["content"].(string)
		if !ok {
			return nil, contract.ErrParse
		}
		if len(d.Seen) == maxDiscussionReplies || len(raw) > maxDiscussionText-d.Bytes {
			out = append(out, finishDiscussion(c, "bounded_reply_budget_reached"))
			return out, nil
		}
		body, e := plainBody(raw)
		if e != nil {
			return nil, e
		}
		d.Bytes += len(raw)
		d.Seen[id] = true
		f := sourceFields(co)
		for _, k := range []string{"chapterId", "lessonId", "unitId", "contentId", "nodeId", "path", "path_titles", "sourceCourseId"} {
			if value, ok := root.Fields[k]; ok {
				f[k] = value
			}
		}
		f["catalog_node_type"] = "discussion_reply"
		f["source_collection"] = "replies"
		f["discussion_record_type"] = "reply"
		f["root_discussion_id"] = root.ExternalID
		f["replyId"] = id
		f["postId"] = post
		f["source_body_endpoint"] = "PostBean.getPaginationReplys"
		f["body_status"] = "read"
		f["reply_page"] = page
		f["current_user_state"] = userState(m)
		f["current_user_identity_coverage"] = "unknown"
		if co.CurrentUser != "" && validID(text(m["replyerId"])) {
			f["is_current_user"] = co.CurrentUser == text(m["replyerId"])
			f["current_user_identity_coverage"] = "official_webUser_id_comparison"
		}
		copyScalars(f, m, "replyTime", "gmtCreate", "gmtModified", "countComment", "countVote", "floorNumber", "tagAgree", "tagTop", "anonymous", "deleted", "lectorOrAssistFlag")
		f["comment_coverage"] = "unsupported: nested comments not fetched"
		out = append(out, contract.Record{Kind: "discussion", ParentID: co.ref(), ExternalID: key(co.ID, co.Term, sourceTerm(co), "discussion", post, "reply", id), Title: root.Title, Content: body, Fields: f, ObservedAt: time.Now().UTC()})
	}
	if d.Page >= pages {
		out = append(out, finishDiscussion(c, "visible_reply_pages_exhausted"))
	} else if d.Page == maxReplyPages || len(d.Seen) == maxDiscussionReplies {
		out = append(out, finishDiscussion(c, "bounded_reply_budget_reached"))
	} else {
		d.Page++
	}
	return out, nil
}
