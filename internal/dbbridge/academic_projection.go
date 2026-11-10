package dbbridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"academic/backend/internal/model"
	"github.com/jackc/pgx/v5"
)

type academicRaw struct {
	row                 model.PlatformRecord
	title, body, author string
	fields              map[string]any
	occurred, updated   string
}
type academicProjection struct {
	offerings []model.AcademicOffering
	items     []model.AcademicItem
	evidence  map[string][]model.PlatformRecord
}

func academicID(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func academicJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func academicString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}
func academicField(f map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := academicString(f[k]); s != "" {
			return s
		}
	}
	return ""
}
func decodeAcademicRaw(row model.PlatformRecord) (academicRaw, error) {
	var v struct {
		Title    string         `json:"title"`
		Content  string         `json:"content"`
		Author   string         `json:"author"`
		Fields   map[string]any `json:"fields"`
		Occurred string         `json:"occurred_at"`
		Updated  string         `json:"updated_at"`
	}
	d := json.NewDecoder(bytes.NewReader(row.Content))
	d.UseNumber()
	if err := d.Decode(&v); err != nil {
		return academicRaw{}, model.ErrInvalidArgument
	}
	if v.Fields == nil {
		v.Fields = map[string]any{}
	}
	return academicRaw{row, v.Title, v.Content, v.Author, v.Fields, v.Occurred, v.Updated}, nil
}
func academicNative(f map[string]any, source string) (course, class, term string) {
	if source == "chaoxing" {
		return academicField(f, "course_id", "courseId"), academicField(f, "class_id", "classId"), ""
	}
	return academicField(f, "courseId", "course_id"), "", academicField(f, "termId", "term_id", "activeTermId")
}
func academicRef(source, course, class, term string) string {
	if course == "" {
		return ""
	}
	if source == "chaoxing" {
		if class == "" {
			return ""
		}
		return string(academicJSON([]string{course, class}))
	}
	if term == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(academicJSON([]string{course, term}))
}
func academicKind(v academicRaw) (kind, task string) {
	kind, task = v.row.Kind, academicField(v.fields, "task_type")
	if v.row.Source == "mooc" && academicField(v.fields, "contentType") == "6" {
		collection := academicField(v.fields, "source_collection", "collection")
		explicit := academicField(v.fields, "assessment_evidence", "assessmentEvidence", "examId", "assessmentId")
		if explicit == "unit.contentType6" {
			explicit = ""
		}
		assessmentTime := academicField(v.fields, "examStartTime", "examEndTime", "testEndTime", "submitEndTime")
		if collection != "exams" && explicit == "" && assessmentTime == "" {
			return "discussion", ""
		}
	}
	if kind == "progress" && academicField(v.fields, "lastLearnUnitId") != "" {
		return "course_observation", ""
	}
	if kind == "progress" && v.row.Source == "mooc" {
		switch task {
		case "quiz", "homework":
			kind = "assignment"
		case "exam":
			kind = "exam"
		}
	}
	return
}

var academicPersonalKeys = []string{"userScore", "studentScore", "totalScore", "finalMark", "usedTryCount", "submitStatus", "finishStatus", "statusText", "statusName", "status", "submission_status", "availability_status", "score_raw", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "enableEvaluation", "scorePubStatus", "peerReviewStatus", "evaluationStatusText", "finished", "learned", "learnStatus", "progress", "lastLearnUnitId", "completed", "total", "finished_count", "total_count", "completePercent", "learnTime", "personal_status", "task_point_total", "task_point_completed", "task_point_remaining", "participation_status", "is_current_user", "participation_coverage"}
var academicMetadataKeys = []string{"contentType", "type", "courseType", "source_scope", "source_collection", "assessment_evidence", "body_status", "coverage", "identity_source", "duration_raw", "terminal_raw", "time_raw", "time_source", "size_raw", "creator", "file_type", "material_type", "parent_id", "folder_id", "replyCount", "reply_count", "praiseCount", "praise_count", "topicType", "isTeacher", "deadline_raw", "deadline_source", "published_at_raw", "published_at_source", "updated_at_raw", "updated_at_source", "status_interpretation", "sourceCourseId", "metric", "inconsistency_reason", "nodeId", "catalog_node_type", "unit_type", "classification_status", "coverage_reason", "notice_coverage", "assessment_body_coverage", "source_term_discovery", "releaseTime", "gmtCreate", "gmtModified", "firstPublishTime", "position", "duration", "durationInSeconds", "contentFileName", "publishStatus", "visible", "viewStatus", "webVisible", "draftStatus", "material_body_coverage", "discussion_body_coverage", "body_reason", "time_reason", "catalog_status", "catalog_chapter_count", "source_inconsistent", "parent_data_id", "time_precision", "time_semantics", "source_endpoint", "source_body_endpoint", "noticeId", "postId", "post_title", "postTime", "countReply", "countVote", "countBrowse", "lastReplyTime", "discussion_coverage", "textPages", "textPageWhRatio", "weight", "publishTime", "answer_time_raw", "answer_time_source", "question_count", "question_count_source", "full_score", "full_score_source", "header_status", "header_coverage", "start_at_raw", "start_at_source", "end_at_raw", "end_at_source", "node_id", "parent_node_id", "order", "task_point_total", "task_point_completed", "task_point_remaining", "personal_status", "participation_status", "is_current_user", "participation_coverage", "topic_id", "reply_id", "user_id", "user_name", "anonymous", "reply_created_at_raw", "reply_created_at_source", "discussion_record_type", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "scoreReleaseTime", "current_user_state", "native_progress_fields", "current_user_participation_fields", "assessment_time_fields", "assessment_time_semantics", "assessment_time_coverage", "progress_status", "progress_coverage", "progress_value_semantics", "root_discussion_id", "replyId", "current_user_identity_coverage", "current_user_participation_coverage", "reply_page", "reply_coverage", "reply_pages_read", "reply_count_observed", "reply_total_count", "reply_total_pages", "reply_snapshot_changed", "reply_truncated", "comment_coverage", "source_detail_endpoint", "header_reason", "node_progress_coverage", "task_point_total_source", "task_point_remaining_source", "task_point_completed_source", "personal_status_source", "personal_status_reason", "current_user_match", "participation_semantics"}

var academicAssessmentTimeKeys = []string{"releaseTime", "startTime", "examStartTime", "endTime", "deadline", "submitEndTime", "examEndTime", "testEndTime", "homeworkEndTime", "jobDeadline", "closeTime", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "scoreReleaseTime"}
var academicProgressDetailKeys = []string{"submitStatus", "userSubmitStatus", "userEffectStatus", "finishStatus", "studentScore", "userScore", "finalMark", "usedTryCount", "finished", "learned", "learnStatus", "progress", "completePercent", "learnTime", "learnCount", "totalLearnedTime", "replyCount", "voteCount", "viewStatus"}
var academicNestedMetadataKeys = map[string][]string{"current_user_state": {"hasFollowed", "hasVoteUp", "hasVoteDown"}, "native_progress_fields": academicProgressDetailKeys, "current_user_participation_fields": academicProgressDetailKeys}

func academicSafeFields(f map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := f[k]; ok {
			if s := academicString(v); s != "" && len(s) <= 512 {
				out[k] = v
			} else if keys, ok := academicNestedMetadataKeys[k]; ok {
				if nested, ok := v.(map[string]any); ok {
					out[k] = academicSafeFields(nested, keys)
				}
			} else if k == "assessment_time_fields" {
				if nested, ok := v.(map[string]any); ok {
					scopes := map[string]any{}
					for _, scope := range []string{"node", "test"} {
						if fields, ok := nested[scope].(map[string]any); ok {
							scopes[scope] = academicSafeFields(fields, academicAssessmentTimeKeys)
						}
					}
					out[k] = scopes
				}
			} else if k == "coverage" {
				if coverage, ok := v.(map[string]any); ok {
					out[k] = academicSafeFields(coverage, []string{"scope", "state", "status", "complete", "body", "content", "replies", "source", "reason", "kind", "count", "total", "covered", "limit", "resources", "directory", "source_term", "discovery"})
				}
			}
		}
	}
	return out
}
func academicStatus(f map[string]any) (string, string) {
	raw := academicField(f, "submission_status", "status", "statusText", "statusName", "submitStatus", "finishStatus", "learnStatus", "personal_status")
	lower := strings.ToLower(strings.TrimSpace(raw))
	switch lower {
	case "needs_redo", "redo", "returned", "需重做", "待重做", "打回重做", "已打回", "重做":
		return "needs_redo", raw
	case "submitted", "已提交", "已交", "已完成待批阅", "待批阅":
		return "submitted", raw
	case "completed", "finished", "已完成", "已批阅":
		return "completed", raw
	case "open", "not_submitted", "未提交", "未交", "未完成", "待完成":
		return "open", raw
	}
	// Boolean completion and explicit submission are meaningful; attempt counts and
	// unknown numeric source enums cannot establish completion.
	for _, k := range []string{"finished", "learned", "completed"} {
		if b, ok := f[k].(bool); ok && b {
			return "completed", raw
		}
	}
	if b, ok := f["submitStatus"].(bool); ok && b {
		return "submitted", raw
	}
	return "unknown", raw
}
func academicTimeField(f map[string]any, keys ...string) model.AcademicTime {
	for _, k := range keys {
		if raw := academicString(f[k]); raw != "" {
			field := k
			if strings.HasSuffix(k, "_raw") {
				if s := academicField(f, strings.TrimSuffix(k, "_raw")+"_source"); s != "" {
					field = s
				}
			}
			return parseAcademicTime(raw, field)
		}
	}
	return parseAcademicTime("", "")
}

// The newer MOOC adapter inserts the source term into its opaque key. Legacy
// keys prove only the owner term; normalize that case without conflating a SPOC
// reference term with the owner's original directory.
func academicLogicalNative(v academicRaw) string {
	if v.row.Source != "mooc" {
		return v.row.ExternalID
	}
	b, err := base64.RawURLEncoding.DecodeString(v.row.ExternalID)
	if err != nil {
		return v.row.ExternalID
	}
	var parts []string
	if json.Unmarshal(b, &parts) != nil || (len(parts) != 5 && !(len(parts) == 3 && parts[2] == "lastLearnUnitId")) {
		return v.row.ExternalID
	}
	sourceTerm := academicField(v.fields, "sourceTermId", "source_term_id")
	if sourceTerm == "" {
		sourceTerm = parts[1]
	}
	canonical := []string{parts[0], parts[1], sourceTerm, parts[2]}
	if len(parts) == 5 {
		canonical = append(canonical, parts[3], parts[4])
	}
	return base64.RawURLEncoding.EncodeToString(academicJSON(canonical))
}
func academicItemFromRaw(owner, connection string, v academicRaw, refs map[string]model.AcademicOffering) model.AcademicItem {
	c, cl, t := academicNative(v.fields, v.row.Source)
	ref := academicRef(v.row.Source, c, cl, t)
	// ParentID is a course reference only when it matches an actual archived course.
	if o, ok := refs[v.row.ParentID]; ok && (ref == "" || ref == o.CourseRef) {
		ref = o.CourseRef
		c = o.NativeCourseID
		cl = o.NativeClassID
		t = o.NativeTermID
	}
	kind, task := academicKind(v)
	namespace := kind
	if kind == "assignment" || kind == "progress" {
		namespace = "task"
	}
	if v.row.Source == "mooc" && (kind == "assignment" || kind == "exam" || kind == "discussion" || kind == "progress" || kind == "material") {
		namespace = "unit"
	}
	item := model.AcademicItem{ID: academicID(owner, connection, "item", namespace, v.row.ParentID, academicLogicalNative(v)), Source: v.row.Source, ConnectionID: connection, CourseRef: ref, CourseName: academicField(v.fields, "course_name"), Kind: kind, TaskType: task, Title: v.title, Text: v.body, Author: v.author, NativeID: v.row.ExternalID, NativeCourseID: c, NativeClassID: cl, NativeTermID: t, SourceTermID: academicField(v.fields, "sourceTermId", "source_term_id"), ChapterID: academicField(v.fields, "chapterId", "chapter_id"), LessonID: academicField(v.fields, "lessonId", "lesson_id"), UnitID: academicField(v.fields, "unitId", "unit_id"), ContentID: academicField(v.fields, "contentId", "content_id"), Path: []string{}, RecordIDs: []string{strconv.FormatInt(v.row.ID, 10)}, ObservedAt: v.row.ObservedAt}
	if o, ok := refs[ref]; ok {
		item.OfferingID = o.ID
		item.CourseRef = o.CourseRef
		item.CourseName = o.Name
	}
	if item.Author == "" {
		item.Author = academicField(v.fields, "author", "creatorName", "createrName", "createName", "creator")
	}
	pathField := "path"
	if titles, ok := v.fields["path_titles"].([]any); ok && len(titles) > 0 {
		pathField = "path_titles"
	}
	if p, ok := v.fields[pathField].([]any); ok {
		for _, x := range p {
			if s, ok := x.(string); ok && len(item.Path) < 100 {
				item.Path = append(item.Path, s)
			}
		}
	}
	item.Published = academicTimeField(v.fields, "published_at_raw", "published_at", "publishTime", "publishedTime", "releaseTime", "firstPublishTime")
	item.Updated = academicTimeField(v.fields, "updated_at_raw", "updated_at", "updateTime", "update_time", "modifiedTime", "gmtModified")
	if kind == "notice" {
		if item.Published.Raw == "" {
			item.Published = academicTimeField(v.fields, "sendTime", "insertTime", "publishTime", "createTime")
		}
	}
	if kind == "discussion" {
		if item.Published.Raw == "" {
			item.Published = academicTimeField(v.fields, "postTime", "createTime", "create_time", "insertTime", "publishTime", "publish_time", "createtime")
		}
	}
	if item.Published.Raw == "" && v.occurred != "" {
		item.Published = parseAcademicTime(v.occurred, "occurred_at")
	}
	if item.Updated.Raw == "" && v.updated != "" {
		item.Updated = parseAcademicTime(v.updated, "updated_at")
	}
	item.Start = academicTimeField(v.fields, "start_at_raw", "startTime", "examStartTime", "start_time")
	item.End = academicTimeField(v.fields, "end_at_raw", "examEndTime", "testEndTime", "endTime", "end_time")
	item.Deadline = academicTimeField(v.fields, "deadline_raw", "deadline", "submitEndTime", "homeworkEndTime", "jobDeadline", "examEndTime", "testEndTime")
	if item.Deadline.State != "known" {
		absolute := academicTimeField(v.fields, "deadline", "submitEndTime", "homeworkEndTime", "jobDeadline", "examEndTime", "testEndTime")
		if absolute.State == "known" {
			item.Deadline = absolute
		}
	}
	if kind == "exam" && academicField(v.fields, "time_raw") != "" {
		start, end := academicExamRange(academicField(v.fields, "time_raw"), "time_raw")
		if item.Start.Raw == "" {
			item.Start = start
		}
		if item.End.Raw == "" {
			item.End = end
		}
	}
	if item.Deadline.Raw == "" && academicField(v.fields, "unit_type") != "assessment_unknown" {
		switch kind {
		case "material", "chapter", "lesson", "progress", "course_observation", "discussion":
			item.Deadline.State = "not_applicable"
		}
	}
	item.Status, item.SourceStatus = academicStatus(v.fields)
	item.PersonalState = academicJSON(academicSafeFields(v.fields, academicPersonalKeys))
	meta := academicSafeFields(v.fields, academicMetadataKeys)
	pathIDs := v.fields["path_ids"]
	if pathIDs == nil {
		pathIDs = v.fields["path"]
	}
	if native, ok := pathIDs.([]string); ok {
		converted := make([]any, len(native))
		for i, id := range native {
			converted[i] = id
		}
		pathIDs = converted
	}
	if ids, ok := pathIDs.([]any); ok {
		safe := []string{}
		for _, id := range ids {
			if s, ok := id.(string); ok && len(s) <= 512 && len(safe) < 100 {
				safe = append(safe, s)
			}
		}
		meta["path_ids"] = safe
	}
	if v.row.Kind == "exam" && kind == "discussion" {
		meta["classification_reason"] = "legacy_content_type_6_without_assessment_evidence"
	}
	if kind == "material" && academicField(v.fields, "time_raw") != "" {
		meta["unclassified_time"] = parseAcademicTime(academicField(v.fields, "time_raw"), "time_raw")
	}
	item.Metadata = academicJSON(meta)
	missing := map[string]string{}
	if item.OfferingID == "" {
		missing["offering"] = "no_explicit_archived_course_reference"
	}
	if item.Text == "" {
		missing["body"] = "not_collected"
		if reason := academicField(v.fields, "body_reason", "body_status"); reason != "" {
			missing["body"] = reason
		}
		switch academicField(v.fields, "body_status") {
		case "read":
			missing["body"] = "source_empty"
		case "source_text":
			missing["body"] = "source_text_empty"
		}
		if kind == "chapter" || kind == "lesson" {
			missing["body"] = "not_applicable"
		}
	}
	if academicField(v.fields, "classification_status") == "unsupported_assessment_subtype" || academicField(v.fields, "unit_type") == "assessment_unknown" {
		missing["assessment_subtype"] = "unsupported_assessment_subtype"
	}
	if item.Title == "" {
		missing["title"] = "source_missing"
	}
	if item.Status == "unknown" {
		missing["status"] = "no_interpretable_source_state"
	}
	for name, tm := range map[string]model.AcademicTime{"published": item.Published, "updated": item.Updated, "start": item.Start, "end": item.End, "deadline": item.Deadline} {
		if tm.State != "known" {
			reason := tm.State
			if reason == "missing" {
				reason = "source_missing_or_not_collected"
			}
			missing[name] = reason
		}
	}
	if kind == "material" && academicField(v.fields, "time_raw") != "" {
		missing["material_time_semantics"] = "source_does_not_identify_created_or_updated"
	}
	item.Missing = academicJSON(missing)
	return item
}
func buildAcademicProjection(owner, connection, source string, rows []model.PlatformRecord) (academicProjection, error) {
	p := academicProjection{offerings: []model.AcademicOffering{}, items: []model.AcademicItem{}, evidence: map[string][]model.PlatformRecord{}}
	parsed := []academicRaw{}
	refs := map[string]model.AcademicOffering{}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ObservedAt.Equal(rows[j].ObservedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].ObservedAt.Before(rows[j].ObservedAt)
	})
	for _, row := range rows {
		if row.Source != source {
			return p, model.ErrInvalidArgument
		}
		v, err := decodeAcademicRaw(row)
		if err != nil {
			return p, err
		}
		parsed = append(parsed, v)
		if row.Kind != "course" {
			continue
		}
		c, cl, t := academicNative(v.fields, source)
		ref := academicRef(source, c, cl, t)
		if ref == "" {
			continue
		}
		// Preserve the adapter's opaque course identity, even if JSON formatting differs.
		if row.ExternalID != "" {
			ref = row.ExternalID
		}
		terms := json.RawMessage(`[]`)
		if x, ok := v.fields["source_terms"].([]any); ok {
			safe := []map[string]any{}
			for _, entry := range x {
				if m, ok := entry.(map[string]any); ok {
					safe = append(safe, academicSafeFields(m, []string{"courseId", "termId", "sourceTermId", "sourceCourseId", "source_scope", "scope", "relation", "discovery", "name"}))
				} else if s := academicString(entry); s != "" && len(s) <= 512 {
					safe = append(safe, map[string]any{"termId": entry})
				}
				if len(safe) >= 100 {
					break
				}
			}
			terms = academicJSON(safe)
		}
		if source == "mooc" {
			var collected []map[string]any
			_ = json.Unmarshal(terms, &collected)
			relations := []map[string]any{{"courseId": c, "termId": t, "sourceTermId": t, "source_scope": "route", "relation": "owner_route"}}
			if sourceTerm := academicField(v.fields, "sourceTermId"); sourceTerm != "" && sourceTerm != t {
				active := academicSafeFields(v.fields, []string{"sourceTermId", "sourceCourseId", "source_scope", "source_term_discovery"})
				active["relation"] = "active_source_reference"
				relations = append(relations, active)
			}
			for _, relation := range collected {
				duplicate := false
				for _, existing := range relations {
					if academicField(relation, "sourceTermId", "termId") == academicField(existing, "sourceTermId", "termId") {
						duplicate = true
						break
					}
				}
				if !duplicate {
					relations = append(relations, relation)
				}
			}
			terms = academicJSON(relations)
		}
		coverage := academicJSON(academicSafeFields(v.fields, []string{"coverage", "source_scope", "body_status", "coverage_reason", "notice_coverage", "assessment_body_coverage", "material_body_coverage", "discussion_body_coverage", "source_term_discovery", "catalog_status", "catalog_chapter_count", "teacher_coverage", "description_coverage", "description_source_course_id"}))
		details := map[string]any{}
		for _, key := range []string{"requirements", "teachingGoal", "target", "courseLoad", "courseStyle", "courseName", "description_source_course_id", "teacher_coverage", "description_coverage"} {
			if value := academicString(v.fields[key]); value != "" && len(value) <= 8192 {
				details[key] = value
			}
		}
		teachers := []string{}
		if values, ok := v.fields["teachers"].([]any); ok {
			for _, value := range values {
				if teacher, ok := value.(string); ok && len(teacher) <= 512 && len(teachers) < 50 {
					teachers = append(teachers, teacher)
				}
			}
		}
		if len(teachers) > 0 {
			details["teachers"] = teachers
		}
		o := model.AcademicOffering{Description: v.body, Details: academicJSON(details), ID: academicID(owner, connection, "offering", ref), CourseID: academicID(owner, connection, "course", c), Source: source, ConnectionID: connection, CourseRef: ref, NativeCourseID: c, NativeClassID: cl, NativeTermID: t, CourseType: academicField(v.fields, "courseType", "course_type"), Name: v.title, Teacher: academicField(v.fields, "teacher", "teacher_name", "teacherName"), School: academicField(v.fields, "school", "school_name"), SourceTerms: terms, Coverage: coverage, RecordID: strconv.FormatInt(row.ID, 10), ObservedAt: row.ObservedAt}
		if o.Teacher == "" {
			o.Teacher = strings.Join(teachers, "、")
		}
		refs[ref] = o
		canonical := academicRef(source, c, cl, t)
		refs[canonical] = o
	}
	unique := map[string]model.AcademicOffering{}
	for _, o := range refs {
		unique[o.ID] = o
	}
	for _, o := range unique {
		p.offerings = append(p.offerings, o)
	}
	items := map[string]model.AcademicItem{}
	resourceCounts := map[string]map[string]int{}
	for _, v := range parsed {
		if v.row.Kind == "course" {
			continue
		}
		item := academicItemFromRaw(owner, connection, v, refs)
		if item.OfferingID != "" {
			if resourceCounts[item.OfferingID] == nil {
				resourceCounts[item.OfferingID] = map[string]int{}
			}
			resourceCounts[item.OfferingID][v.row.Kind]++
		}
		if old, ok := items[item.ID]; ok {
			personal := map[string]any{}
			_ = json.Unmarshal(old.PersonalState, &personal)
			newPersonal := map[string]any{}
			_ = json.Unmarshal(item.PersonalState, &newPersonal)
			for k, val := range newPersonal {
				personal[k] = val
			}
			metadata := map[string]any{}
			_ = json.Unmarshal(old.Metadata, &metadata)
			newMetadata := map[string]any{}
			_ = json.Unmarshal(item.Metadata, &newMetadata)
			for k, val := range newMetadata {
				if v.row.Kind == "progress" && old.Kind != "progress" {
					// A progress directory is not authority for collected content,
					// hierarchy, classification, or its coverage. Only explicit time
					// observations supplement personal state here.
					switch k {
					case "deadline_raw", "deadline_source", "published_at_raw", "published_at_source", "updated_at_raw", "updated_at_source", "releaseTime", "firstPublishTime", "gmtModified":
					default:
						continue
					}
				}
				metadata[k] = val
			}
			item.Metadata = academicJSON(metadata)
			bodyStatus := academicField(v.fields, "body_status")
			uncollected := bodyStatus == "not_collected" || bodyStatus == "not_requested"
			for _, pair := range []struct {
				name   string
				target *model.AcademicTime
				prior  model.AcademicTime
			}{{"published", &item.Published, old.Published}, {"updated", &item.Updated, old.Updated}, {"start", &item.Start, old.Start}, {"end", &item.End, old.End}, {"deadline", &item.Deadline, old.Deadline}} {
				reason := academicField(v.fields, pair.name+"_reason", pair.name+"_status")
				if pair.target.Raw == "" && (v.row.Kind == "progress" || uncollected) && reason != "not_provided" && reason != "source_missing" && reason != "source_empty" {
					*pair.target = pair.prior
				}
			}
			// Progress is personal evidence; it must not replace a richer task body or kind.
			if v.row.Kind == "progress" && old.Kind != "progress" {
				old.RecordIDs = append(old.RecordIDs, item.RecordIDs...)
				old.PersonalState = academicJSON(personal)
				old.Metadata = academicJSON(metadata)
				// Accept reliable newer time evidence without treating absence or
				// a partial countdown as removal of a previously known instant.
				for _, pair := range []struct {
					target   *model.AcademicTime
					observed model.AcademicTime
				}{{&old.Published, item.Published}, {&old.Updated, item.Updated}, {&old.Start, item.Start}, {&old.End, item.End}, {&old.Deadline, item.Deadline}} {
					if pair.observed.Raw != "" && (pair.observed.State == "known" || pair.target.Raw == "") {
						*pair.target = pair.observed
					}
				}
				old.Status, old.SourceStatus = academicStatus(personal)
				if item.ObservedAt.After(old.ObservedAt) {
					old.ObservedAt = item.ObservedAt
				}
				item = old
			} else {
				if item.Title == "" {
					item.Title = old.Title
				}
				if item.Text == "" && uncollected {
					item.Text = old.Text
					previous := map[string]any{}
					_ = json.Unmarshal(old.Metadata, &previous)
					for _, key := range []string{"body_status", "body_reason", "coverage"} {
						if value, ok := previous[key]; ok {
							metadata[key] = value
						}
					}
					item.Metadata = academicJSON(metadata)
				}
				item.RecordIDs = append(old.RecordIDs, item.RecordIDs...)
				item.PersonalState = academicJSON(personal)
				item.Status, item.SourceStatus = academicStatus(personal)
			}
		}
		items[item.ID] = item
		p.evidence[item.ID] = append(p.evidence[item.ID], v.row)
	}
	for _, item := range items {
		missing := map[string]string{}
		_ = json.Unmarshal(item.Missing, &missing)
		if item.Text != "" {
			delete(missing, "body")
		}
		if item.Title != "" {
			delete(missing, "title")
		}
		if item.Status != "unknown" {
			delete(missing, "status")
		}
		for name, tm := range map[string]model.AcademicTime{"published": item.Published, "updated": item.Updated, "start": item.Start, "end": item.End, "deadline": item.Deadline} {
			if tm.State == "known" {
				delete(missing, name)
			} else if tm.State == "partial" {
				missing[name] = "partial"
			}
		}
		item.Missing = academicJSON(missing)
		p.items = append(p.items, item)
	}
	itemCounts := map[string]map[string]int{}
	for _, item := range p.items {
		if item.OfferingID != "" {
			if itemCounts[item.OfferingID] == nil {
				itemCounts[item.OfferingID] = map[string]int{}
			}
			itemCounts[item.OfferingID][item.Kind]++
		}
	}
	for index := range p.offerings {
		o := &p.offerings[index]
		coverage := map[string]any{}
		_ = json.Unmarshal(o.Coverage, &coverage)
		resources := map[string]any{}
		for _, kind := range []string{"assignment", "exam", "notice", "discussion", "material", "chapter", "lesson", "progress", "course_observation"} {
			evidenceCount := resourceCounts[o.ID][kind]
			itemCount := itemCounts[o.ID][kind]
			state := "not_observed"
			if itemCount > 0 {
				state = "observed"
			} else if evidenceCount > 0 {
				state = "evidence_only"
			}
			resources[kind] = map[string]any{"state": state, "item_count": itemCount, "evidence_record_count": evidenceCount, "record_count": evidenceCount, "complete": false}
		}
		coverage["count_semantics"] = "item_count counts normalized logical items; evidence_record_count and legacy record_count count raw resource records"
		coverage["resources"] = resources
		coverage["source_completeness"] = "not_asserted"
		o.Coverage = academicJSON(coverage)
	}
	sort.Slice(p.offerings, func(i, j int) bool { return p.offerings[i].ID < p.offerings[j].ID })
	sort.Slice(p.items, func(i, j int) bool { return p.items[i].ID < p.items[j].ID })
	return p, nil
}

// syncAcademicConnection runs after raw upserts inside the caller's transaction.
// Reconciliation only removes derived rows absent from the current raw authority.
func syncAcademicConnection(ctx context.Context, tx pgx.Tx, owner, connection string) error {
	var source string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT source,enabled AND state='active' FROM platform_connections WHERE user_id=$1 AND id=$2 FOR UPDATE`, owner, connection).Scan(&source, &active); err != nil {
		return storageError(err)
	}
	if source != "chaoxing" && source != "mooc" {
		return nil
	}
	if !active {
		return model.ErrConflict
	}
	rows, err := tx.Query(ctx, `SELECT id,source,kind,external_id,parent_id,hash,content,observed_at FROM platform_records WHERE user_id=$1 AND connection_id=$2 ORDER BY id`, owner, connection)
	if err != nil {
		return storageError(err)
	}
	raw := []model.PlatformRecord{}
	for rows.Next() {
		var v model.PlatformRecord
		if err = rows.Scan(&v.ID, &v.Source, &v.Kind, &v.ExternalID, &v.ParentID, &v.Hash, &v.Content, &v.ObservedAt); err != nil {
			rows.Close()
			return storageError(err)
		}
		raw = append(raw, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return storageError(err)
	}
	p, err := buildAcademicProjection(owner, connection, source, raw)
	if err != nil {
		return err
	}
	return persistAcademicProjection(ctx, tx, owner, connection, p)
}
