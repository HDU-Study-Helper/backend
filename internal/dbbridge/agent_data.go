package dbbridge

import (
	"context"
	"strings"

	f "academic/agent/framework"
)

// AgentData exposes a small readonly projection of existing per-owner platform records.
// Raw content, private memories, sessions, credentials and arbitrary SQL are never exposed.
func (d *DB) AgentData() f.DataService { return &agentData{db: d} }

type agentData struct{ db *DB }

// Keep catalog order stable and build SQL only from this server-owned allowlist.
// Every projected value is text. Source epoch numbers/booleans remain their raw
// textual value; the model must not treat availability dates as publication or
// invent a schedule instant from an untyped timestamp or source status code.
var dataFieldOrder = []string{
	"id", "connection_id", "source", "kind", "external_id", "parent_id", "title", "text",
	"course_id", "class_id", "term_id", "course_name", "task_type",
	"status", "source_status", "submission_status", "availability_status", "finish_status", "learn_status", "progress", "completed", "total",
	"deadline", "start_time", "end_time", "published_at", "source_time", "time_raw",
	"file_type", "size_raw", "path", "is_folder",
	"academic_year", "term", "entry_type", "class_name", "credits_raw", "category", "course_category", "nature",
	"teacher", "teacher_raw", "teacher_name", "location", "location_raw", "campus", "assessment_raw",
	"total_hours_raw", "weekly_hours_raw", "schedule_raw", "weekday", "periods_raw", "weeks_raw", "identity_source",
	"node_id", "parent_node_id", "order", "coverage", "task_point_total", "task_point_remaining", "task_point_completed", "personal_status",
	"answer_time_raw", "answer_time_source", "question_count", "full_score", "header_status", "metadata",
}

// sourceScalar rejects objects/arrays even for an allowed key. Do not expose the
// complete fields object: older or malformed records could contain private state.
func sourceScalar(path ...string) string {
	quoted := make([]string, len(path))
	for i, part := range path {
		quoted[i] = "'" + strings.ReplaceAll(part, "'", "''") + "'"
	}
	p := "ARRAY[" + strings.Join(quoted, ",") + "]::text[]"
	return "CASE WHEN jsonb_typeof(r.content #> " + p + ") IN ('string','number','boolean') THEN NULLIF(r.content #>> " + p + ",'') END"
}

func sourceFields(keys ...string) string {
	values := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		values = append(values, sourceScalar("fields", key))
	}
	return "COALESCE(" + strings.Join(append(values, "''"), ",") + ")"
}

var dataFields = map[string]string{
	"id": "r.id::text", "connection_id": "r.connection_id", "source": "r.source", "kind": "r.kind", "external_id": "r.external_id", "parent_id": "r.parent_id",
	"title":               "COALESCE(" + sourceScalar("title") + ",'')",
	"text":                "COALESCE(" + sourceScalar("content") + "," + sourceScalar("text") + ",'')",
	"course_id":           sourceFields("course_id", "courseId"),
	"class_id":            sourceFields("class_id", "classId"),
	"term_id":             sourceFields("term_id", "termId"),
	"course_name":         "COALESCE(" + sourceScalar("fields", "course_name") + "," + sourceScalar("fields", "courseName") + ",CASE WHEN r.kind='course' THEN " + sourceScalar("title") + " END,'')",
	"task_type":           sourceFields("task_type"),
	"status":              sourceFields("status", "statusText", "statusName", "submission_status", "submitStatus", "finishStatus", "learnStatus"),
	"source_status":       sourceFields("status", "statusText", "statusName", "submission_status", "submitStatus", "finishStatus", "learnStatus"),
	"submission_status":   sourceFields("submission_status", "submitStatus"),
	"availability_status": sourceFields("availability_status"),
	"finish_status":       sourceFields("finishStatus"),
	"learn_status":        sourceFields("learnStatus"),
	"progress":            sourceFields("progress"), "completed": sourceFields("completed"), "total": sourceFields("total"),
	// Only explicitly named task deadlines are aliases. endTime/closeTime and
	// U-campus strategies.end_time are availability, never fallback deadlines.
	"deadline":     sourceFields("deadline", "submitEndTime", "homeworkEndTime", "examEndTime", "testEndTime", "jobDeadline"),
	"start_time":   "COALESCE(" + sourceScalar("fields", "start_time") + "," + sourceScalar("fields", "startTime") + "," + sourceScalar("fields", "examStartTime") + "," + sourceScalar("fields", "start_time_raw") + "," + sourceScalar("fields", "strategies", "start_time") + ",'')",
	"end_time":     "COALESCE(" + sourceScalar("fields", "end_time") + "," + sourceScalar("fields", "endTime") + "," + sourceScalar("fields", "closeTime") + "," + sourceScalar("fields", "end_time_raw") + "," + sourceScalar("fields", "strategies", "end_time") + ",'')",
	"published_at": sourceFields("published_at", "publish_time", "publishTime", "releaseTime"),
	"source_time":  sourceFields("time_raw", "createTime", "sendTime", "insertTime"),
	"time_raw":     sourceFields("time_raw"),
	"file_type":    sourceFields("file_type"), "size_raw": sourceFields("size_raw"), "is_folder": sourceFields("is_folder"),
	// Only arrays of strings are exposed; nested objects or signed metadata
	// cannot leak through the directory path projection.
	"path": "CASE WHEN jsonb_typeof(r.content #> '{fields,path}')='array' THEN CASE WHEN NOT EXISTS (SELECT 1 FROM jsonb_array_elements(r.content #> '{fields,path}') AS p(value) WHERE jsonb_typeof(p.value)<>'string') THEN (r.content #> '{fields,path}')::text ELSE '' END ELSE '' END",
}

func (r *agentData) Catalog(ctx context.Context, s f.Scope) ([]f.Dataset, error) {
	var owner string
	if err := r.db.pool.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 AND NOT disabled`, s.OwnerID).Scan(&owner); err != nil {
		return nil, agentError(err)
	}
	fields := []f.Field{}
	for _, name := range dataFieldOrder {
		fields = append(fields, f.Field{Name: name, Type: "string"})
	}
	out := append([]f.Dataset{{Name: "platform_records", Fields: fields}}, academicCatalog()...)
	return append(out, profileDataCatalog()...), nil
}
func (r *agentData) Query(ctx context.Context, s f.Scope, q f.Query) (result f.QueryResult, err error) {
	return r.queryDataset(ctx, s, q)
}

// jsonFieldProjection builds a JSON object from explicitly allowed scalar keys
// and arrays of scalar values. Nested objects, credentials and transport URLs
// cannot enter model input through a nominally safe metadata key.
func jsonFieldProjection(document string, keys []string, arrays []string) string {
	pairs := []string{}
	for _, key := range keys {
		literal := "'" + strings.ReplaceAll(key, "'", "''") + "'"
		pairs = append(pairs, literal, "CASE WHEN jsonb_typeof("+document+"->"+literal+") IN ('string','number','boolean') THEN "+document+"->"+literal+" END")
	}
	for _, key := range arrays {
		literal := "'" + strings.ReplaceAll(key, "'", "''") + "'"
		expr := document + "->" + literal
		pairs = append(pairs, literal, "CASE WHEN jsonb_typeof("+expr+")='array' THEN CASE WHEN NOT EXISTS (SELECT 1 FROM jsonb_array_elements("+expr+") p(value) WHERE jsonb_typeof(p.value) NOT IN ('string','number','boolean')) THEN "+expr+" END END")
	}
	// PostgreSQL limits function calls to 100 arguments. Keep projections
	// extensible by combining small objects instead of one oversized call.
	objects := []string{}
	for start := 0; start < len(pairs); start += 80 {
		end := start + 80
		if end > len(pairs) {
			end = len(pairs)
		}
		objects = append(objects, "jsonb_build_object("+strings.Join(pairs[start:end], ",")+")")
	}
	if len(objects) == 0 {
		return "'{}'::text"
	}
	return "jsonb_strip_nulls(" + strings.Join(objects, " || ") + ")::text"
}

var safeSourceMetadataKeys = []string{
	"body_status", "body_reason", "coverage", "node_id", "parent_node_id", "order", "task_point_total", "task_point_remaining", "task_point_completed", "personal_status",
	"answer_time_raw", "answer_time_source", "question_count", "full_score", "header_status", "start_at", "end_at", "deadline_raw", "deadline_source", "deadline_reason", "deadline_status",
	"published_at_raw", "published_at_source", "updated_at_raw", "updated_at_source", "start_time_raw", "end_time_raw", "start_reason", "end_reason", "classification_status", "unit_type", "task_type",
	"reply_count", "discussion_count", "current_user_status", "source_status", "source_inconsistent", "time_raw", "time_precision", "time_semantics", "time_reason",
	"start_at_raw", "start_at_source", "end_at_raw", "end_at_source", "question_count_source", "full_score_source", "header_coverage",
	"topic_id", "participation_status", "is_current_user", "participation_coverage", "discussion_record_type", "root_discussion_id", "replyId", "postId", "current_user_identity_coverage", "current_user_participation_coverage",
	"reply_page", "reply_coverage", "reply_pages_read", "reply_count_observed", "reply_total_count", "reply_total_pages", "reply_snapshot_changed", "reply_truncated", "comment_coverage", "source_detail_endpoint",
}

var safeProgressMetadataKeys = []string{"submitStatus", "userSubmitStatus", "userEffectStatus", "finishStatus", "studentScore", "userScore", "finalMark", "usedTryCount", "finished", "learned", "learnStatus", "progress", "completePercent", "learnTime", "learnCount", "totalLearnedTime", "replyCount", "voteCount", "viewStatus"}
var safeAssessmentTimeKeys = []string{"releaseTime", "startTime", "examStartTime", "endTime", "deadline", "submitEndTime", "examEndTime", "testEndTime", "homeworkEndTime", "jobDeadline", "closeTime", "evaluateStart", "evaluateEnd", "evaluationEndTime", "evaluateScoreReleaseTime", "scoreReleaseTime"}

func safeMetadataProjection(document string) string {
	base := "(" + jsonFieldProjection(document, safeSourceMetadataKeys, []string{"path_ids"}) + ")::jsonb"
	objects := []string{base}
	for _, key := range []string{"native_progress_fields", "current_user_participation_fields"} {
		projection := jsonFieldProjection("("+document+"->'"+key+"')", safeProgressMetadataKeys, nil)
		objects = append(objects, "jsonb_build_object('"+key+"',("+projection+")::jsonb)")
	}
	// The current-user presence flags are booleans, never a roster object.
	current := []string{}
	for _, key := range []string{"hasFollowed", "hasVoteUp", "hasVoteDown"} {
		expr := "(" + document + "->'current_user_state'->'" + key + "')"
		current = append(current, "'"+key+"'", "CASE WHEN jsonb_typeof("+expr+")='boolean' THEN "+expr+" END")
	}
	objects = append(objects, "jsonb_build_object('current_user_state',jsonb_strip_nulls(jsonb_build_object("+strings.Join(current, ",")+")))")
	timePairs := []string{}
	for _, outer := range []string{"node", "test"} {
		projection := jsonFieldProjection("("+document+"->'assessment_time_fields'->'"+outer+"')", safeAssessmentTimeKeys, nil)
		timePairs = append(timePairs, "'"+outer+"'", "("+projection+")::jsonb")
	}
	objects = append(objects, "jsonb_build_object('assessment_time_fields',jsonb_build_object("+strings.Join(timePairs, ",")+"))")
	return "(" + strings.Join(objects, " || ") + ")::text"
}

func init() {
	for _, name := range dataFieldOrder {
		if _, exists := dataFields[name]; !exists && name != "metadata" {
			dataFields[name] = sourceFields(name)
		}
	}
	dataFields["metadata"] = safeMetadataProjection("(r.content->'fields')")
}
