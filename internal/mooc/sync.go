package mooc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"academic/backend/internal/contract"
)

type course struct {
	ID           string
	Term         string
	School       string
	Name         string
	Type         int
	SourceTerm   string
	ActiveTerm   string
	Discovery    string
	Metadata     map[string]any
	Discovered   bool
	ActiveRead   bool
	InfoPending  bool
	SourceSchool string
	CurrentUser  string // official webUser.id; cursor only, never projected as identity
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
	name := c.ID
	if c.School != "" {
		name = c.School + "-" + c.ID
	}
	return origin + p + url.PathEscape(name) + "?tid=" + url.QueryEscape(c.Term)
}

type cursor struct {
	Version       int
	Source        string
	Scope         contract.Scope
	Resource      contract.Resource
	Type          int
	Page          int
	Courses       []course
	Index         int
	Done          bool
	Previous      string
	Pending       []contract.Record
	PendingCourse course
	SourceSchools map[string]string
	Discussion    *discussionCursor
}

func newCursor(scope contract.Scope, r contract.Resource, raw string) (cursor, error) {
	c := cursor{Version: 1, Source: "mooc", Scope: scope, Resource: r, Type: 1, Page: 1}
	if raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || len(b) > 1<<20 {
			return c, contract.ErrInvalid
		}
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		var trailing any
		if decoder.Decode(&c) != nil || decoder.Decode(&trailing) != io.EOF {
			return c, contract.ErrInvalid
		}
	}
	if c.Version != 1 && !(r == "discussion" && c.Version == 2) || c.Source != "mooc" || c.Scope != scope || c.Resource != r || c.Type < 1 || c.Type > 2 || c.Page < 1 || c.Page > 100 || len(c.Pending) > 2000 || len(c.Courses) > 8 || c.Index < 0 || c.Index > len(c.Courses) {
		return c, contract.ErrInvalid
	}
	if c.Discussion != nil && !validDiscussionCursor(c.Discussion) {
		return c, contract.ErrInvalid
	}
	if len(c.SourceSchools) > 1600 {
		return c, contract.ErrInvalid
	}
	for id, school := range c.SourceSchools {
		if !validID(id) || len(school) > 256 || strings.ContainsAny(school, "/:?#\\\r\n") {
			return c, contract.ErrInvalid
		}
	}
	validationCourses := append([]course{}, c.Courses...)
	if len(c.Pending) > 0 || c.Discussion != nil {
		validationCourses = append(validationCourses, c.PendingCourse)
	}
	for _, x := range validationCourses {
		if !validID(x.ID) || !validID(x.Term) || x.SourceTerm != "" && !validID(x.SourceTerm) || x.ActiveTerm != "" && !validID(x.ActiveTerm) || x.Type < 1 || x.Type > 2 || len(x.School) > 256 || len(x.Name) > 512 || x.CurrentUser != "" && !validID(x.CurrentUser) {
			return c, contract.ErrInvalid
		}
	}
	// The short-lived discussion v2 used the same wire state. Accept and
	// normalize it without restarting discovery or discarding pending replies.
	c.Version = 1
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

// nextCourse consumes the saved discovery slot without changing the authority of
// the remaining indices. A later full scan discovers excluded courses again.
func (c *cursor) nextCourse() {
	c.Index++
	if c.Index == len(c.Courses) {
		c.Courses = nil
		c.Index = 0
	}
}

func (a *adapter) Sync(ctx context.Context, scope contract.Scope, session contract.Session, r contract.Resource, raw string) (contract.Page, error) {
	if e := ctx.Err(); e != nil {
		return contract.Page{}, e
	}
	if r != contract.Course && r != contract.Assignment && r != contract.Exam && r != contract.Progress && r != "chapter" && r != "lesson" && r != "material" && r != "discussion" && r != contract.Notice {
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
	if c.Discussion != nil {
		if contract.CourseExcluded(ctx, scope, c.PendingCourse.ref()) {
			c.Discussion = nil
			c.Pending = nil
			return syncPage(c, records, raw, s, h)
		}
		records, e = advanceDiscussion(ctx, h, c.PendingCourse, &c)
		if e != nil {
			return contract.Page{}, e
		}
		return syncPage(c, records, raw, s, h)
	}
	if len(c.Pending) > 0 {
		// Pending rows and active-source bodies retain the original route owner.
		// Recheck fresh policy before emitting even already materialized rows.
		if contract.CourseExcluded(ctx, scope, c.PendingCourse.ref()) {
			c.Pending = nil
			c.PendingCourse = course{}
			return syncPage(c, records, raw, s, h)
		}
		if needsUnitBody(c.Pending[0]) {
			row := c.Pending[0]
			if e = enrichUnit(ctx, h, c.PendingCourse, &row); e != nil {
				return contract.Page{}, e
			}
			c.Pending = c.Pending[1:]
			if row.Kind == "discussion" {
				c.Discussion = &discussionCursor{Root: row, Page: 1, Seen: map[string]bool{}}
			} else {
				records = append(records, row)
			}
		} else {
			count := 0
			for count < len(c.Pending) && count < 500 && !needsUnitBody(c.Pending[count]) {
				count++
			}
			records = append(records, c.Pending[:count]...)
			c.Pending = c.Pending[count:]
		}
		return syncPage(c, records, raw, s, h)
	}
	if c.Index < len(c.Courses) {
		co := c.Courses[c.Index]
		// The native external key belongs to the selected route course/term,
		// including when SourceTerm points at a distinct active source term.
		// Consume one slot per call, before discovery, metadata, notices or DTOs.
		if contract.CourseExcluded(ctx, scope, co.ref()) {
			c.nextCourse()
			return syncPage(c, records, raw, s, h)
		}
		if (co.Type == 2 || r == contract.Course || r == "discussion") && !co.Discovered {
			co, e = discoverCourse(ctx, h, co)
			if e != nil {
				return contract.Page{}, e
			}
			co.Discovered = true
			c.Courses[c.Index] = co
			if r == contract.Course {
				records = append(records, courseRecord(co))
			}
			return syncPage(c, records, raw, s, h)
		}
		if r == contract.Course && co.InfoPending {
			sourceID := text(co.Metadata["sourceCourseId"])
			if sourceID != co.ID {
				co.SourceSchool = c.SourceSchools[sourceID]
			}
			co, e = readCourseInfo(ctx, h, co)
			if e != nil {
				return contract.Page{}, e
			}
			co.InfoPending = false
			c.Courses[c.Index] = co
			records = append(records, courseRecord(co))
			if co.ActiveTerm != "" && co.ActiveTerm != co.Term && !co.ActiveRead {
				co.SourceTerm = co.ActiveTerm
				co.ActiveRead = true
				c.Courses[c.Index] = co
			} else {
				c.Index++
			}
			if c.Index == len(c.Courses) {
				c.Courses = nil
				c.Index = 0
			}
			return syncPage(c, records, raw, s, h)
		}
		if co.Type != 2 {
			co.Discovery = "not_applicable"
		}
		var dto map[string]any
		if r == contract.Notice {
			v, err := readAnnouncements(ctx, h, co)
			if err != nil {
				return contract.Page{}, err
			}
			records, e = noticeRecords(v, co)
		} else {
			dto, e = rpc(ctx, h, "courseBean.getLastLearnedMocTermDto", co.referer(), url.Values{"termId": {sourceTerm(co)}})
		}
		if e != nil {
			return contract.Page{}, e
		}
		if r != contract.Notice {
			if co.Metadata == nil {
				co.Metadata = map[string]any{}
			}
			termDTO := object(dto["mocTermDto"])
			if termDTO == nil {
				termDTO = dto
			}
			co.Metadata["sourceCourseId"] = text(termDTO["courseId"])
			if r == contract.Course && co.SourceSchool == "" {
				co.SourceSchool = co.School
			}
			records, e = termRecords(dto, co, r)
		}
		if e != nil {
			return contract.Page{}, e
		}
		if r == contract.Course {
			metadataRecord := records[0]
			co.Metadata = metadataRecord.Fields
			co.InfoPending = true
			c.Courses[c.Index] = co
			return syncPage(c, records, raw, s, h)
		}
		if r == "material" || r == "discussion" {
			safe := []contract.Record{}
			for _, row := range records {
				if needsUnitBody(row) {
					c.Pending = append(c.Pending, row)
				} else {
					safe = append(safe, row)
				}
			}
			c.PendingCourse = co
			records = safe
		}
		if len(records) > 500 {
			overflow := append([]contract.Record{}, records[500:]...)
			c.Pending = append(overflow, c.Pending...)
			c.PendingCourse = co
			records = records[:500]
		}
		if co.ActiveTerm != "" && co.ActiveTerm != co.Term && !co.ActiveRead {
			co.SourceTerm = co.ActiveTerm
			co.ActiveRead = true
			c.Courses[c.Index] = co
		} else {
			c.Index++
		}
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
			schools := map[string]string{}
			for _, co := range found {
				if co.School != "" {
					schools[co.ID] = co.School
				}
			}
			if len(schools) > 0 {
				if c.SourceSchools == nil {
					c.SourceSchools = map[string]string{}
				}
				for k, v := range schools {
					c.SourceSchools[k] = v
				}
			}
		}
		for i := range found {
			found[i].SourceSchool = found[i].School
		}
		c.Courses = found
		c.Index = 0
	}
	return syncPage(c, records, raw, s, h)
}
func needsUnitBody(row contract.Record) bool {
	typ := text(row.Fields["contentType"])
	return (row.Kind == "material" || row.Kind == "discussion") && row.Fields["source_collection"] == "units" && (typ == "3" || typ == "4" || typ == "6")
}
func syncPage(c cursor, records []contract.Record, raw string, s *state, h *http.Client) (contract.Page, error) {
	var e error
	out := contract.Page{Records: records, HasMore: !c.Done || c.Index < len(c.Courses) || len(c.Pending) > 0 || c.Discussion != nil}
	if out.HasMore {
		b, err := json.Marshal(c)
		if err != nil || len(b) > 1<<20 {
			return contract.Page{}, contract.ErrParse
		}
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
		if !validID(co.ID) || !validID(co.Term) || len(co.School) > 256 {
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
