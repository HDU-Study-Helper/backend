package mooc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"academic/backend/internal/contract"
)

type course struct {
	ID     string
	Term   string
	School string
	Name   string
	Type   int
}

func key(parts ...string) string {
	b, _ := json.Marshal(parts)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (c course) ref() string { return key(c.ID, c.Term) }
func (c course) referer() string {
	p := "/learn/"
	if c.Type == 2 {
		p = "/spoc/learn/"
	}
	return origin + p + url.PathEscape(c.School+"-"+c.ID) + "?tid=" + url.QueryEscape(c.Term)
}

type cursor struct {
	Version  int
	Source   string
	Scope    contract.Scope
	Resource contract.Resource
	Type     int
	Page     int
	Courses  []course
	Index    int
	Done     bool
	Previous string
}

func newCursor(scope contract.Scope, r contract.Resource, raw string) (cursor, error) {
	c := cursor{Version: 1, Source: "mooc", Scope: scope, Resource: r, Type: 1, Page: 1}
	if raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || len(b) > 32768 || json.Unmarshal(b, &c) != nil {
			return c, contract.ErrInvalid
		}
	}
	if c.Version != 1 || c.Source != "mooc" || c.Scope != scope || c.Resource != r || c.Type < 1 || c.Type > 2 || c.Page < 1 || c.Page > 100 || len(c.Courses) > 8 || c.Index < 0 || c.Index > len(c.Courses) {
		return c, contract.ErrInvalid
	}
	for _, x := range c.Courses {
		if !validID(x.ID) || !validID(x.Term) || x.Type < 1 || x.Type > 2 || len(x.School) > 256 {
			return c, contract.ErrInvalid
		}
	}
	return c, nil
}
func validID(s string) bool {
	if s == "" || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
func (c *cursor) advance(more bool) error {
	if more {
		if c.Page == 100 {
			return contract.ErrParse
		}
		c.Page++
		return nil
	}
	c.Previous = ""
	if c.Type == 1 {
		c.Type = 2
		c.Page = 1
	} else {
		c.Done = true
	}
	return nil
}
func (a *adapter) Sync(ctx context.Context, scope contract.Scope, session contract.Session, r contract.Resource, raw string) (contract.Page, error) {
	if e := ctx.Err(); e != nil {
		return contract.Page{}, e
	}
	if r != contract.Course && r != contract.Assignment && r != contract.Exam && r != contract.Progress {
		return contract.Page{}, contract.ErrUnsupported
	}
	s, h, e := a.restore(scope, session)
	if e != nil {
		return contract.Page{}, e
	}
	if !s.Authorized {
		return contract.Page{}, contract.ErrAuthRequired
	}
	c, e := newCursor(scope, r, raw)
	if e != nil {
		return contract.Page{}, e
	}
	records := []contract.Record{}
	if c.Index < len(c.Courses) {
		co := c.Courses[c.Index]
		dto, e := rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", co.referer(), url.Values{"termId": {co.Term}})
		if e != nil {
			return contract.Page{}, e
		}
		records, e = termRecords(dto, co, r)
		if e != nil {
			return contract.Page{}, e
		}
		c.Index++
		if c.Index == len(c.Courses) {
			c.Courses = nil
			c.Index = 0
		}
	} else if !c.Done {
		found, more, fingerprint, e := coursePage(ctx, h, c.Type, c.Page)
		if e != nil {
			return contract.Page{}, e
		}
		if fingerprint == c.Previous && len(found) > 0 {
			return contract.Page{}, contract.ErrParse
		}
		c.Previous = fingerprint
		if e = c.advance(more); e != nil {
			return contract.Page{}, e
		}
		if r == contract.Course {
			for _, co := range found {
				records = append(records, courseRecord(co))
			}
		} else {
			c.Courses = found
			c.Index = 0
		}
	}
	out := contract.Page{Records: records, HasMore: !c.Done || c.Index < len(c.Courses)}
	if out.HasMore {
		b, _ := json.Marshal(c)
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
		if out.NextCursor == raw {
			return contract.Page{}, contract.ErrParse
		}
	}
	out.Session, e = save(s, h)
	if e != nil {
		return contract.Page{}, e
	}
	return out, nil
}
func coursePage(ctx context.Context, h *http.Client, typ, page int) ([]course, bool, string, error) {
	result, e := rpc(ctx, h, "learnerCourseRpcBean.getMyLearnedCoursePanelList", origin+"/", url.Values{"type": {"30"}, "p": {strconv.Itoa(page)}, "psize": {"8"}, "courseType": {strconv.Itoa(typ)}})
	if e != nil {
		return nil, false, "", e
	}
	rows, ok := first(result, "result", "list").([]any)
	if !ok || len(rows) > 8 {
		return nil, false, "", contract.ErrParse
	}
	courses := make([]course, 0, len(rows))
	for _, v := range rows {
		m := object(v)
		term := object(m["termPanel"])
		school := object(m["schoolPanel"])
		co := course{ID: text(first(m, "id", "courseId")), Term: text(first(term, "id", "termId")), School: text(first(school, "shortName", "schoolSN")), Name: text(m["name"]), Type: typ}
		if !validID(co.ID) || !validID(co.Term) || co.School == "" || len(co.School) > 256 {
			return nil, false, "", contract.ErrParse
		}
		courses = append(courses, co)
	}
	more := len(rows) == 8
	paging := object(first(result, "pagination", "query"))
	if v := first(paging, "totlePageCount", "totalPageCount"); v != nil {
		n, e := strconv.Atoi(text(v))
		if e != nil || n < 0 || n > 100 || page > n && len(rows) > 0 {
			return nil, false, "", contract.ErrParse
		}
		more = page < n
	}
	if len(rows) == 0 && more {
		return nil, false, "", contract.ErrParse
	}
	b, _ := json.Marshal(courses)
	sum := sha256.Sum256(b)
	return courses, more, hex.EncodeToString(sum[:]), nil
}
func courseRecord(c course) contract.Record {
	return contract.Record{Kind: contract.Course, ExternalID: c.ref(), Title: c.Name, ObservedAt: time.Now().UTC(), Fields: map[string]any{"courseId": c.ID, "termId": c.Term, "courseType": c.Type, "school": c.School}}
}

// Only selected scalar source metadata can leave the adapter. Questions, answers,
// cookies, arbitrary HTML, signed links and embedded raw DTOs are never copied.
var metadataKeys = []string{"contentType", "type", "submitStatus", "finishStatus", "statusText", "statusName", "studentScore", "userScore", "finalMark", "usedTryCount", "submitEndTime", "deadline", "endTime", "examEndTime", "testEndTime", "homeworkEndTime", "jobDeadline", "closeTime", "startTime", "examStartTime", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "enableEvaluation", "scorePubStatus", "peerReviewStatus", "evaluationStatusText", "finished", "learned", "learnStatus", "progress"}

func scalar(v any) bool {
	switch v.(type) {
	case string, json.Number, bool:
		return true
	}
	return false
}
func metadata(m map[string]any, nested map[string]any) map[string]any {
	f := map[string]any{}
	for _, k := range metadataKeys {
		v := first(m, k)
		if v == nil {
			v = first(nested, k)
		}
		if scalar(v) {
			f[k] = v
		}
	}
	return f
}
func termRecords(result map[string]any, co course, r contract.Resource) ([]contract.Record, error) {
	dto := object(result["mocTermDto"])
	if dto == nil {
		dto = result
	}
	// A successful code without an identifiable directory must not become an
	// empty successful term. Explicit chapters:[] is a valid, limited observation.
	if _, ok := dto["chapters"].([]any); !ok {
		return nil, contract.ErrParse
	}
	if id := text(first(dto, "id", "termId")); id != "" && id != co.Term {
		return nil, contract.ErrParse
	}
	out := []contract.Record{}
	seen := map[string]bool{}
	visited := 0
	if r == contract.Progress {
		if last := first(result, "lastLearnUnitId"); scalar(last) {
			out = append(out, contract.Record{Kind: r, ParentID: co.ref(), ExternalID: key(co.ID, co.Term, "lastLearnUnitId"), Fields: map[string]any{"courseId": co.ID, "termId": co.Term, "lastLearnUnitId": last, "coverage": "last visited unit only"}, ObservedAt: time.Now().UTC()})
		}
	}
	var walk func(any, string, string, int) error
	walk = func(v any, collection, unit string, depth int) error {
		visited++
		if depth > 24 || visited > 10000 {
			return contract.ErrParse
		}
		if arr, ok := v.([]any); ok {
			for _, x := range arr {
				if e := walk(x, collection, unit, depth+1); e != nil {
					return e
				}
			}
			return nil
		}
		m := object(v)
		if m == nil {
			return contract.ErrParse
		}
		if collection == "units" {
			unit = text(first(m, "unitId", "id"))
			if unit == "" {
				return contract.ErrParse
			}
		}
		if explicit := text(m["unitId"]); explicit != "" {
			unit = explicit
		}
		nested := object(m["test"])
		task := ""
		switch collection {
		case "quizs", "quizzes":
			task = "quiz"
		case "homeworks":
			task = "homework"
		case "exams":
			task = "exam"
		}
		// Type values only classify task nodes, never chapters or lessons. A plain
		// contentType=3 unit can be a document in legacy DTOs, so require test metadata.
		if collection == "units" && task == "" {
			t := text(first(nested, "type"))
			if t == "" {
				t = text(m["contentType"])
			}
			switch t {
			case "2":
				task = "quiz"
			case "6":
				task = "exam"
			case "3":
				if nested != nil {
					task = "homework"
				}
			}
		}
		f := metadata(m, nested)
		personal := false
		for _, k := range []string{"submitStatus", "finishStatus", "statusText", "statusName", "studentScore", "userScore", "finalMark", "usedTryCount", "finished", "learned", "learnStatus", "progress"} {
			if _, ok := f[k]; ok {
				personal = true
			}
		}
		emit := r == contract.Assignment && (task == "quiz" || task == "homework") || r == contract.Exam && task == "exam" || r == contract.Progress && personal && (task != "" || collection == "units")
		if emit {
			content := text(first(m, "contentId", "quizId", "homeworkId", "testId"))
			if content == "" {
				content = text(first(nested, "contentId", "id"))
			}
			id := text(first(m, "id", "unitId", "quizId", "homeworkId", "testId"))
			if id == "" {
				id = content
			}
			if id == "" {
				return contract.ErrParse
			}
			external := key(co.ID, co.Term, unit, content, id)
			if !seen[external] {
				seen[external] = true
				f["courseId"] = co.ID
				f["termId"] = co.Term
				f["unitId"] = unit
				f["contentId"] = content
				f["courseType"] = co.Type
				if task != "" {
					f["task_type"] = task
				}
				f["status_interpretation"] = "source values only; completion unknown unless explicit"
				out = append(out, contract.Record{Kind: r, ExternalID: external, ParentID: co.ref(), Title: text(first(m, "name", "title", "unitName")), Fields: f, ObservedAt: time.Now().UTC()})
			}
		}
		for _, k := range []string{"chapters", "lessons", "units", "quizs", "quizzes", "homeworks", "exams"} {
			if child, ok := m[k]; ok {
				if _, ok := child.([]any); !ok {
					return contract.ErrParse
				}
				if e := walk(child, k, unit, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(dto, "", "", 0); e != nil {
		return nil, e
	}
	if len(out) > 2000 {
		return nil, contract.ErrParse
	}
	return out, nil
}
