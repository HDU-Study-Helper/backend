package chaoxing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

const discussionResource = contract.Resource("discussion")

type discussionPage struct {
	Client *http.Client
	Body   []byte
	Fields map[string]string
	URL    *url.URL
	BBS    string
}

// discussionURL follows the actual navigation element, preserving any signed
// query values supplied by the server. Tickets never leave this private path.
func discussionURL(b []byte, co course) (*url.URL, map[string]string, error) {
	d, err := document(b)
	if err != nil {
		return nil, nil, err
	}
	fields, err := hidden(b)
	if err != nil {
		return nil, nil, err
	}
	var target *url.URL
	bad := false
	walk(d, func(n *html.Node) {
		if attr(n, "id") == "t" && fields["t"] == "" {
			fields["t"] = strings.TrimSpace(text(n))
		}
		if n.Data != "li" || attr(n, "dataname") != "tl" {
			return
		}
		walk(n, func(a *html.Node) {
			if a.Data != "a" || attr(a, "data-url") == "" {
				return
			}
			u, e := url.Parse(attr(a, "data-url"))
			if e != nil || !official(u) || !strings.Contains(u.Path, "topic") {
				bad = true
				return
			}
			if target != nil && target.String() != u.String() {
				bad = true
				return
			}
			target = u
		})
	})
	if bad || target == nil {
		return nil, nil, contract.ErrParse
	}
	q := url.Values{"courseid": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "ut": {"s"}, "t": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "stuenc": {fields["enc"]}, "bbsid": {fields["bbsid"]}, "showAIEnc": {fields["showAIEnc"]}, "supportAI": {fields["showAiAssistantTea"]}, "enc": {fields["enc"]}}
	if fields["courseApp"] == "2" && fields["bbsUrlSwitch"] == "false" {
		q = url.Values{"courseId": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "ut": {"s"}, "enc": {fields["oldenc"]}}
	}
	if fields["t"] != "" {
		q.Set("t", fields["t"])
	}
	if fields["bbsUrlSwitch"] == "true" {
		q.Set("learnSilverStartTime", fields["learnSilverStartTime"])
		q.Set("learnSilverEndTime", fields["learnSilverEndTime"])
	}
	if fields["microCourseTopicOpenType"] == "1" {
		q.Set("microClassDiscuss", "1")
	}
	original := target.Query()
	for key, values := range q {
		if _, exists := original[key]; !exists {
			original[key] = values
		}
	}
	target.RawQuery = original.Encode()
	return target, fields, nil
}

var discussionCircle = regexp.MustCompile(`(?m)\bcircle\s*:\s*(\{[^\r\n]*\})\s*,?`)

func openDiscussion(ctx context.Context, c *http.Client, co course) (discussionPage, error) {
	out := discussionPage{}
	b, err := request(ctx, c, http.MethodGet, middle+"?"+url.Values{"courseid": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "ismooc2": {"1"}, "v": {"2"}}.Encode(), nil)
	if err != nil {
		return out, err
	}
	u, fields, err := discussionURL(b, co)
	if err != nil {
		return out, err
	}
	client := *c
	client.Transport = discussionReferer{base: c.Transport, referer: u.String()}
	b, err = request(ctx, &client, http.MethodGet, u.String(), nil)
	if err != nil {
		return out, err
	}
	if _, err = document(b); err != nil {
		return out, err
	}
	pageFields, err := hidden(b)
	if err != nil {
		return out, err
	}
	bbs := pageFields["bbsid"]
	if bbs == "" {
		if m := discussionCircle.FindSubmatch(b); len(m) > 1 {
			var circle map[string]json.RawMessage
			if json.Unmarshal(m[1], &circle) != nil {
				return out, contract.ErrParse
			}
			bbs = scalar(circle["bbsid"])
		}
	}
	if bbs == "" {
		bbs = fields["bbsid"]
	}
	if bbs == "" {
		bbs = u.Query().Get("bbsid")
	}
	if bbs == "" || strings.ContainsAny(bbs, "/\\?#") || len(bbs) > 512 {
		return out, contract.ErrParse
	}
	for _, key := range []string{"learnSilverStartTime", "learnSilverEndTime"} {
		if values, ok := u.Query()[key]; ok && len(values) > 0 {
			fields[key] = values[0]
		}
	}
	fields["folder_uuid"] = pageFields["folder_uuid"]
	return discussionPage{Client: &client, Body: b, Fields: fields, URL: u, BBS: bbs}, nil
}

func topicList(ctx context.Context, page discussionPage, co course, number int, last, aux string) ([]DiscussionTopic, string, string, bool, error) {
	q := url.Values{"folder_uuid": {page.Fields["folder_uuid"]}, "page": {strconv.Itoa(number)}, "pageSize": {"20"}, "kw": {""}, "courseId": {co.ID}, "isSetTop": {""}, "selectedStartTime": {""}, "selectedEndTime": {""}, "selectedType": {"1"}, "learnSilverStartTime": {page.Fields["learnSilverStartTime"]}, "learnSilverEndTime": {page.Fields["learnSilverEndTime"]}, "last_reply_time": {last}, "lastAuxValue": {aux}}
	b, err := request(ctx, page.Client, http.MethodPost, "https://groupweb.chaoxing.com/course/topic/"+url.PathEscape(page.BBS)+"/getTopicList?"+q.Encode(), url.Values{"tags": {"classId0000001,classId" + co.Class + ",courseId" + co.ID}})
	if err != nil {
		return nil, "", "", false, err
	}
	if err = checked(b); err != nil {
		return nil, "", "", false, err
	}
	return parseTopics(b)
}

func parseTopics(b []byte) ([]DiscussionTopic, string, string, bool, error) {
	var payload struct {
		Status *bool           `json:"status"`
		Datas  json.RawMessage `json:"datas"`
		Poff   *struct {
			LastPage     json.RawMessage `json:"lastPage"`
			LastValue    json.RawMessage `json:"lastValue"`
			LastAuxValue json.RawMessage `json:"lastAuxValue"`
		} `json:"poff"`
	}
	if json.Unmarshal(b, &payload) != nil || payload.Status == nil {
		return nil, "", "", false, contract.ErrParse
	}
	if !*payload.Status {
		return nil, "", "", false, contract.ErrParse
	}
	if payload.Poff == nil {
		return nil, "", "", false, contract.ErrParse
	}
	p := payload.Poff
	end := string(p.LastPage) == "true" || string(p.LastPage) == "1"
	if !end && string(p.LastPage) != "false" && string(p.LastPage) != "0" {
		return nil, "", "", false, contract.ErrParse
	}
	var rows []map[string]json.RawMessage
	if len(payload.Datas) == 0 || string(payload.Datas) == "null" || json.Unmarshal(payload.Datas, &rows) != nil || len(rows) > 500 {
		return nil, "", "", false, contract.ErrParse
	}
	last, aux := scalar(p.LastValue), scalar(p.LastAuxValue)
	if !end && (last == "" || len(p.LastAuxValue) == 0 || string(p.LastAuxValue) == "null" || (aux == "" && string(p.LastAuxValue) != `""`)) {
		return nil, "", "", false, contract.ErrParse
	}
	out := make([]DiscussionTopic, 0, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		id := scalar(row["uuid"])
		if id == "" || seen[id] {
			return nil, "", "", false, contract.ErrParse
		}
		seen[id] = true
		var title, content string
		if raw, exists := row["title"]; exists && string(raw) != "null" && json.Unmarshal(raw, &title) != nil {
			return nil, "", "", false, contract.ErrParse
		}
		if string(row["content"]) == "null" || json.Unmarshal(row["content"], &content) != nil {
			return nil, "", "", false, contract.ErrParse
		}
		d, err := html.Parse(strings.NewReader(content))
		if err != nil {
			return nil, "", "", false, contract.ErrParse
		}
		fields := map[string]any{}
		// Only observed content metadata is allowed; signatures, file objects and
		// navigation URLs cannot be copied through generic response maps.
		for _, key := range []string{"createrName", "creatorName", "createName", "name", "createTime", "updateTime", "lastReplyTime", "reply_count", "replyCount", "praise_count", "praiseCount", "isTeacher", "topicType", "isReply"} {
			raw, exists := row[key]
			if !exists {
				continue
			}
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return nil, "", "", false, contract.ErrParse
			}
			switch value.(type) {
			case string, float64, bool:
				fields[key] = value
			default:
				return nil, "", "", false, contract.ErrParse
			}
		}
		out = append(out, DiscussionTopic{ID: id, Title: title, Text: text(d), Fields: fields})
	}
	return out, last, aux, !end, nil
}

// DiscussionCourses discovers independently schedulable courses without exposing
// CPI, signed navigation URLs, or other upstream session state.
func (a *adapter) DiscussionCourses(ctx context.Context, scope contract.Scope, session contract.Session) ([]contract.CoursePartition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, c, _, err := a.restore(scope, session)
	if err != nil {
		return nil, err
	}
	if !s.Authorized {
		return nil, contract.ErrAuthRequired
	}
	courses, err := fetchCourses(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]contract.CoursePartition, 0, len(courses))
	for _, co := range courses {
		out = append(out, contract.CoursePartition{CourseID: co.ID, ClassID: co.Class})
	}
	return out, nil
}

// SyncDiscussionCourse reads one course page. The caller keeps an independent
// checkpoint for each course/class pair so one failure cannot block other courses.
func (a *adapter) SyncDiscussionCourse(ctx context.Context, scope contract.Scope, session contract.Session, courseID, classID, raw string) (contract.Page, error) {
	if err := ctx.Err(); err != nil {
		return contract.Page{}, err
	}
	if courseID == "" || classID == "" || len(courseID) > 512 || len(classID) > 512 {
		return contract.Page{}, contract.ErrInvalid
	}
	if raw == "" {
		s, c, j, err := a.restore(scope, session)
		if err != nil {
			return contract.Page{}, err
		}
		if !s.Authorized {
			return contract.Page{}, contract.ErrAuthRequired
		}
		courses, err := fetchCourses(ctx, c)
		if err != nil {
			return contract.Page{}, err
		}
		var target *course
		for i := range courses {
			if courses[i].ID == courseID && courses[i].Class == classID {
				target = &courses[i]
				break
			}
		}
		if target == nil {
			return contract.Page{}, contract.ErrInvalid
		}
		raw = encode(cursor{Version: 1, Scope: scope, Resource: discussionResource, Courses: []course{*target}, Page: 1})
		session = save(s, j)
	} else {
		v, err := decode(raw, scope, discussionResource)
		if err != nil {
			return contract.Page{}, err
		}
		if len(v.Courses) != 1 || v.Index != 0 || v.Courses[0].ID != courseID || v.Courses[0].Class != classID {
			return contract.Page{}, contract.ErrInvalid
		}
	}
	return a.Sync(ctx, scope, session, discussionResource, raw)
}

func syncDiscussion(ctx context.Context, c *http.Client, v *cursor) ([]contract.Record, error) {
	co := v.Courses[v.Index]
	if co.ID == "" || co.Class == "" || co.CPI == "" {
		return nil, contract.ErrParse
	}
	page, err := openDiscussion(ctx, c, co)
	if err != nil {
		return nil, err
	}
	topics, last, aux, more, err := topicList(ctx, page, co, v.Page, v.DiscussionLastValue, v.DiscussionLastAuxValue)
	if err != nil {
		return nil, err
	}
	if more && (v.Page >= 1000 || (last == v.DiscussionLastValue && aux == v.DiscussionLastAuxValue)) {
		return nil, contract.ErrParse
	}
	out := make([]contract.Record, 0, len(topics))
	for _, topic := range topics {
		fields := topic.Fields
		fields["course_id"], fields["class_id"] = co.ID, co.Class
		for _, mapping := range [][2]string{{"createrName", "author"}, {"creatorName", "author"}, {"createName", "author"}, {"name", "author"}, {"reply_count", "reply_count"}, {"replyCount", "reply_count"}, {"praise_count", "praise_count"}, {"praiseCount", "praise_count"}, {"topicType", "topic_type"}} {
			if value, ok := topic.Fields[mapping[0]]; ok {
				if _, exists := fields[mapping[1]]; !exists {
					fields[mapping[1]] = value
				}
			}
		}
		out = append(out, record(discussionResource, topic.ID, co.key(), topic.Title, topic.Text, fields))
	}
	if more {
		v.Page++
		v.DiscussionLastValue, v.DiscussionLastAuxValue = last, aux
	} else {
		v.Index++
		v.Page = 1
		v.DiscussionLastValue, v.DiscussionLastAuxValue = "", ""
	}
	return out, nil
}
