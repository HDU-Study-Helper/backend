package mooc

import (
	"academic/backend/internal/contract"
	"encoding/json"
	"math"
)

// Match the original directory's numeric viewStatus truth test, not a client
// attempt to write learning progress. Catalog replyCount is the per-user unit
// counter; post.countReply is a different, whole-thread counter.
func discussionTaskStatus(f map[string]any) {
	f["reply_collection_policy"] = "disabled_current_user_task_state_only"
	delete(f, "current_user_reply_count")
	f["participation_status"] = "unknown"
	f["participation_status_reason"] = "current_user_catalog_reply_count_not_provided"
	f["task_point_status"] = "unknown"
	f["task_point_status_source"] = "catalog.unit.viewStatus"
	f["task_point_status_semantics"] = "server_returned_official_directory_learned_marker; numeric_nonzero_matches_official_UI; reply_content_and_scoring_not_verified"
	delete(f, "task_point_completed")
	// Only the authenticated catalog layer is an authoritative personal counter.
	// Unit-detail fields and whole-thread totals must not overwrite it.
	if n, ok := nonnegativeSourceInteger(f["replyCount"]); ok {
		f["current_user_reply_count"] = n
		f["participation_status_reason"] = "authenticated_catalog.unit.replyCount"
		f["participation_status"] = "not_participated"
		if n > 0 {
			f["participation_status"] = "participated"
		}
	}
	value, exists := f["viewStatus"]
	if !exists || value == nil {
		return
	}
	done := false
	if b, ok := value.(bool); ok {
		done = b
	} else {
		n, ok := nonnegativeSourceInteger(value)
		if !ok {
			return
		}
		done = n != 0
	}
	f["task_point_completed"] = done
	if done {
		f["task_point_status"] = "completed"
	} else {
		f["task_point_status"] = "incomplete"
	}
}
func nonnegativeSourceInteger(value any) (int64, bool) {
	var n float64
	switch v := value.(type) {
	case json.Number:
		f, e := v.Float64()
		if e != nil {
			return 0, false
		}
		n = f
	case int:
		n = float64(v)
	case int64:
		n = float64(v)
	case float64:
		n = v
	default:
		return 0, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1<<53-1 || math.Trunc(n) != n {
		return 0, false
	}
	return int64(n), true
}
func discussionUnitRecord(row contract.Record) contract.Record {
	if row.Fields == nil {
		row.Fields = map[string]any{}
	}
	if row.Fields["source_detail_endpoint"] != nil || row.Fields["detail_progress_fields"] != nil {
		snapshot := object(row.Fields["current_user_participation_fields"])
		for _, key := range []string{"viewStatus", "replyCount"} {
			delete(row.Fields, key)
			if value, exists := snapshot[key]; exists {
				row.Fields[key] = value
			}
		}
		row.Fields["native_progress_fields"] = nativeProgress(row.Fields)
	}
	// Root-poster identity is not the learner's discussion participation.
	delete(row.Fields, "is_current_user")
	delete(row.Fields, "current_user_identity_coverage")
	discussionTaskStatus(row.Fields)
	row.Fields["reply_coverage"] = "not_requested_current_user_task_state_only"
	row.Fields["reply_body_coverage"] = "not_collected_by_policy"
	row.Fields["reply_roster_coverage"] = "not_collected_by_policy"
	row.Fields["discussion_record_type"] = "root_post"
	row.Fields["root_discussion_id"] = row.ExternalID
	row.Fields["discussion_coverage"] = "root requirements and current-user directory task marker only; reply list disabled"
	row.Fields["comment_coverage"] = "not_requested_by_policy"
	return row
}
