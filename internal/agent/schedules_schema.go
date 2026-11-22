package agent

import "encoding/json"

const scheduleMaxOffset = 100000

func scheduleObject(props map[string]any, required ...string) json.RawMessage {
	if required == nil {
		required = []string{}
	}
	raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
	return raw
}
func scheduleText(max int, description string) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": max, "description": description}
}
func scheduleInt(min, max int, description string) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max, "description": description}
}
func scheduleTimestamp(description string) map[string]any {
	return map[string]any{"type": "string", "format": "date-time", "maxLength": 64, "description": description}
}
func scheduleEnum(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values, "description": description}
}
func scheduleDescribed(raw json.RawMessage, description string) map[string]any {
	var schema map[string]any
	_ = json.Unmarshal(raw, &schema)
	schema["description"] = description
	return schema
}
func scheduleFieldsSchema() map[string]any {
	return map[string]any{
		"academic_year": scheduleInt(2000, 2100, "Academic year starting year; 2026 means academic year 2026–2027. Must come from the user's actual calendar, not an inferred year."),
		"term":          scheduleInt(1, 2, "Academic term: 1 or 2, used with academic_year."),
		"teaching_week": scheduleInt(1, 60, "Teaching-week number in the selected academic term, not ISO or calendar week."),
		"start_time":    scheduleTimestamp("Inclusive start instant in RFC3339 with explicit timezone. Whole-minute precision; must precede end_time."),
		"end_time":      scheduleTimestamp("Exclusive end instant in RFC3339 with explicit timezone. Whole-minute precision; duration must be positive and no more than seven days."),
		"title":         scheduleText(1000, "Schedule title. Required non-blank text, at most 1000 UTF-8 bytes; maxLength is only a character precheck. For updates, omission preserves the title."),
		"details":       map[string]any{"type": "string", "maxLength": 16384, "default": "", "description": "Schedule details, at most 16384 UTF-8 bytes; maxLength is only a character precheck. Create omission becomes empty text. Update omission preserves details; an explicit empty string clears them. null is not accepted."},
	}
}
func scheduleItemSchema() json.RawMessage {
	fields := scheduleFieldsSchema()
	delete(fields, "academic_year")
	delete(fields, "term")
	// Historical before/after snapshots always serialize all ScheduleFields.
	fieldRecord := scheduleObject(fields, "teaching_week", "start_time", "end_time", "title", "details")
	patch := scheduleObject(map[string]any{
		"fields": map[string]any{"type": "array", "description": "Explicit fields changed by this patch; after contains a full snapshot, but only these fields are applied.", "items": scheduleEnum("Changed ScheduleFields member.", "start_time", "end_time", "title", "details")},
		"after":  scheduleDescribed(fieldRecord, "Complete after snapshot; apply only members named in fields."),
	}, "fields", "after")
	change := scheduleObject(map[string]any{
		"mode":   scheduleEnum("Kind of this recorded change; SYNC/SYNC_REMOVED are source refresh history and DELETE is a retained timetable deletion marker.", "TEMPORARY", "PERMANENT", "SYNC", "DELETE", "SYNC_REMOVED"),
		"before": scheduleDescribed(fieldRecord, "Complete ScheduleFields immediately before this recorded change."), "after": scheduleDescribed(fieldRecord, "Complete ScheduleFields immediately after this recorded change."),
		"effective_week": scheduleInt(1, 60, "Teaching week from which a permanent timetable change applies, or the selected temporary week."),
		"original_id":    scheduleText(128, "ID of the occurrence on which the change was requested."),
		"at":             scheduleTimestamp("Time this change was recorded, not the schedule's start time."),
	}, "mode", "before", "after", "effective_week", "original_id", "at")
	mod := scheduleObject(map[string]any{
		"weeks_raw":      map[string]any{"type": "string", "description": "Original timetable week expression, when available."},
		"teaching_weeks": map[string]any{"type": "array", "description": "Explicit teaching weeks from the source timetable, not inferred calendar recurrence.", "items": scheduleInt(1, 60, "Teaching week in the source timetable series.")},
		"week_pattern":   map[string]any{"type": "string", "description": "Source week-pattern text, when present; not an inferred recurrence rule."},
		"base":           scheduleDescribed(fieldRecord, "Original source timetable fields before local overrides, when available."), "permanent": scheduleDescribed(patch, "Permanent local override for this series from its effective teaching week."), "temporary": scheduleDescribed(patch, "Local override applying only to this selected occurrence/week."),
		"deleted": map[string]any{"type": "boolean", "description": "Whether the timetable occurrence has a deletion marker; absence is false."},
		"history": map[string]any{"type": "array", "description": "Recorded modifications in storage order. Empty array means no recorded changes.", "items": json.RawMessage(change)},
	}, "history")
	props := scheduleFieldsSchema()
	props["id"] = scheduleText(128, "Persisted schedule occurrence ID; use query/get results, never a course or series ID.")
	props["user_id"] = scheduleText(128, "Owning user selected by the trusted runtime, not a model-controlled argument.")
	props["source"] = scheduleEnum("Origin of this persisted occurrence.", "USER", "AGENT", "TIMETABLE")
	props["time_status"] = scheduleEnum("Derived time window status; independent of personal completion/attendance.", "NOT_STARTED", "IN_PROGRESS", "FINISHED")
	props["event_status"] = scheduleEnum("Application completion flag. INCOMPLETE does not prove absence, lateness or cancellation.", "INCOMPLETE", "COMPLETED")
	props["active"] = map[string]any{"type": "boolean", "description": "Whether this occurrence is currently active; get may return inactive history."}
	props["mod"] = scheduleDescribed(mod, "Source timetable fields, local overrides and recorded change history. These explain this occurrence; they are not an automatic recurrence command.")
	props["series_key"] = map[string]any{"type": "string", "description": "Optional timetable series key. Not an occurrence ID."}
	props["origin_key"] = map[string]any{"type": "string", "description": "Optional source-specific timetable origin key."}
	props["created_at"] = scheduleTimestamp("Persistence creation time.")
	props["updated_at"] = scheduleTimestamp("Last persistence update time.")
	return scheduleObject(props, "id", "user_id", "academic_year", "term", "teaching_week", "start_time", "end_time", "title", "details", "source", "time_status", "event_status", "active", "mod", "created_at", "updated_at")
}
func scheduleQuerySchema() json.RawMessage {
	fields := scheduleFieldsSchema()
	query := map[string]any{"academic_year": fields["academic_year"], "term": fields["term"], "teaching_week": fields["teaching_week"],
		"from":   scheduleTimestamp("Inclusive lower boundary of [from,to); matches occurrences overlapping this range. Supply together with to; maximum 366 days."),
		"to":     scheduleTimestamp("Exclusive upper boundary of [from,to), after from. Supply together with from."),
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 100, "description": "Maximum returned rows, default 100; a nonempty page is not proof of complete coverage."},
		"offset": map[string]any{"type": "integer", "minimum": 0, "maximum": scheduleMaxOffset, "default": 0, "description": "First query omits this or uses 0. Continue with returned next_offset using unchanged filters. Stop on an empty page or page_limit_reason=offset_limit; do not reuse an out-of-range next_offset."},
	}
	var value map[string]any
	_ = json.Unmarshal(scheduleObject(query), &value)
	value["anyOf"] = []any{map[string]any{"required": []string{"academic_year", "term", "teaching_week"}}, map[string]any{"required": []string{"from", "to"}}}
	raw, _ := json.Marshal(value)
	return raw
}
func scheduleUpdateSchema() json.RawMessage {
	fields := scheduleFieldsSchema()
	delete(fields, "academic_year")
	delete(fields, "term")
	delete(fields, "teaching_week")
	// Defaults are create semantics, not patch values.
	delete(fields["details"].(map[string]any), "default")
	fields["id"] = scheduleText(128, "Current user's persisted schedule occurrence ID returned by schedule_query/get. Read it before updating.")
	fields["mode"] = scheduleEnum("Required scope. TEMPORARY changes only the selected timetable week. PERMANENT changes the same timetable series from the selected week onward. Non-timetable schedules change only this occurrence. event_status applies only to the selected occurrence.", "TEMPORARY", "PERMANENT")
	fields["event_status"] = scheduleEnum("Completion flag for the selected occurrence only; omission preserves the existing flag.", "INCOMPLETE", "COMPLETED")
	var value map[string]any
	_ = json.Unmarshal(scheduleObject(fields, "id", "mode"), &value)
	branches := []any{}
	for _, key := range []string{"start_time", "end_time", "title", "details", "event_status"} {
		branches = append(branches, map[string]any{"required": []string{key}})
	}
	value["anyOf"] = branches
	raw, _ := json.Marshal(value)
	return raw
}
