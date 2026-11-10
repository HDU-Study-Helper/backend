package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	f "academic/agent/framework"
	"academic/backend/internal/bridge"
	"academic/backend/internal/model"
)

// RegisterScheduleTools adapts the existing in-process schedule port. Owners are
// taken exclusively from Runtime, never from model arguments.
func RegisterScheduleTools(tools *f.ToolRegistry, port bridge.Port) error {
	str := func(max int) any { return map[string]any{"type": "string", "minLength": 1, "maxLength": max} }
	integer := func(min, max int) any { return map[string]any{"type": "integer", "minimum": min, "maximum": max} }
	object := func(props map[string]any, required ...string) json.RawMessage {
		if required == nil {
			required = []string{}
		}
		raw, _ := json.Marshal(map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false})
		return raw
	}
	stamp := map[string]any{"type": "string", "format": "date-time", "maxLength": 64}
	fields := func() map[string]any {
		return map[string]any{
			"academic_year": integer(2000, 2100), "term": integer(1, 2), "teaching_week": integer(1, 60),
			"start_time": stamp, "end_time": stamp, "title": str(1000), "details": map[string]any{"type": "string", "maxLength": 16384},
		}
	}
	item := json.RawMessage(`{"type":"object","required":["id","academic_year","term","teaching_week","start_time","end_time","title","details","source","time_status","event_status","active","mod"],"properties":{"id":{"type":"string"},"academic_year":{"type":"integer"},"term":{"type":"integer"},"teaching_week":{"type":"integer"},"start_time":{"type":"string"},"end_time":{"type":"string"},"title":{"type":"string"},"details":{"type":"string"},"source":{"enum":["USER","AGENT","TIMETABLE"]},"time_status":{"enum":["NOT_STARTED","IN_PROGRESS","FINISHED"]},"event_status":{"enum":["INCOMPLETE","COMPLETED"]},"active":{"type":"boolean"},"mod":{"type":"object"}}}`)
	query := map[string]any{"academic_year": integer(2000, 2100), "term": integer(1, 2), "teaching_week": integer(1, 60), "from": stamp, "to": stamp, "limit": integer(1, 1000), "offset": integer(0, 100000)}
	querySchema := object(query)
	var queryObj map[string]any
	_ = json.Unmarshal(querySchema, &queryObj)
	queryObj["anyOf"] = []any{map[string]any{"required": []string{"academic_year", "term", "teaching_week"}}, map[string]any{"required": []string{"from", "to"}}}
	querySchema, _ = json.Marshal(queryObj)
	update := fields()
	delete(update, "academic_year")
	delete(update, "term")
	delete(update, "teaching_week")
	update["id"] = str(128)
	update["mode"] = map[string]any{"enum": []string{"TEMPORARY", "PERMANENT"}}
	update["event_status"] = map[string]any{"enum": []string{"INCOMPLETE", "COMPLETED"}}
	for _, tool := range []struct {
		name, operation, permission, description string
		input, output                            json.RawMessage
	}{
		{"schedule_query", "schedule.query", "schedule.read", "查询当前用户的有效日程。按学年/学期/教学周，或带时区的from/to时间范围查询；范围最长366天，区间[from,to)重叠匹配。按start_time,id排序，默认100条，limit最大1000，offset最大100000；用next_offset直到空页。教学周不是自然周。", querySchema, object(map[string]any{"items": map[string]any{"type": "array", "items": item}, "offset": integer(0, 100000), "next_offset": integer(0, 101000)}, "items", "offset", "next_offset")},
		{"schedule_get", "schedule.get", "schedule.read", "按id读取当前用户的日程详情，包含有效状态、原始课表安排、临时/永久修改和历史。可能返回inactive历史记录；不要据此认定当前仍有效。", object(map[string]any{"id": str(128)}, "id"), item},
		{"schedule_create", "schedule.create", "schedule.write", "为当前用户创建单次日程。必须明确学年、学期、教学周、带时区起止时间与标题；时间精确到分钟，时长不超过7天。来源为AGENT；不推断校历、不发送通知。", object(fields(), "academic_year", "term", "teaching_week", "start_time", "end_time", "title"), item},
		{"schedule_update", "schedule.update", "schedule.write", "修改日程或完成状态。先get核实；mode必填TEMPORARY/PERMANENT。课表TEMPORARY只改选定周，PERMANENT影响同系列当前及后续周；event_status仅作用于选定日程。没有预期版本参数或撤销保证，不可盲重试。", object(update, "id", "mode"), item},
		{"schedule_delete", "schedule.delete", "schedule.delete", "按id删除日程，需要明确授权。课表记录保留删除标记与历史，手动/Agent日程永久删除。无期望版本参数；先读取核实，不可凭重复通知自动删除。", object(map[string]any{"id": str(128)}, "id"), object(map[string]any{"deleted": map[string]any{"type": "boolean"}}, "deleted")},
	} {
		op := tool.operation
		handler := func(ctx context.Context, e f.ExecutionContext, raw json.RawMessage) (any, error) {
			if port == nil {
				return nil, f.Fail("NOT_IMPLEMENTED", "schedule service unavailable")
			}
			if e.Scope.OwnerID == "" {
				return nil, f.Fail("PERMISSION_DENIED", "trusted owner required")
			}
			var q model.ScheduleQuery
			if op == "schedule.query" {
				if json.Unmarshal(raw, &q) != nil {
					return nil, f.Fail("INVALID_ARGUMENT", "invalid schedule dates")
				}
				if (q.AcademicYear != 0 || q.Term != 0) && (q.AcademicYear < 2000 || q.AcademicYear > 2100 || q.Term < 1 || q.Term > 2) {
					return nil, f.Fail("INVALID_ARGUMENT", "academic_year and term must be supplied together")
				}
				if q.TeachingWeek != 0 && (q.AcademicYear == 0 || q.Term == 0) {
					return nil, f.Fail("INVALID_ARGUMENT", "teaching week requires semester")
				}
				if !q.From.IsZero() || !q.To.IsZero() {
					if q.From.IsZero() || q.To.IsZero() || !q.To.After(q.From) || q.To.Sub(q.From) > 366*24*time.Hour {
						return nil, f.Fail("INVALID_ARGUMENT", "invalid schedule time range")
					}
				} else if q.TeachingWeek == 0 {
					return nil, f.Fail("INVALID_ARGUMENT", "semester/week or time range required")
				}
			}
			reply, err := port.Call(ctx, bridge.Request{Version: bridge.Version, OwnerID: e.Scope.OwnerID, Operation: op, Payload: raw})
			if errors.Is(err, model.ErrInvalidArgument) || errors.Is(err, bridge.ErrInvalid) {
				return nil, f.Fail("INVALID_ARGUMENT", "invalid schedule arguments")
			}
			if errors.Is(err, model.ErrNotFound) {
				return nil, f.Fail("RESOURCE_NOT_FOUND", "schedule not found in current user scope")
			}
			if errors.Is(err, model.ErrConflict) {
				return nil, f.Fail("CONFLICT", "schedule change conflicts with current state")
			}
			if errors.Is(err, bridge.ErrUnsupported) {
				return nil, f.Fail("NOT_IMPLEMENTED", "schedule operation unavailable")
			}
			if err != nil {
				return nil, err
			}
			if reply.Version != bridge.Version || reply.Operation != op {
				return nil, f.Fail("INTERNAL_ERROR", "invalid schedule bridge reply")
			}
			if op == "schedule.query" {
				var rows []model.Schedule
				if json.Unmarshal(reply.Payload, &rows) != nil {
					return nil, f.Fail("INTERNAL_ERROR", "invalid schedule query result")
				}
				limit := q.Limit
				if limit == 0 {
					limit = 100
				}
				if len(rows) > limit {
					return nil, f.Fail("INTERNAL_ERROR", "schedule result exceeds requested limit")
				}
				if rows == nil {
					rows = []model.Schedule{}
				}
				for _, row := range rows {
					if row.UserID != e.Scope.OwnerID || !row.Active {
						return nil, f.Fail("INTERNAL_ERROR", "schedule result outside current scope")
					}
				}
				return map[string]any{"items": rows, "offset": q.Offset, "next_offset": q.Offset + len(rows)}, nil
			}
			if op != "schedule.delete" {
				var row model.Schedule
				if json.Unmarshal(reply.Payload, &row) != nil || row.UserID != e.Scope.OwnerID {
					return nil, f.Fail("INTERNAL_ERROR", "schedule result outside current scope")
				}
			}
			return reply.Payload, nil
		}
		if err := tools.Register(f.ToolSpec{Name: tool.name, Description: tool.description, Permissions: []string{tool.permission}, InputSchema: tool.input, OutputSchema: tool.output, Timeout: 20 * time.Second}, handler); err != nil {
			return err
		}
	}
	return nil
}
