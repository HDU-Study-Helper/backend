package mooc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"academic/backend/internal/contract"
)

func TestDiscussionReplyContentIgnoredAndNeverRetained(t *testing.T) {
	for _, content := range []struct{ name, field string }{
		{"missing", ""},
		{"null", ",content:null"},
		{"wrong_type", ",content:{privateBody:\"BODY_SENTINEL\"}"},
		{"malformed_html", ",content:\"<p><script>BODY_SENTINEL</p><a href='https://private.invalid/token'>\""},
		{"long", ",content:" + strconvQuote(strings.Repeat("BODY_SENTINEL", 50000))},
	} {
		t.Run(content.name, func(t *testing.T) {
			h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
				return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:2,totalPageCount:2,list:[{id:21,postId:14,replyerId:80,replyerDto:{id:80,nickName:"Participant",phone:"PROFILE_SECRET"},replyTime:0,countVote:0,hasVoteUp:false,anonymous:false` + content.field + `}]});`), nil
			})
			c.Discussion.Detail = true
			c.Discussion.Bytes = maxDiscussionText
			rows, err := advanceDiscussion(context.Background(), h, co, c)
			if err != nil || len(rows) != 1 || c.Discussion == nil || c.Discussion.Page != 2 || c.Discussion.Bytes != maxDiscussionText {
				t.Fatal(rows, err, c.Discussion)
			}
			r := rows[0]
			if r.Kind != contract.DiscussionReplyUser || r.Content != "" || r.ParentID != c.Discussion.Root.ExternalID || r.Fields["anonymous"] != false || r.Fields["is_current_user"] != true || text(r.Fields["replyTime"]) != "0" || text(r.Fields["countVote"]) != "0" || object(r.Fields["current_user_state"])["hasVoteUp"] != false {
				t.Fatal("metadata or explicit zero/false lost", r)
			}
			wire, _ := json.Marshal(struct {
				Rows   []contract.Record
				Cursor *cursor
			}{rows, c})
			for _, secret := range []string{"BODY_SENTINEL", "PROFILE_SECRET", "private.invalid", "source_body_endpoint"} {
				if strings.Contains(string(wire), secret) {
					t.Fatal("reply content/profile retained", secret)
				}
			}
		})
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDiscussionReplyParticipantIdentityEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, identity, current, user, display string
		match                                  any
	}{
		{"explicit_self", `replyerId:80,anonymous:false,replyerDto:{id:80,nickName:"Public",realName:"PRIVATE",studentNo:"PRIVATE"}`, "80", "80", "Public", true},
		{"dto_self", `replyerDto:{id:80,nickName:"Public"}`, "80", "80", "Public", true},
		{"explicit_other", `replyerId:81,anonymous:0`, "80", "81", "", false},
		{"anonymous", `replyerId:80,anonymous:true,replyerDto:{id:80,nickName:"PRIVATE"}`, "80", "", "", nil},
		{"anonymous_one", `replyerId:80,anonymous:"1",replyerDto:{id:80,nickName:"PRIVATE"}`, "80", "", "", nil},
		{"missing_id", `replyerDto:{nickName:"Public"},replyer:{id:80,name:"PRIVATE"}`, "80", "", "Public", nil},
		{"missing_current", `replyerId:80`, "", "80", "", nil},
		{"conflicting_id", `replyerId:80,replyerDto:{id:81,nickName:"PRIVATE"}`, "80", "", "", nil},
		{"unknown_anonymity", `replyerId:80,anonymous:"unknown",replyerDto:{nickName:"PRIVATE"}`, "80", "", "", nil},
		{"unsafe_name", `replyerId:80,replyerDto:{nickName:"https://private.invalid/PRIVATE"}`, "80", "80", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
				return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:1,totalPageCount:1,list:[{id:21,postId:14,` + tc.identity + `}]});`), nil
			})
			co.CurrentUser = tc.current
			c.Discussion.Detail = true
			rows, err := advanceDiscussion(context.Background(), h, co, c)
			if err != nil || len(rows) != 2 {
				t.Fatal(rows, err)
			}
			f := rows[0].Fields
			if text(f["user_id"]) != tc.user || text(f["user_name"]) != tc.display || f["is_current_user"] != tc.match {
				t.Fatal(f)
			}
			if tc.match == nil && (f["current_user_match"] != "unknown" || f["current_user_identity_coverage"] != "unknown") {
				t.Fatal("unknown identity misrepresented", f)
			}
			b, _ := json.Marshal(rows)
			if strings.Contains(string(b), "PRIVATE") {
				t.Fatal("private profile copied", string(b))
			}
		})
	}
}

func TestDiscussionReplyRosterBudgetAndHTTPBound(t *testing.T) {
	t.Run("roster_limit", func(t *testing.T) {
		h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) {
			return response(`dwr.engine._remoteHandleCallback('0','0',{pageNo:1,totalCount:999,totalPageCount:99,list:[{id:21,postId:14},{id:22,postId:14}]});`), nil
		})
		c.Discussion.Detail = true
		for i := 0; i < maxDiscussionReplies-1; i++ {
			c.Discussion.Seen["old"+strings.Repeat("x", i)] = true
		}
		rows, err := advanceDiscussion(context.Background(), h, co, c)
		if err != nil || len(rows) != 2 || rows[0].Kind != contract.DiscussionReplyUser || rows[1].Fields["reply_count_observed"] != maxDiscussionReplies || rows[1].Fields["reply_truncated"] != true || c.Discussion != nil {
			t.Fatal(rows, err)
		}
	})
	t.Run("response_bound", func(t *testing.T) {
		h, co, c := discussionFixture(t, func(*http.Request) (*http.Response, error) { return response(strings.Repeat("x", maxBody+1)), nil })
		c.Discussion.Detail = true
		if _, err := advanceDiscussion(context.Background(), h, co, c); !errors.Is(err, contract.ErrParse) {
			t.Fatal("HTTP bound lost", err)
		}
	})
}
