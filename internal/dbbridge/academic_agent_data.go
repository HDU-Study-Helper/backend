package dbbridge

import (
	f "academic/agent/framework"
	"context"
	"strings"
)

// These projections are explicit business fields, never raw source metadata or credentials.
// Timestamp comparisons use timestamptz; partial raw dates remain in *_time metadata.
type academicDataset struct {
	from   string
	order  string
	fields map[string]string
	times  map[string]bool
	names  []string
}

func textColumn(name string) string { return "COALESCE(a." + name + ",'')" }
func academicDatasets() map[string]academicDataset {
	offerings := academicDataset{from: "academic_offerings a", order: "a.name,a.id", fields: map[string]string{}, times: map[string]bool{}, names: []string{"id", "offering_id", "course_id", "connection_id", "source", "course_ref", "course_name", "native_course_id", "native_class_id", "native_term_id", "course_type", "teacher", "school", "description", "details", "source_terms", "coverage", "record_id", "observed_at"}}
	for _, name := range []string{"id", "course_id", "connection_id", "source", "course_ref", "native_course_id", "native_class_id", "native_term_id", "course_type", "teacher", "school", "description"} {
		offerings.fields[name] = textColumn(name)
	}
	offerings.fields["offering_id"] = "a.id"
	offerings.fields["course_name"] = "a.name"
	offerings.fields["record_id"] = "a.record_id::text"
	offerings.fields["observed_at"] = "a.observed_at"
	offerings.times["observed_at"] = true
	for _, name := range []string{"source_terms", "coverage", "details"} {
		offerings.fields[name] = "a." + name + "::text"
	}
	items := academicDataset{from: "academic_items a LEFT JOIN academic_item_personal_states ps ON ps.user_id=a.user_id AND ps.connection_id=a.connection_id AND ps.item_id=a.id", order: "a.id", fields: map[string]string{}, times: map[string]bool{}, names: []string{"id", "item_id", "connection_id", "source", "offering_id", "course_ref", "course_name", "kind", "task_type", "title", "text", "author", "native_id", "native_course_id", "native_class_id", "native_term_id", "source_term_id", "chapter_id", "lesson_id", "unit_id", "content_id", "path", "published_at", "updated_at", "start_at", "end_at", "deadline", "published_time", "updated_time", "start_time", "end_time", "deadline_time", "status", "source_status", "submission_status", "used_try_count", "user_score", "total_score", "evaluate_start", "evaluate_end", "complete_percent", "learn_time", "last_learn_unit_id", "missing", "record_ids", "observed_at", "body_status", "coverage"}}
	for _, name := range []string{"id", "connection_id", "source", "offering_id", "course_ref", "course_name", "kind", "task_type", "title", "author", "native_id", "native_course_id", "native_class_id", "native_term_id", "source_term_id", "chapter_id", "lesson_id", "unit_id", "content_id", "status", "source_status"} {
		items.fields[name] = textColumn(name)
	}
	items.fields["item_id"] = "a.id"
	items.fields["text"] = "a.body"
	for _, name := range []string{"path", "published_time", "updated_time", "start_time", "end_time", "deadline_time", "missing"} {
		items.fields[name] = "a." + name + "::text"
	}
	for _, name := range []string{"published_at", "updated_at", "start_at", "end_at", "observed_at"} {
		items.fields[name] = "a." + name
		items.times[name] = true
	}
	items.fields["deadline"] = "a.deadline_at"
	items.times["deadline"] = true
	for name, keys := range map[string][]string{"submission_status": {"submitStatus", "submission_status"}, "used_try_count": {"usedTryCount"}, "user_score": {"userScore", "studentScore", "finalMark"}, "total_score": {"totalScore", "fullMark", "totalMark"}, "evaluate_start": {"evaluateStart"}, "evaluate_end": {"evaluateEnd"}, "complete_percent": {"completePercent"}, "learn_time": {"learnTime"}, "last_learn_unit_id": {"lastLearnUnitId"}, "user_submit_status": {"userSubmitStatus"}, "user_effect_status": {"userEffectStatus"}, "total_learned_time": {"totalLearnedTime"}, "reply_count": {"replyCount"}, "vote_count": {"voteCount"}, "view_status": {"viewStatus"}} {
		expr := []string{}
		for _, key := range keys {
			expr = append(expr, "CASE WHEN jsonb_typeof(ps.state->'"+key+"') IN ('string','number','boolean') THEN ps.state->>'"+key+"' END")
		}
		items.fields[name] = "COALESCE(" + strings.Join(expr, ",") + ",'')"

	}
	items.names = append(items.names, "user_submit_status", "user_effect_status", "total_learned_time", "reply_count", "vote_count", "view_status", "metadata", "answer_time_raw", "answer_time_source", "question_count", "full_score", "header_status", "node_id", "parent_node_id", "task_point_total", "task_point_remaining", "task_point_completed", "personal_status", "topic_id", "is_current_user", "participation_status")
	items.fields["metadata"] = safeMetadataProjection("a.metadata")
	for _, name := range []string{"answer_time_raw", "answer_time_source", "question_count", "full_score", "header_status", "node_id", "parent_node_id", "task_point_total", "task_point_remaining", "task_point_completed", "personal_status"} {
		items.fields[name] = "CASE WHEN jsonb_typeof(a.metadata->'" + name + "') IN ('string','number','boolean') THEN COALESCE(a.metadata->>'" + name + "','') ELSE '' END"
	}
	items.fields["topic_id"] = "CASE WHEN jsonb_typeof(a.metadata->'topic_id')='string' THEN a.metadata->>'topic_id' ELSE '' END"
	items.fields["is_current_user"] = "CASE WHEN jsonb_typeof(a.metadata->'is_current_user')='boolean' THEN a.metadata->>'is_current_user' ELSE '' END"
	items.fields["participation_status"] = "COALESCE(a.metadata->>'participation_status','')"
	items.fields["body_status"] = "COALESCE(a.metadata->>'body_status','unknown')"
	items.fields["coverage"] = "COALESCE(a.metadata->'coverage','{}'::jsonb)::text"
	items.fields["record_ids"] = "COALESCE((SELECT jsonb_agg(e.record_id::text ORDER BY e.record_id)::text FROM academic_item_evidence e WHERE e.user_id=a.user_id AND e.connection_id=a.connection_id AND e.item_id=a.id),'[]')"
	return map[string]academicDataset{"academic_offerings": offerings, "academic_items": items}
}
func academicCatalog() []f.Dataset {
	sets := academicDatasets()
	out := []f.Dataset{}
	for _, name := range []string{"academic_offerings", "academic_items"} {
		set := sets[name]
		fields := []f.Field{}
		for _, key := range set.names {
			typ := "string"
			if set.times[key] {
				typ = "timestamp"
			}
			fields = append(fields, f.Field{Name: key, Type: typ})
		}
		out = append(out, f.Dataset{Name: name, Fields: fields})
	}
	return out
}
func (r *agentData) queryAcademic(ctx context.Context, s f.Scope, q f.Query) (f.QueryResult, error) {
	return r.queryDataset(ctx, s, q)
}
