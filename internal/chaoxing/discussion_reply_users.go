package chaoxing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"academic/backend/internal/contract"
)

const discussionReplyUserResource = contract.Resource("discussion_reply_user")

// DiscussionReplyUsers returns bounded top-level reply observations for one
// verified course topic. It never copies reply content, attachments or URLs.
// It is deliberately separate from Sync: discovering courses never reads replies.
func (a *adapter) DiscussionReplyUsers(ctx context.Context, scope contract.Scope, session contract.Session, courseID, classID, topicID string) (contract.Page, error) {
	fail := contract.Page{}
	if err := ctx.Err(); err != nil {
		return fail, err
	}
	for _, id := range []string{courseID, classID, topicID} {
		if id == "" || len(id) > 512 || strings.ContainsAny(id, "/\\?#\x00\r\n") {
			return fail, contract.ErrInvalid
		}
	}
	s, c, j, err := a.restore(scope, session)
	if err != nil {
		return fail, err
	}
	if !s.Authorized {
		return fail, contract.ErrAuthRequired
	}
	if contract.CourseExcluded(ctx, scope, (course{ID: courseID, Class: classID}).key()) {
		return contract.Page{Records: []contract.Record{}, Session: save(s, j)}, nil
	}
	courses, err := fetchCourses(ctx, c)
	if err != nil {
		return fail, err
	}
	var co *course
	for i := range courses {
		if courses[i].ID == courseID && courses[i].Class == classID {
			co = &courses[i]
			break
		}
	}
	if co == nil {
		return fail, contract.ErrInvalid
	}
	page, err := openDiscussion(ctx, c, *co)
	if err != nil {
		return fail, err
	}
	if err = verifyReplyTopic(ctx, page, *co, topicID); err != nil {
		return fail, err
	}
	out := contract.Page{Records: []contract.Record{}}
	last := ""
	cursors := map[string]bool{"": true}
	ids := map[string]bool{}
	for number := 1; number <= 25; number++ {
		q := url.Values{"bbsid": {page.BBS}, "uuid": {topicID}, "order": {"1"}, "lastValue": {last}}
		b, err := request(ctx, page.Client, http.MethodGet, "https://groupweb.chaoxing.com/pc/invitation/getReplyList?"+q.Encode(), nil)
		if err != nil {
			return fail, err
		}
		if err = checked(b); err != nil {
			return fail, err
		}
		rows, next, more, err := parseReplyUsers(b, courseID, classID, topicID)
		if err != nil {
			return fail, err
		}
		if len(out.Records)+len(rows) > 500 {
			return fail, contract.ErrParse
		}
		for _, row := range rows {
			if ids[row.ExternalID] {
				return fail, contract.ErrParse
			}
			ids[row.ExternalID] = true
		}
		identity := replyCurrentIdentity(page.Client)
		for i := range rows {
			fields := rows[i].Fields
			fields["participation_status"] = "reply_observed"
			fields["participation_semantics"] = "top_level_reply_not_submission_receipt"
			fields["current_user_match"] = "unknown"
			if fields["anonymous"] != true && identity != "" {
				if uid, ok := fields["user_id"].(string); ok && uid != "" {
					fields["is_current_user"] = uid == identity
					fields["current_user_match"] = "verified_cookie_uid"
				}
			}
		}
		out.Records = append(out.Records, rows...)
		if !more {
			if err := ctx.Err(); err != nil {
				return fail, err
			}
			out.Session = save(s, j)
			return out, nil
		}
		if cursors[next] {
			return fail, contract.ErrParse
		}
		cursors[next] = true
		last = next
	}
	return fail, contract.ErrParse
}

func verifyReplyTopic(ctx context.Context, page discussionPage, co course, topicID string) error {
	last, aux := "", ""
	cursors := map[[2]string]bool{{"", ""}: true}
	seen := map[string]bool{}
	count := 0
	for number := 1; number <= 20; number++ {
		topics, next, nextAux, more, err := topicList(ctx, page, co, number, last, aux)
		if err != nil {
			return err
		}
		count += len(topics)
		if count > 400 {
			return contract.ErrParse
		}
		found := false
		for _, topic := range topics {
			if seen[topic.ID] {
				return contract.ErrParse
			}
			seen[topic.ID] = true
			if topic.ID == topicID {
				found = true
			}
		}
		if found {
			return nil
		}
		if !more {
			return contract.ErrInvalid
		}
		key := [2]string{next, nextAux}
		if cursors[key] {
			return contract.ErrParse
		}
		cursors[key] = true
		last, aux = next, nextAux
	}
	return contract.ErrInvalid
}

// UID is account identity evidence kept private; conflicting cookies fail closed.
func replyCurrentIdentity(c *http.Client) string {
	if c.Jar == nil {
		return ""
	}
	u, _ := url.Parse("https://groupweb.chaoxing.com")
	identity := ""
	for _, cookie := range c.Jar.Cookies(u) {
		if cookie.Name != "_uid" && cookie.Name != "UID" {
			continue
		}
		value := strings.TrimSpace(cookie.Value)
		if value == "" {
			continue
		}
		if identity != "" && identity != value {
			return ""
		}
		identity = value
	}
	return identity
}

func parseReplyUsers(b []byte, courseID, classID, topicID string) ([]contract.Record, string, bool, error) {
	var payload struct {
		Status *bool           `json:"status"`
		Datas  json.RawMessage `json:"datas"`
		Poff   *struct {
			LastPage  json.RawMessage `json:"lastPage"`
			LastValue json.RawMessage `json:"lastValue"`
		} `json:"poff"`
	}
	if json.Unmarshal(b, &payload) != nil || payload.Status == nil || !*payload.Status || payload.Poff == nil {
		return nil, "", false, contract.ErrParse
	}
	p := payload.Poff
	end := string(p.LastPage) == "true" || string(p.LastPage) == "1"
	if !end && string(p.LastPage) != "false" && string(p.LastPage) != "0" {
		return nil, "", false, contract.ErrParse
	}
	next := scalar(p.LastValue)
	if !end && (next == "" || len(next) > 512) {
		return nil, "", false, contract.ErrParse
	}
	var rows []map[string]json.RawMessage
	if len(payload.Datas) == 0 || string(payload.Datas) == "null" || json.Unmarshal(payload.Datas, &rows) != nil || len(rows) > 500 {
		return nil, "", false, contract.ErrParse
	}
	out := make([]contract.Record, 0, len(rows))
	for _, row := range rows {
		id := scalar(row["id"])
		if id == "" || len(id) > 512 {
			return nil, "", false, contract.ErrParse
		}
		fields := map[string]any{"course_id": courseID, "class_id": classID, "topic_id": topicID, "coverage": "top_level_only"}
		anonymous := false
		if raw, exists := row["anonymous"]; exists {
			switch string(raw) {
			case "true", "1", `"1"`:
				anonymous = true
			case "false", "0", `"0"`:
			default:
				return nil, "", false, contract.ErrParse
			}
			fields["anonymous"] = anonymous
		}
		name := ""
		// Anonymous replies keep only the displayed alias and never retain puid.
		keys := []string{"createrName", "replyName"}
		if anonymous {
			keys = []string{"replyName"}
		}
		for _, key := range keys {
			raw, exists := row[key]
			if !exists || string(raw) == "null" {
				continue
			}
			if json.Unmarshal(raw, &name) != nil {
				return nil, "", false, contract.ErrParse
			}
			if name != "" {
				break
			}
		}
		if anonymous && name == "" {
			name = "匿名用户"
		}
		if !anonymous {
			uid := scalar(row["puid"])
			if uid != "" {
				fields["user_id"] = uid
			}
		}
		fields["user_name"] = name
		for _, key := range []string{"create_time", "createTime", "update_time", "updateTime"} {
			if raw := scalar(row[key]); raw != "" {
				fields[key] = raw
			}
		}
		aliasTime(fields, "published_at", "create_time", "createTime")
		aliasTime(fields, "updated_at", "update_time", "updateTime")
		out = append(out, record(discussionReplyUserResource, id, topicID, name, "", fields))
	}
	return out, next, !end, nil
}
