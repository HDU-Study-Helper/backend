package chaoxing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

type course struct {
	ID      string
	Class   string
	CPI     string
	Name    string
	Teacher string
}

func (c course) key() string { b, _ := json.Marshal([]string{c.ID, c.Class}); return string(b) }

type cursor struct {
	Version                int
	Scope                  contract.Scope
	Resource               contract.Resource
	Courses                []course
	Index                  int
	Page                   int
	Seen                   int
	Last                   string
	Fingerprint            string
	WorkIDs                []string
	ExpectedTotal          int
	DiscussionLastValue    string
	DiscussionLastAuxValue string
	MaterialFolders        []materialFolder
	MaterialIDs            []string
	MaterialTotalPages     int
	AssignmentLandings     []assignmentLanding
	AssignmentCourseDone   bool
	TeachingRecords        []contract.Record
}

// advanceCourse discards every course-local pagination value while retaining the
// original snapshot: Index, not a filtered Courses slice, identifies the next course.
func (v *cursor) advanceCourse() {
	v.Index++
	v.Page, v.Seen, v.ExpectedTotal = 1, 0, 0
	v.Last, v.Fingerprint = "", ""
	v.WorkIDs = nil
	v.DiscussionLastValue, v.DiscussionLastAuxValue = "", ""
	v.MaterialFolders, v.MaterialIDs, v.MaterialTotalPages = nil, nil, 0
	v.AssignmentLandings, v.AssignmentCourseDone, v.TeachingRecords = nil, false, nil
}

func (v *cursor) skipExcludedCourses(ctx context.Context) {
	// decode/fetchCourses cap the snapshot at 500; this loop performs no I/O.
	for v.Index < len(v.Courses) && contract.CourseExcluded(ctx, v.Scope, v.Courses[v.Index].key()) {
		v.advanceCourse()
	}
}

func encode(v cursor) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
func decode(raw string, scope contract.Scope, r contract.Resource) (cursor, error) {
	v := cursor{Version: 1, Scope: scope, Resource: r, Page: 1}
	if raw == "" {
		return v, nil
	}
	if len(raw) > 1<<20 {
		return v, contract.ErrInvalid
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil || json.Unmarshal(b, &v) != nil || v.Version != 1 || v.Scope != scope || v.Resource != r || v.Index < 0 || v.Index > len(v.Courses) || v.Page < 1 || v.Page > 1000 || v.Seen < 0 || len(v.Courses) > 500 {
		return v, contract.ErrInvalid
	}
	return v, nil
}
func (a *adapter) Sync(ctx context.Context, scope contract.Scope, session contract.Session, r contract.Resource, raw string) (contract.Page, error) {
	if err := ctx.Err(); err != nil {
		return contract.Page{}, err
	}
	switch r {
	case contract.Course, contract.Chapter, contract.Lesson, contract.Assignment, contract.Exam, contract.Progress, contract.Notice, discussionResource, contract.Material:
	default:
		return contract.Page{}, contract.ErrUnsupported
	}
	s, c, j, e := a.restore(scope, session)
	if e != nil {
		return contract.Page{}, e
	}
	if !s.Authorized {
		return contract.Page{}, contract.ErrAuthRequired
	}
	v, e := decode(raw, scope, r)
	if e != nil {
		return contract.Page{}, e
	}
	out := contract.Page{Records: []contract.Record{}}
	switch r {
	case contract.Material:
		if raw == "" {
			v.Courses, e = fetchCourses(ctx, c)
			if e != nil {
				return contract.Page{}, e
			}
		}
		v.skipExcludedCourses(ctx)
		if v.Index < len(v.Courses) {
			out.Records, e = syncMaterials(ctx, c, &v)
			if e != nil {
				return contract.Page{}, e
			}
		}
		v.skipExcludedCourses(ctx)
		out.HasMore = v.Index < len(v.Courses)
	case contract.Chapter, contract.Lesson, contract.Progress:
		if raw == "" {
			v.Courses, e = fetchCourses(ctx, c)
			if e != nil {
				return contract.Page{}, e
			}
		}
		v.skipExcludedCourses(ctx)
		if v.Index < len(v.Courses) {
			out.Records, e = syncTeaching(ctx, c, &v)
			if e != nil {
				return contract.Page{}, e
			}
		}
		v.skipExcludedCourses(ctx)
		out.HasMore = v.Index < len(v.Courses)
	case contract.Course, contract.Assignment:
		if raw == "" {
			v.Courses, e = fetchCourses(ctx, c)
			if e != nil {
				return out, e
			}
		}
		v.skipExcludedCourses(ctx)
		if r == contract.Course {
			end := v.Index + 50
			if end > len(v.Courses) {
				end = len(v.Courses)
			}
			for _, co := range v.Courses[v.Index:end] {
				if contract.CourseExcluded(ctx, scope, co.key()) {
					continue
				}
				out.Records = append(out.Records, record(r, co.key(), "", co.Name, "", map[string]any{"course_id": co.ID, "class_id": co.Class, "course_name": co.Name, "teacher": co.Teacher, "coverage": "course_list"}))
			}
			v.Index = end
		} else if v.Index < len(v.Courses) {
			co := v.Courses[v.Index]
			if r == contract.Assignment && len(v.AssignmentLandings) > 0 {
				out.Records, e = syncAssignmentHeaders(ctx, c, &v)
				if e != nil {
					return contract.Page{}, e
				}
				v.skipExcludedCourses(ctx)
				out.HasMore = v.Index < len(v.Courses)
				break
			}
			if co.ID == "" || co.Class == "" || co.CPI == "" {
				return out, contract.ErrParse
			}
			b, err := request(ctx, c, http.MethodGet, middle+"?"+url.Values{"courseid": {co.ID}, "clazzid": {co.Class}, "cpi": {co.CPI}, "ismooc2": {"1"}, "v": {"2"}}.Encode(), nil)
			if err != nil {
				return out, err
			}
			if err = checked(b); err != nil {
				return out, err
			}
			inputs, err := hidden(b)
			if err != nil {
				return out, err
			}
			if r == contract.Assignment {
				if inputs["workEnc"] == "" || inputs["enc"] == "" {
					return out, contract.ErrParse
				}
				q := url.Values{"courseId": {co.ID}, "classId": {co.Class}, "cpi": {co.CPI}, "enc": {inputs["workEnc"]}, "stuenc": {inputs["enc"]}, "openc": {inputs["openc"]}, "ut": {"s"}, "t": {strconv.FormatInt(time.Now().UnixMilli(), 10)}, "isdisplaytable": {"2"}, "pageNum": {strconv.Itoa(v.Page)}}
				b, err = request(ctx, c, http.MethodGet, "https://mooc1.chaoxing.com/mooc-ans/mooc2/work/list?"+q.Encode(), nil)
				if err != nil {
					return out, err
				}
				rr, total, err := assignments(b, co)
				if err != nil {
					return out, err
				}
				identityBytes, _ := json.Marshal(rrIDs(rr))
				fingerprint := digest(identityBytes)
				if v.Page > 1 && total != v.ExpectedTotal {
					return out, contract.ErrParse
				}
				known := map[string]bool{}
				for _, id := range v.WorkIDs {
					known[id] = true
				}
				for _, row := range rr {
					if known[row.ExternalID] {
						return out, contract.ErrParse
					}
					known[row.ExternalID] = true
					v.WorkIDs = append(v.WorkIDs, row.ExternalID)
				}
				v.ExpectedTotal = total
				if v.Page > 1 && (fingerprint == v.Fingerprint || len(rr) == 0) {
					return out, contract.ErrParse
				}
				v.Seen += len(rr)
				if v.Seen > total {
					return out, contract.ErrParse
				}
				v.AssignmentLandings = assignmentLandings(b, rr, co)
				v.AssignmentCourseDone = v.Seen == total
				if v.Seen < total {
					v.Page++
					v.Fingerprint = fingerprint
				}
				if len(v.AssignmentLandings) == 0 {
					v.advanceCourse()
				} else {
					out.Records, err = syncAssignmentHeaders(ctx, c, &v)
					if err != nil {
						return contract.Page{}, err
					}
				}
			}
		}
		v.skipExcludedCourses(ctx)
		out.HasMore = v.Index < len(v.Courses)
	case discussionResource:
		if raw == "" {
			v.Courses, e = fetchCourses(ctx, c)
			if e != nil {
				return out, e
			}
		}
		v.skipExcludedCourses(ctx)
		if v.Index < len(v.Courses) {
			out.Records, e = syncDiscussion(ctx, c, &v)
			if e != nil {
				return contract.Page{}, e
			}
		}
		v.skipExcludedCourses(ctx)
		out.HasMore = v.Index < len(v.Courses)
	case contract.Notice:
		out.Records, v.Last, out.HasMore, e = notices(ctx, c, v.Last)
		if e != nil {
			return out, e
		}
	case contract.Exam:
		b, err := request(ctx, c, http.MethodGet, api+"/exam-ans/exam/test/examcode/examlist", nil)
		if err != nil {
			return out, err
		}
		out.Records, e = exams(b)
		if e != nil {
			return out, e
		}
	}
	if out.HasMore {
		out.NextCursor = encode(v)
		if len(out.NextCursor) > 1<<20 {
			return contract.Page{}, contract.ErrParse
		}
	}
	out.Session = save(s, j)
	return out, nil
}
func fetchCourses(ctx context.Context, c *http.Client) ([]course, error) {
	b, e := request(ctx, c, http.MethodGet, api+"/mycourse/backclazzdata?view=json&mcode=&rss=1", nil)
	if e != nil {
		return nil, e
	}
	return parseCourses(b)
}

func parseCourses(b []byte) ([]course, error) {
	if e := checked(b); e != nil {
		return nil, e
	}
	var v struct {
		Result      int
		ChannelList *[]struct {
			CPI     json.RawMessage
			Content struct {
				ID     json.RawMessage
				CPI    json.RawMessage
				Course struct {
					Data []struct {
						ID      json.RawMessage
						Name    string
						Teacher string `json:"teacherfactor"`
					}
				}
			}
		}
	}
	if json.Unmarshal(b, &v) != nil || v.Result != 1 || v.ChannelList == nil {
		return nil, contract.ErrParse
	}
	out := []course{}
	seen := map[string]bool{}
	for _, ch := range *v.ChannelList {
		for _, d := range ch.Content.Course.Data {
			co := course{ID: scalar(d.ID), Class: scalar(ch.Content.ID), CPI: scalar(ch.Content.CPI), Name: d.Name, Teacher: d.Teacher}
			if co.CPI == "" {
				co.CPI = scalar(ch.CPI)
			}
			if co.ID == "" || co.Class == "" || co.CPI == "" || co.Name == "" {
				return nil, contract.ErrParse
			}
			if !seen[co.key()] {
				out = append(out, co)
				seen[co.key()] = true
			}
		}
	}
	if len(out) > 500 {
		return nil, contract.ErrParse
	}
	return out, nil
}
func scalar(b json.RawMessage) string {
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(b, &n) == nil {
		return n.String()
	}
	return ""
}
func record(r contract.Resource, id, parent, title, content string, f map[string]any) contract.Record {
	return contract.Record{Kind: r, ExternalID: id, ParentID: parent, Title: title, Content: content, Fields: f, ObservedAt: time.Now().UTC()}
}
func rrIDs(rows []contract.Record) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ExternalID)
	}
	return ids
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func document(b []byte) (*html.Node, error) {
	if e := checked(b); e != nil {
		return nil, e
	}
	d, e := html.Parse(bytes.NewReader(b))
	if e != nil {
		return nil, contract.ErrParse
	}
	return d, nil
}
func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		walk(ch, fn)
	}
}
func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func class(n *html.Node, key string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == key {
			return true
		}
	}
	return false
}
func text(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	if n.Data == "script" || n.Data == "style" {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(text(c))
		b.WriteByte(' ')
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
func hidden(b []byte) (map[string]string, error) {
	d, e := html.Parse(bytes.NewReader(b))
	if e != nil {
		return nil, contract.ErrParse
	}
	m := map[string]string{}
	walk(d, func(n *html.Node) {
		if n.Data == "input" {
			key := attr(n, "id")
			if key == "" {
				key = attr(n, "name")
			}
			m[key] = attr(n, "value")
		}
	})
	return m, nil
}

var workRatio = regexp.MustCompile(`(?:^|\s)([0-9]+)\s*/\s*([0-9]+)(?:\s|$)`)
var progressRatio = regexp.MustCompile(`已完成任务点\s*[:：]\s*([0-9]+)\s*/\s*([0-9]+)`)

func assignments(b []byte, co course) ([]contract.Record, int, error) {
	d, e := document(b)
	if e != nil {
		return nil, 0, e
	}
	out := []contract.Record{}
	seen := map[string]bool{}
	bad := false
	walk(d, func(n *html.Node) {
		if n.Data != "li" || attr(n, "data") == "" {
			return
		}
		u, err := url.Parse(attr(n, "data"))
		if err != nil || !strings.Contains(u.Path, "/work/task") {
			return
		}
		q := u.Query()
		id := q.Get("workId")
		if id == "" || q.Get("courseId") != co.ID || q.Get("classId") != co.Class {
			bad = true
			return
		}
		if seen[id] {
			return
		}
		seen[id] = true
		label := strings.SplitN(attr(n, "aria-label"), ";", 2)
		if len(label) != 2 || strings.TrimSpace(label[0]) == "" {
			bad = true
			return
		}
		f := courseFields(co)
		f["status"] = strings.TrimSpace(label[1])
		content := assignmentMetadata(n, f)
		out = append(out, record(contract.Assignment, id, co.key(), strings.TrimSpace(label[0]), content, f))
	})
	if bad {
		return nil, 0, contract.ErrParse
	}
	m := workRatio.FindStringSubmatch(text(d))
	total := -1
	if len(m) == 3 {
		total, _ = strconv.Atoi(m[2])
	}
	if total < 0 && len(out) == 0 && (strings.Contains(text(d), "暂无作业") || strings.Contains(text(d), "没有作业")) {
		total = 0
	}
	// No guessed page size or fabricated end-of-feed on missing pagination evidence.
	if total < 0 || total > 10000 || len(out) > 500 || (len(out) == 0 && total != 0) {
		return nil, 0, contract.ErrParse
	}
	return out, total, nil
}
func notices(ctx context.Context, c *http.Client, last string) ([]contract.Record, string, bool, error) {
	form := url.Values{"type": {"0"}, "notice_type": {"0"}, "lastValue": {last}, "sort": {"0"}}
	for _, k := range []string{"folderUUID", "kw", "startTime", "endTime", "gKw", "gName", "year", "tag", "fidsCode", "filterSenderPuids", "filterTags"} {
		form.Set(k, "")
	}
	b, e := request(ctx, c, http.MethodPost, "https://notice.chaoxing.com/pc/notice/getNoticeList", form)
	if e != nil {
		return nil, "", false, e
	}
	if e = checked(b); e != nil {
		return nil, "", false, e
	}
	var v struct {
		Status  *bool
		Notices *struct {
			List      *[]map[string]json.RawMessage
			LastGetID string
			LastPage  json.RawMessage
		}
	}
	if json.Unmarshal(b, &v) != nil || v.Status == nil {
		return nil, "", false, contract.ErrParse
	}
	if !*v.Status {
		return nil, "", false, contract.ErrAuthRequired
	}

	if v.Notices == nil {
		// The official continuation endpoint returns exactly {"status":true}
		// after its final data page, whose lastPage may still be 0. Only accept
		// this observed terminal envelope after a real continuation cursor.
		var envelope map[string]json.RawMessage
		if last != "" && json.Unmarshal(b, &envelope) == nil && len(envelope) == 1 {
			return []contract.Record{}, "", false, nil
		}
		return nil, "", false, contract.ErrParse
	}
	if v.Notices.List == nil {
		return nil, "", false, contract.ErrParse
	}
	n := v.Notices
	end := string(n.LastPage) == "true" || string(n.LastPage) == "1"
	if !end && string(n.LastPage) != "false" && string(n.LastPage) != "0" {
		return nil, "", false, contract.ErrParse
	}
	if !end && (n.LastGetID == "" || n.LastGetID == last) {
		return nil, "", false, contract.ErrParse
	}
	if len(*n.List) > 500 {
		return nil, "", false, contract.ErrParse
	}
	out := []contract.Record{}
	for _, it := range *n.List {
		id := scalar(it["uuid"])
		if id == "" {
			id = scalar(it["idCode"])
		}
		if id == "" {
			id = scalar(it["id"])
		}
		if id == "" {
			return nil, "", false, contract.ErrParse
		}
		title := scalar(it["title"])
		content := scalar(it["content"])
		if strings.Contains(content, "<") {
			d, err := html.Parse(strings.NewReader(content))
			if err != nil {
				return nil, "", false, contract.ErrParse
			}
			content = text(d)
		}
		f := map[string]any{}
		for _, k := range []string{"sendTime", "insertTime", "updateTime", "isread", "sourceType", "senderName", "sendName", "createrName", "creatorName"} {
			if v := scalar(it[k]); v != "" {
				f[k] = v
			}
		}
		f["coverage"], f["body_status"] = "account_notice_list", "source_text"
		aliasAuthor(f, "senderName", "sendName", "createrName", "creatorName")
		aliasTime(f, "published_at", "sendTime")
		aliasTime(f, "updated_at", "updateTime")
		if _, ok := f["sendTime"]; ok {
			f["published_at_semantics"] = "notice_send_time"
		}
		// insertTime is storage insertion evidence, not an inferred update or send.
		if raw := scalarText(f["insertTime"]); raw != "" {
			timeEvidence(f, "inserted_at", raw, "insertTime")
		}
		out = append(out, record(contract.Notice, id, "", title, content, f))
	}
	return out, n.LastGetID, !end, nil
}
func exams(b []byte) ([]contract.Record, error) {
	d, e := document(b)
	if e != nil {
		return nil, e
	}
	out := []contract.Record{}
	bad := false
	table := false
	walk(d, func(n *html.Node) {
		if n.Data == "table" {
			table = true
		}
		if n.Data != "tr" || !class(n, "dataTr") {
			return
		}
		cols := []string{}
		walk(n, func(c *html.Node) {
			if c.Data == "td" {
				cols = append(cols, text(c))
			}
		})
		if len(cols) < 9 {
			bad = true
			return
		}
		// Read-only identity: prefer the native exam ID from attributes/links. When
		// absent, stable schedule text is a synthetic observation key, never row index.
		id := attr(n, "examid")
		walk(n, func(c *html.Node) {
			if id != "" {
				return
			}
			for _, k := range []string{"href", "data"} {
				u, err := url.Parse(attr(c, k))
				if err == nil {
					for _, qk := range []string{"examId", "examid", "testid"} {
						if v := u.Query().Get(qk); v != "" {
							id = v
							break
						}
					}
				}
			}
		})
		identity := "native"
		if id == "" {
			id = "schedule:" + digest([]byte(cols[1]+"\x00"+cols[2]))
			identity = "schedule_text_hash"
		}
		f := map[string]any{"time_raw": cols[2], "duration_raw": cols[3], "availability_status": cols[4], "submission_status": cols[5], "score_raw": cols[6], "terminal_raw": cols[7], "identity_source": identity}
		f["coverage"], f["body_status"], f["body_reason"] = "account_exam_schedule", "not_collected", "exam_not_opened"
		f["time_source"], f["time_semantics"] = "exam_schedule:time_column", "exam_availability_range"
		f["time_precision"], f["time_reason"] = "raw_range", "range_requires_central_normalization"
		missingTime(f, "published_at", "not_provided_by_source")
		out = append(out, record(contract.Exam, id, "account-exam-schedule", cols[1], "", f))
	})
	if bad || len(out) > 500 || (!table && !strings.Contains(text(d), "暂无考试")) {
		return nil, contract.ErrParse
	}
	return out, nil
}
