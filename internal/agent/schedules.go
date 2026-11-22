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
	return registerScheduleTools(tools, port, false)
}

// RegisterScheduleReadTools exposes only query/get, so a staging Runtime has no
// schedule write handler even if a misconfigured Agent requests write permission.
func RegisterScheduleReadTools(tools *f.ToolRegistry, port bridge.Port) error {
	return registerScheduleTools(tools, scheduleReadPort{port}, true)
}

type scheduleReadPort struct{ backend bridge.Port }

func (p scheduleReadPort) Call(ctx context.Context, r bridge.Request) (bridge.Reply, error) {
	if r.Operation != "schedule.query" && r.Operation != "schedule.get" {
		return bridge.Reply{}, f.Fail("PERMISSION_DENIED", "Learner staging only allows schedule reads")
	}
	if p.backend == nil {
		return bridge.Reply{}, f.Fail("NOT_IMPLEMENTED", "schedule service unavailable")
	}
	return p.backend.Call(ctx, r)
}

func registerScheduleTools(tools *f.ToolRegistry, port bridge.Port, readOnly bool) error {
	item := scheduleItemSchema()
	id := func(description string) json.RawMessage {
		return scheduleObject(map[string]any{"id": scheduleText(128, description)}, "id")
	}
	page := scheduleObject(map[string]any{
		"items":             map[string]any{"type": "array", "items": item, "description": "Active occurrences in start_time/id order; an empty page ends iteration."},
		"offset":            scheduleInt(0, scheduleMaxOffset, "Offset used for this page."),
		"next_offset":       scheduleInt(0, 101000, "Offset after this page. Continue only when items is nonempty and page_limit_reason is absent; an offset_limit marker means this value cannot be used."),
		"page_limit_reason": scheduleEnum("Present as offset_limit when the next offset exceeds the accepted query boundary. Stop instead of retrying the same or an out-of-range offset.", "offset_limit"),
	}, "items", "offset", "next_offset")
	for _, tool := range []struct {
		name, operation, permission, description string
		input, output                            json.RawMessage
	}{
		{"schedule_query", "schedule.query", "schedule.read", "查询当前用户的有效日程。按学年/学期/教学周，或带时区的from/to时间范围查询；范围最长366天，区间[from,to)重叠匹配。两类条件同时提供时按AND组合。按start_time,id排序，默认100条，limit最大1000，offset最大100000；空页结束，page_limit_reason=offset_limit时停止，不再使用next_offset。教学周不是自然周。", scheduleQuerySchema(), page},
		{"schedule_get", "schedule.get", "schedule.read", "按id读取当前用户的日程详情，包含有效状态、原始课表安排、临时/永久修改和历史。可能返回inactive历史记录；不要据此认定当前仍有效。", id("Persisted occurrence ID returned by schedule_query/create/update; not a course, source or series ID."), item},
		{"schedule_create", "schedule.create", "schedule.write", "为当前用户创建单次日程。必须明确学年、学期、教学周、带时区起止时间与标题；时间精确到分钟，时长不超过7天。来源为AGENT，初始event_status=INCOMPLETE、active=true；不推断校历、不发送通知。超时或响应未知先核对，不盲重试。", scheduleObject(scheduleFieldsSchema(), "academic_year", "term", "teaching_week", "start_time", "end_time", "title"), item},
		{"schedule_update", "schedule.update", "schedule.write", "修改日程或完成状态。先get核实；mode必填TEMPORARY/PERMANENT，至少提供一个修改字段。省略字段保留；details空串清除，null不接受。课表TEMPORARY只改选定周，PERMANENT影响同系列当前及后续周；event_status仅作用于选定日程。返回选定条目，不代表只影响这一条；没有预期版本参数或撤销保证，不可盲重试。", scheduleUpdateSchema(), item},
		{"schedule_delete", "schedule.delete", "schedule.delete", "按id删除日程，需要明确授权。课表记录保留删除标记与历史，手动/Agent日程永久删除。无期望版本参数；先读取核实，不可凭重复通知自动删除。重复删除会返回找不到，不保证幂等成功。", id("Current user's persisted occurrence ID; read and verify scope and source before deletion."), scheduleObject(map[string]any{"deleted": map[string]any{"type": "boolean", "description": "True only when this delete operation completed; not an undo receipt."}}, "deleted")},
	} {
		op := tool.operation
		if readOnly && op != "schedule.query" && op != "schedule.get" {
			continue
		}
		handler := func(ctx context.Context, e f.ExecutionContext, raw json.RawMessage) (any, error) {
			if port == nil {
				return nil, f.Fail("NOT_IMPLEMENTED", "schedule service unavailable")
			}
			if e.Scope.OwnerID == "" {
				return nil, f.Fail("PERMISSION_DENIED", "trusted owner required")
			}
			if op == "schedule.create" || op == "schedule.update" {
				var patch struct {
					Title   *string    `json:"title"`
					Details *string    `json:"details"`
					Start   *time.Time `json:"start_time"`
					End     *time.Time `json:"end_time"`
					Event   *string    `json:"event_status"`
				}
				if json.Unmarshal(raw, &patch) != nil {
					return nil, f.Fail("INVALID_ARGUMENT", "invalid schedule arguments or RFC3339 dates")
				}
				if patch.Title != nil && len(*patch.Title) > 1000 {
					return nil, f.Fail("INVALID_ARGUMENT", "title exceeds the 1000 UTF-8 byte limit; shorten the title before retrying")
				}
				if patch.Details != nil && len(*patch.Details) > 16384 {
					return nil, f.Fail("INVALID_ARGUMENT", "details exceeds the 16384 UTF-8 byte limit; shorten details before retrying")
				}
				if op == "schedule.update" && patch.Title == nil && patch.Details == nil && patch.Start == nil && patch.End == nil && patch.Event == nil {
					return nil, f.Fail("INVALID_ARGUMENT", "schedule_update requires at least one of start_time, end_time, title, details or event_status")
				}
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
				page := map[string]any{"items": rows, "offset": q.Offset, "next_offset": q.Offset + len(rows)}
				if q.Offset+len(rows) > scheduleMaxOffset {
					page["page_limit_reason"] = "offset_limit"
				}
				return page, nil
			}
			if op != "schedule.delete" {
				var row model.Schedule
				if json.Unmarshal(reply.Payload, &row) != nil || row.UserID != e.Scope.OwnerID {
					return nil, f.Fail("INTERNAL_ERROR", "schedule result outside current scope")
				}
			}
			return reply.Payload, nil
		}
		if err := tools.Register(f.ToolSpec{Name: tool.name, Description: tool.description, Permissions: []string{tool.permission}, InputSchema: tool.input, OutputSchema: tool.output, Timeout: 20 * time.Second, ReadOnly: op == "schedule.query" || op == "schedule.get"}, handler); err != nil {
			return err
		}
	}
	return nil
}
