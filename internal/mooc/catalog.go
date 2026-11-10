package mooc

import (
	"encoding/json"
	"strings"
	"time"

	"academic/backend/internal/contract"
	"golang.org/x/net/html"
)

// Copy only selected source scalars. Native timestamps retain their original unit.
var metadataKeys = []string{"contentType", "type", "position", "releaseTime", "gmtCreate", "gmtModified", "firstPublishTime", "published", "publishStatus", "visible", "viewStatus", "webVisible", "draftStatus", "testDraftStatus", "isTestChecked", "duration", "durationInSeconds", "learnTime", "learnCount", "totalLearnedTime", "completePercent", "contentFileName", "submitStatus", "finishStatus", "statusText", "statusName", "studentScore", "userScore", "finalMark", "totalScore", "bonusScore", "testScore", "usedTryCount", "trytime", "tryTime", "testTime", "examTime", "questionCount", "submitEndTime", "deadline", "endTime", "examEndTime", "testEndTime", "homeworkEndTime", "jobDeadline", "closeTime", "startTime", "examStartTime", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "evaluateJudgeType", "evaluateNeedTrain", "enableEvaluation", "scorePubStatus", "peerReviewStatus", "evaluationStatusText", "finished", "learned", "learnStatus", "progress", "replyCount", "voteCount", "examId", "examType", "objectTestId", "subjectTestId", "submitTestCount", "scoreReleaseTime", "scoreReleaseType", "userSubmitStatus", "userEffectStatus", "submitTime", "evaluateStatus", "userEvaluateStatus", "evaluateCount", "evaluatedCount"}

func scalar(v any) bool {
	switch v.(type) {
	case string, json.Number, bool:
		return true
	}
	return false
}
func metadata(m, n map[string]any) map[string]any {
	f := map[string]any{}
	for _, k := range metadataKeys {
		v := first(m, k)
		if v == nil {
			v = first(n, k)
		}
		if scalar(v) {
			if s, ok := v.(string); ok && (len(s) > 4096 || strings.Contains(s, "://")) {
				continue
			}
			f[k] = v
		}
	}
	return f
}
func sourceTerm(c course) string {
	if c.SourceTerm != "" {
		return c.SourceTerm
	}
	return c.Term
}
func sourceFields(c course) map[string]any {
	scope := "route"
	if sourceTerm(c) != c.Term {
		scope = "active_source"
	}
	return map[string]any{"courseId": c.ID, "termId": c.Term, "course_name": c.Name, "courseType": c.Type, "routeCourseId": c.ID, "routeTermId": c.Term, "sourceTermId": sourceTerm(c), "activeTermId": c.ActiveTerm, "source_scope": scope, "courseRef": c.ref(), "source_endpoint": "courseBean.getLastLearnedMocTermDto", "coverage": "partial", "coverage_reason": "visible source catalog only; unpublished and inaccessible content unknown", "body_status": "not_requested"}
}
func mergeFields(dst, src map[string]any) {
	for k, v := range src {
		dst[k] = v
	}
}
func courseRecord(c course) contract.Record {
	f := sourceFields(c)
	f["school"] = c.School
	f["source_term_discovery"] = c.Discovery
	f["notice_coverage"] = "partial: term announcements via verified CourseBean.getAllAnnouncementByTerm; teacher/global announcements excluded"
	f["material_body_coverage"] = "partial: verified type4 rich text body, type3 PDF metadata; PDF/media bytes excluded"
	f["discussion_body_coverage"] = "partial: verified type6 root post, detail state and bounded reply pages; nested comments and participant profiles excluded"
	f["assessment_body_coverage"] = "unsupported: papers are never opened"
	mergeFields(f, c.Metadata)
	return contract.Record{Kind: contract.Course, ExternalID: c.ref(), Title: c.Name, Content: text(f["course_introduction"]), Fields: f, ObservedAt: time.Now().UTC()}
}

type location struct {
	chapter, lesson, unit string
	ids, titles           []string
}

func nativeID(m map[string]any, keys ...string) string { return text(first(m, keys...)) }
func classify(collection string, m map[string]any) (contract.Resource, string, string) {
	test := object(m["test"])
	switch collection {
	case "quizs", "quizzes":
		return contract.Assignment, "quiz", collection + "_collection"
	case "homeworks":
		return contract.Assignment, "homework", "homeworks_collection"
	case "exams", "exam":
		return contract.Exam, "exam", "exams_collection"
	}
	if collection != "units" {
		return "", "", ""
	}
	if test != nil {
		switch text(test["type"]) {
		case "2":
			return contract.Assignment, "quiz", "test.type"
		case "3":
			return contract.Assignment, "homework", "test.type"
		case "6":
			return contract.Exam, "exam", "test.type"
		}
	}
	// Unit and assessment enums are different. Unit 6 is discussion; unit 5
	// is an assessment placeholder whose exact subtype needs separate evidence.
	switch text(m["contentType"]) {
	case "2":
		if test != nil {
			return contract.Assignment, "quiz", "test_metadata_and_unit.contentType2"
		}
		return contract.Resource("material"), "video_subtitles", "unit.contentType2"
	case "6":
		return contract.Resource("discussion"), "discussion", "unit.contentType6"
	case "1":
		return contract.Resource("material"), "video", "unit.contentType1"
	case "3":
		return contract.Resource("material"), "document", "unit.contentType3"
	case "4":
		return contract.Resource("material"), "rich_text", "unit.contentType4"
	case "5":
		return contract.Resource("material"), "assessment_unknown", "unit.contentType5"
	case "7":
		return contract.Resource("material"), "live", "unit.contentType7"
	case "15":
		return contract.Resource("material"), "interactive", "unit.contentType15"
	}
	return contract.Resource("material"), "unknown", "unrecognized_unit_type"
}
func termRecords(result map[string]any, co course, r contract.Resource) ([]contract.Record, error) {
	dto := object(result["mocTermDto"])
	if dto == nil {
		dto = result
	}
	if _, ok := dto["chapters"].([]any); !ok {
		return nil, contract.ErrParse
	}
	if id := nativeID(dto, "id", "termId"); id != "" && id != sourceTerm(co) {
		return nil, contract.ErrParse
	}
	out := []contract.Record{}
	seen := map[string]bool{}
	visited := 0
	if r == contract.Course {
		c := co
		c.Metadata = metadata(dto, nil)
		mergeFields(c.Metadata, co.Metadata)
		c.Metadata["catalog_chapter_count"] = len(dto["chapters"].([]any))
		c.Metadata["catalog_status"] = "observed"
		if len(dto["chapters"].([]any)) == 0 {
			c.Metadata["catalog_status"] = "empty_visible_directory"
		}
		rec := courseRecord(c)
		if raw := text(dto["description"]); raw != "" {
			body, e := plainBody(raw)
			if e != nil {
				return nil, e
			}
			rec.Content = body
			rec.Fields["course_introduction"] = body
			rec.Fields["body_status"] = "catalog_description"
		}
		for _, k := range []string{"requirements", "teachingGoal", "target", "courseLoad", "courseStyle", "courseName"} {
			if raw := text(dto[k]); raw != "" {
				value, e := plainBody(raw)
				if e != nil {
					return nil, e
				}
				rec.Fields[k] = value
			}
		}
		teachers := []string{}
		for _, k := range []string{"chiefLectorDto", "staffLectorDtos"} {
			v := dto[k]
			if m := object(v); m != nil {
				v = []any{m}
			}
			if rows, ok := v.([]any); ok {
				if len(rows) > 100 {
					return nil, contract.ErrParse
				}
				for _, row := range rows {
					if name := text(first(object(row), "name", "realName")); name != "" && len(name) <= 512 {
						teachers = append(teachers, name)
					}
				}
			}
		}
		if len(teachers) > 0 {
			rec.Fields["teachers"] = teachers
		}
		return []contract.Record{rec}, nil
	}
	if r == contract.Progress {
		if fields := metadata(dto, nil); len(nativeProgress(fields)) > 0 {
			progressCoverage(fields)
			mergeFields(fields, sourceFields(co))
			fields["catalog_node_type"] = "term"
			fields["progress_scope"] = "native term source fields only"
			out = append(out, contract.Record{Kind: r, ParentID: co.ref(), ExternalID: key(co.ID, co.Term, sourceTerm(co), "term_progress"), Fields: fields, ObservedAt: time.Now().UTC()})
		}
		if last := result["lastLearnUnitId"]; scalar(last) {
			f := sourceFields(co)
			f["lastLearnUnitId"] = last
			f["progress_scope"] = "last visited unit only"
			out = append(out, contract.Record{Kind: r, ParentID: co.ref(), ExternalID: key(co.ID, co.Term, sourceTerm(co), "lastLearnUnitId"), Fields: f, ObservedAt: time.Now().UTC()})
		}
	}
	var walk func(any, string, location, int) error
	walk = func(v any, collection string, loc location, depth int) error {
		visited++
		if depth > 24 || visited > 10000 {
			return contract.ErrParse
		}
		if arr, ok := v.([]any); ok {
			for _, x := range arr {
				if e := walk(x, collection, loc, depth+1); e != nil {
					return e
				}
			}
			return nil
		}
		m := object(v)
		if m == nil {
			return contract.ErrParse
		}
		id := nativeID(m, "id", "unitId", "quizId", "homeworkId", "testId")
		name := nativeID(m, "name", "title", "unitName")
		kind, subtype, evidence := classify(collection, m)
		switch collection {
		case "chapters":
			loc.chapter = id
			kind = contract.Resource("chapter")
		case "lessons":
			loc.lesson = id
			kind = contract.Resource("lesson")
		case "units":
			loc.unit = nativeID(m, "unitId", "id")
			if loc.unit == "" {
				return contract.ErrParse
			}
		}
		if collection != "" {
			loc.ids = append(append([]string{}, loc.ids...), id)
			loc.titles = append(append([]string{}, loc.titles...), name)
		}
		if x := nativeID(m, "chapterId"); x != "" {
			loc.chapter = x
		}
		if x := nativeID(m, "lessonId"); x != "" {
			loc.lesson = x
		}
		if x := nativeID(m, "unitId"); x != "" {
			loc.unit = x
		}
		nested := object(m["test"])
		f := metadata(m, nested)
		personal := len(nativeProgress(f)) > 0
		emit := kind == r || r == contract.Progress && personal && progressNode(kind, collection)
		if emit {
			if id == "" {
				return contract.ErrParse
			}
			content := nativeID(m, "contentId", "quizId", "homeworkId", "testId")
			if content == "" {
				content = nativeID(nested, "contentId", "id")
			}
			external := key(co.ID, co.Term, sourceTerm(co), loc.unit, content, id)
			if !seen[external] {
				seen[external] = true
				mergeFields(f, sourceFields(co))
				mergeFields(f, co.Metadata)
				f["nodeId"] = id
				f["chapterId"] = loc.chapter
				f["lessonId"] = loc.lesson
				f["unitId"] = loc.unit
				f["contentId"] = content
				f["path"] = loc.ids
				f["path_titles"] = loc.titles
				f["source_collection"] = collection
				f["catalog_node_type"] = string(kind)
				f["unit_type"] = subtype
				if collection == "units" {
					f["catalog_node_type"] = "unit"
				}
				if kind == "material" || kind == "discussion" {
					f["body_status"] = "unsupported"
					f["body_reason"] = "safe standalone body operation not verified"
				}
				f["assessment_evidence"] = evidence
				f["status_interpretation"] = "source values only; completion unknown unless explicit"
				progressCoverage(f)
				if kind == contract.Assignment || kind == contract.Exam {
					f["task_type"] = subtype
					assessmentTimes(f, m, nested)
				}
				if nested != nil {
					f["test_metadata"] = metadata(nested, nil)
				}
				if collection == "units" && kind == "discussion" {
					f["current_user_participation_fields"] = nativeProgress(f)
					f["current_user_participation_coverage"] = "catalog source fields only; replyCount/voteCount and completePercent are not inferred submission states"
				}
				if collection == "chapters" || collection == "lessons" {
					f["body_status"] = "not_applicable"
				}
				if subtype == "assessment_unknown" {
					f["classification_status"] = "unsupported_assessment_subtype"
				}
				body := ""
				if raw := text(m["description"]); raw != "" {
					var e error
					body, e = plainBody(raw)
					if e != nil {
						return e
					}
					f["body_status"] = "catalog_description"
				}
				if body == "" && (kind == contract.Assignment || kind == contract.Exam) {
					if raw := text(nested["description"]); raw != "" {
						var e error
						body, e = plainBody(raw)
						if e != nil {
							return e
						}
						f["body_status"] = "test_description"
					}
				}
				out = append(out, contract.Record{Kind: r, ExternalID: external, ParentID: co.ref(), Title: name, Content: body, Fields: f, ObservedAt: time.Now().UTC()})
			}
		}
		for _, k := range []string{"chapters", "lessons", "units", "quizs", "quizzes", "homeworks", "exams"} {
			if child, ok := m[k]; ok && child != nil {
				if _, ok := child.([]any); !ok {
					return contract.ErrParse
				}
				if e := walk(child, k, loc, depth+1); e != nil {
					return e
				}
			}
		}
		// Some official DTOs expose a single chapter exam in addition to root exams.
		if child := m["exam"]; child != nil {
			if object(child) == nil {
				return contract.ErrParse
			}
			if e := walk(child, "exam", loc, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if e := walk(dto, "", location{}, 0); e != nil {
		return nil, e
	}
	if len(out) > 2000 {
		return nil, contract.ErrParse
	}
	return out, nil
}

// plainBody retains readable text while dropping executable/embedded elements and
// all links. No signed URL or raw HTML is copied into business records.
func plainBody(raw string) (string, error) {
	if len(raw) > 256<<10 {
		return "", contract.ErrParse
	}
	n, e := html.Parse(strings.NewReader(raw))
	if e != nil {
		return "", contract.ErrParse
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "iframe") {
			return
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	s := strings.Join(strings.Fields(b.String()), " ")
	for _, w := range strings.Fields(s) {
		if strings.Contains(w, "://") {
			s = strings.ReplaceAll(s, w, "[link omitted]")
		}
	}
	return s, nil
}
