package dbbridge

import (
	"context"
	"encoding/json"
	"testing"

	f "academic/agent/framework"
	agentservice "academic/backend/internal/agent"
	"academic/backend/internal/model"
	"academic/backend/internal/schedule"
)

type scheduleToolModel struct {
	tool, args string
	result     f.ToolResult
}

func (m *scheduleToolModel) Complete(_ context.Context, in f.ModelRequest) (f.ModelResponse, error) {
	last := in.Messages[len(in.Messages)-1]
	if last.Role == "tool" {
		if err := json.Unmarshal([]byte(last.Content), &m.result); err != nil {
			return f.ModelResponse{}, err
		}
		return f.ModelResponse{Message: f.Message{Role: "assistant", Content: "checked"}, FinishReason: "stop"}, nil
	}
	return f.ModelResponse{Message: f.Message{Role: "assistant", ToolCalls: []f.ToolCall{{ID: "schedule-test", Type: "function", Function: f.FunctionCall{Name: m.tool, Arguments: m.args}}}}, FinishReason: "tool_calls"}, nil
}

// A real PostgreSQL repository, Runtime, validated Tool Registry and schedule
// Bridge are exercised together; no external model or platform is contacted.
func TestAgentScheduleToolsPersisted(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	u := fixtureUser(t, db, "schedule-tools-a", model.RoleUser)
	v := fixtureUser(t, db, "schedule-tools-b", model.RoleUser)
	tools := []string{"schedule_query", "schedule_get", "schedule_create", "schedule_update", "schedule_delete"}
	run := func(owner, tool, args, wantError string, permissions ...string) any {
		t.Helper()
		m := &scheduleToolModel{tool: tool, args: args}
		service, err := agentservice.New(agentservice.Dependencies{Repository: db.Agents(), Providers: map[string]f.ModelProvider{"schedule-mock": m}, Schedules: schedule.New(db.Schedules(), nil)})
		must(t, err)
		a, err := service.Registry.Create(ctx, owner, definition("schedule tool test", "schedule-mock", tools, permissions))
		must(t, err)
		_, err = service.Run(ctx, owner, f.RunRequest{AgentID: a.ID, Trigger: f.SystemEvent, Input: json.RawMessage(`{}`)})
		must(t, err)
		if wantError != "" {
			if m.result.OK || m.result.Error == nil || m.result.Error.Code != wantError {
				t.Fatalf("%s: want %s got %+v", tool, wantError, m.result)
			}
		} else if !m.result.OK {
			t.Fatalf("%s: %+v", tool, m.result)
		}
		return m.result.Data
	}
	create := `{"academic_year":2026,"term":1,"teaching_week":8,"start_time":"2026-11-05T14:00:00+08:00","end_time":"2026-11-05T15:00:00+08:00","title":"数分工具验收"}`
	created := run(u.ID, "schedule_create", create, "", "schedule.write").(map[string]any)
	id := created["id"].(string)
	if created["source"] != "AGENT" || created["user_id"] != u.ID {
		t.Fatal("wrong source or owner", created)
	}
	get := `{"id":"` + id + `"}`
	run(v.ID, "schedule_get", get, "RESOURCE_NOT_FOUND", "schedule.read")
	run(v.ID, "schedule_update", `{"id":"`+id+`","mode":"TEMPORARY","title":"unauthorized"}`, "RESOURCE_NOT_FOUND", "schedule.write")
	run(v.ID, "schedule_delete", get, "RESOURCE_NOT_FOUND", "schedule.delete")
	run(u.ID, "schedule_get", get, "PERMISSION_DENIED")
	run(u.ID, "schedule_delete", get, "PERMISSION_DENIED", "schedule.read")
	run(u.ID, "schedule_create", create, "PERMISSION_DENIED", "schedule.read")
	run(u.ID, "schedule_get", `{"id":"`+id+`","owner_user_id":"`+v.ID+`"}`, "INVALID_ARGUMENT", "schedule.read")
	for _, args := range []string{`{}`, `{"from":"invalid","to":"2026-11-06T00:00:00+08:00"}`, `{"from":"2026-11-06T00:00:00+08:00","to":"2026-11-05T00:00:00+08:00"}`, `{"from":"2026-01-01T00:00:00+08:00","to":"2028-01-01T00:00:00+08:00"}`, `{"academic_year":2026,"term":1,"teaching_week":8,"limit":1001}`, `{"from":"2026-11-05T00:00:00+08:00","to":"2026-11-06T00:00:00+08:00","academic_year":2026}`} {
		run(u.ID, "schedule_query", args, "INVALID_ARGUMENT", "schedule.read")
	}
	q := `{"academic_year":2026,"term":1,"teaching_week":8,"limit":1}`
	page := run(u.ID, "schedule_query", q, "", "schedule.read").(map[string]any)
	if len(page["items"].([]any)) != 1 || page["next_offset"] != float64(1) {
		t.Fatal("bad page", page)
	}
	for _, args := range []string{`{"academic_year":2026,"term":1,"teaching_week":8,"offset":1}`, `{"from":"2026-11-06T00:00:00+08:00","to":"2026-11-07T00:00:00+08:00"}`} {
		page = run(u.ID, "schedule_query", args, "", "schedule.read").(map[string]any)
		if len(page["items"].([]any)) != 0 {
			t.Fatal("expected empty page", page)
		}
	}
	page = run(v.ID, "schedule_query", q, "", "schedule.read").(map[string]any)
	if len(page["items"].([]any)) != 0 {
		t.Fatal("cross-owner list leak")
	}
	run(u.ID, "schedule_query", `{"from":"2026-11-05T14:30:00+08:00","to":"2026-11-05T14:45:00+08:00"}`, "", "schedule.read")
	updated := run(u.ID, "schedule_update", `{"id":"`+id+`","mode":"TEMPORARY","title":"数分已修改","event_status":"COMPLETED"}`, "", "schedule.write").(map[string]any)
	if updated["title"] != "数分已修改" || updated["event_status"] != "COMPLETED" {
		t.Fatal("update not applied")
	}
	read := run(u.ID, "schedule_get", get, "", "schedule.read").(map[string]any)
	if read["title"] != "数分已修改" {
		t.Fatal("update not persisted")
	}
	run(u.ID, "schedule_delete", get, "", "schedule.delete")
	run(u.ID, "schedule_get", get, "RESOURCE_NOT_FOUND", "schedule.read")
}
